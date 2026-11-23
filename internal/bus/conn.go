package bus

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// The protocol on a listener: one JSON object per line, each way. A client says hello first, with its user name
// and token. Then it may publish, subscribe (live events on a filter, from now on), consume (a durable consumer of
// its own, by name) and ack (it is done with a consumer's events up to a sequence number). The server answers
// each request that carries an ID with ok or error, and sends event and gap frames.
type frame struct {
	Op       string          `json:"op"`
	ID       int64           `json:"id,omitempty"`
	User     string          `json:"user,omitempty"`
	Token    string          `json:"token,omitempty"`
	Topic    string          `json:"topic,omitempty"`
	Filter   string          `json:"filter,omitempty"`
	Consumer string          `json:"consumer,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Durable  bool            `json:"durable,omitempty"`
	Seq      int64           `json:"seq,omitempty"`
	Message  string          `json:"message,omitempty"`
}

const (
	maxFrame     = 1 << 20
	queued       = 256              // frames waiting for a client; a client this far behind is disconnected
	window       = 64               // events a consumer is sent before it acks
	writeTimeout = 10 * time.Second // a client that stops reading loses its connection
	helloTimeout = 10 * time.Second
)

type conn struct {
	b      *Bus
	nc     net.Conn
	grants Grants
	user   string
	out    chan []byte
	gone   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	acks   map[string]chan int64 // the consumers this connection reads, by name
	stops  []func()
}

func (b *Bus) serve(l net.Listener) {
	defer b.done.Done()
	for {
		nc, err := l.Accept()
		if err != nil {
			select {
			case <-b.closed:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			b.log.Warn("bus accept failed", "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		c := &conn{b: b, nc: nc, out: make(chan []byte, queued), gone: make(chan struct{}), acks: map[string]chan int64{}}
		b.mu.Lock()
		select {
		case <-b.closed:
			b.mu.Unlock()
			_ = nc.Close()
			return
		default:
		}
		b.conns[c] = struct{}{}
		b.done.Add(2)
		b.mu.Unlock()
		go c.write()
		go c.read()
	}
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.gone)
		_ = c.nc.Close()
		c.mu.Lock()
		for _, stop := range c.stops {
			stop()
		}
		c.mu.Unlock()
		c.b.mu.Lock()
		delete(c.b.conns, c)
		c.b.mu.Unlock()
	})
}

func (c *conn) write() {
	defer c.b.done.Done()
	for {
		select {
		case line := <-c.out:
			_ = c.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := c.nc.Write(line); err != nil {
				c.close()
				return
			}
		case <-c.gone:
			return
		}
	}
}

// send queues f for the client; a client too far behind is disconnected rather than holding up the bus.
func (c *conn) send(f frame) bool {
	line, err := json.Marshal(f)
	if err != nil {
		return false
	}
	select {
	case c.out <- append(line, '\n'):
		return true
	case <-c.gone:
		return false
	default:
		c.b.log.Warn("bus client too slow; disconnected", "user", c.user)
		c.close()
		return false
	}
}

func (c *conn) reply(id int64, err error) {
	if id == 0 {
		return
	}
	if err != nil {
		c.send(frame{Op: "error", ID: id, Message: err.Error()})
		return
	}
	c.send(frame{Op: "ok", ID: id})
}

func (c *conn) read() {
	defer c.b.done.Done()
	defer c.close()
	scanner := bufio.NewScanner(c.nc)
	scanner.Buffer(make([]byte, 4096), maxFrame)
	_ = c.nc.SetReadDeadline(time.Now().Add(helloTimeout))
	var hello frame
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &hello) != nil || hello.Op != "hello" {
		return
	}
	grants, known := c.b.opt.Grants[hello.User]
	if !known || c.b.opt.Authenticate == nil || !c.b.opt.Authenticate(hello.User, hello.Token) {
		c.send(frame{Op: "error", ID: hello.ID, Message: "not authorized"})
		return
	}
	c.user, c.grants = hello.User, grants
	_ = c.nc.SetReadDeadline(time.Time{})
	c.reply(hello.ID, nil)
	for scanner.Scan() {
		var f frame
		if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
			c.send(frame{Op: "error", Message: "not a frame"})
			return
		}
		switch f.Op {
		case "publish":
			c.reply(f.ID, c.publish(f))
		case "subscribe":
			c.reply(f.ID, c.subscribe(f.Filter))
		case "consume":
			c.consume(f)
		case "ack":
			c.mu.Lock()
			acks := c.acks[f.Consumer]
			c.mu.Unlock()
			if acks != nil {
				select {
				case acks <- f.Seq:
				case <-c.gone:
				}
			}
		default:
			c.reply(f.ID, errors.New("unknown op"))
		}
	}
}

var errNotGranted = errors.New("not granted")

func (c *conn) publish(f frame) error {
	if !validTopic(f.Topic) || !allowed(c.grants.Publish, f.Topic) {
		return errNotGranted
	}
	return c.b.Publish(f.Topic, f.Payload, f.Durable)
}

func (c *conn) subscribe(filter string) error {
	if !validFilter(filter) || !allowed(c.grants.Subscribe, filter) {
		return errNotGranted
	}
	stop, err := c.b.subscribe(filter, func(m Message) {
		c.send(frame{Op: "event", Topic: m.Topic, Payload: m.Payload, Seq: m.Seq})
	})
	if err == nil {
		c.mu.Lock()
		c.stops = append(c.stops, stop)
		c.mu.Unlock()
	}
	return err
}

// consume attaches the client's consumer f.Consumer and sends it the durable events after its cursor, at most
// window of them before an ack. A consumer's name is the client's own: user/name.
func (c *conn) consume(f frame) {
	if f.Consumer == "" || strings.ContainsRune(f.Consumer, 0) || !validFilter(f.Filter) || !allowed(c.grants.Subscribe, f.Filter) {
		c.reply(f.ID, errNotGranted)
		return
	}
	c.mu.Lock()
	_, reading := c.acks[f.Consumer]
	c.mu.Unlock()
	if reading {
		c.reply(f.ID, errors.New("already consuming"))
		return
	}
	a, cursor, gap, err := c.b.attach(c.user+"/"+f.Consumer, f.Filter)
	if err != nil {
		c.reply(f.ID, err)
		return
	}
	acks := make(chan int64)
	c.mu.Lock()
	c.acks[f.Consumer] = acks
	c.stops = append(c.stops, func() { c.b.release(a) })
	c.mu.Unlock()
	c.reply(f.ID, nil)
	if gap && !c.send(frame{Op: "gap", Consumer: f.Consumer}) {
		return
	}
	c.b.done.Add(1)
	go c.deliver(a, f.Consumer, f.Filter, cursor, acks)
}

func (c *conn) deliver(a *attachment, name, filter string, cursor int64, acks <-chan int64) {
	b := c.b
	defer b.done.Done()
	defer b.release(a)
	sent := cursor
	var inflight []int64
	save := func(to int64) {
		if to > cursor {
			cursor = to
			if err := b.setCursor(a.name, cursor); err != nil {
				b.log.Error("bus consumer cursor not saved", "consumer", a.name, "error", err)
			}
		}
	}
	for {
		wake := b.waitFor()
		more := false
		if len(inflight) < window {
			msgs, err := b.read(sent)
			if err != nil {
				b.log.Error("bus consumer read failed", "consumer", a.name, "error", err)
			}
			for _, m := range msgs {
				if len(inflight) >= window {
					break
				}
				if match(filter, m.Topic) {
					if !c.send(frame{Op: "event", Consumer: name, Topic: m.Topic, Payload: m.Payload, Seq: m.Seq}) {
						return
					}
					inflight = append(inflight, m.Seq)
				}
				sent = m.Seq
			}
			if len(inflight) == 0 {
				save(sent) // nothing waits for an ack: the events read were not this consumer's
			}
			more = len(msgs) == batch && len(inflight) < window
		}
		if more {
			continue
		}
		select {
		case seq := <-acks:
			if seq > sent {
				seq = sent
			}
			for len(inflight) > 0 && inflight[0] <= seq {
				inflight = inflight[1:]
			}
			if len(inflight) == 0 {
				seq = sent
			}
			save(seq)
		case <-wake:
		case <-a.stop:
			return
		case <-c.gone:
			return
		case <-b.closed:
			return
		}
	}
}

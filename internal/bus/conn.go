package bus

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// The protocol on a listener: one JSON object per line, each way. A client says hello first, with its user name
// and token. Then it may publish, subscribe (live events on a filter, from now on), consume (a durable consumer of
// its own, by name) and ack (it is done with a consumer's events, or its gap, up to a sequence number). The server
// answers each request that carries an ID with ok or error, and sends event, gap and detached frames: detached
// when another connection took a consumer over.
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
	maxFrame     = 1 << 20          // a payload is at most maxPayload, half of it
	queued       = 256              // frames waiting for a client; a client this far behind is disconnected
	window       = 64               // events a consumer is sent before it acks
	writeTimeout = 10 * time.Second // a client that stops reading loses its connection
	helloTimeout = 10 * time.Second
	maxReads     = 64  // consumers a connection reads
	maxSubs      = 256 // subscriptions a connection holds
	maxName      = 128 // bytes of a consumer's name
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
	reads  map[string]*reading // the consumers this connection reads, by the client's name
	stops  []func()            // its subscriptions
}

// reading is a consumer a connection reads: acks for it are noted here and never block the connection. Events have
// positive sequence numbers; a gap frame carries a negative token of its own, and only an ack of that token is
// the gap's.
type reading struct {
	a        *attachment
	mu       sync.Mutex
	acked    int64
	gapToken int64 // the token of the gap frame sent last; 0 while none is pending
	gapAcked bool
	signal   chan struct{}
}

func (r *reading) ack(seq int64) {
	r.mu.Lock()
	if seq < 0 {
		// Only the pending gap's own token counts: an old one can neither clear it nor undo its ack.
		if seq == r.gapToken {
			r.gapAcked = true
		}
	} else {
		r.acked = max(r.acked, seq)
	}
	r.mu.Unlock()
	select {
	case r.signal <- struct{}{}:
	default:
	}
}

func (r *reading) latest() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acked, r.gapAcked
}

// expectGap records the token of the gap frame about to be sent; an ack of any other token is not this gap's.
func (r *reading) expectGap(token int64) {
	r.mu.Lock()
	r.gapToken, r.gapAcked = token, false
	r.mu.Unlock()
}

func (b *Bus) serve(l net.Listener) {
	defer b.done.Done()
	for {
		nc, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			b.log.Warn("bus accept failed", "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		c := &conn{b: b, nc: nc, out: make(chan []byte, queued), gone: make(chan struct{}), reads: map[string]*reading{}}
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
		stops := c.stops
		reads := make([]*reading, 0, len(c.reads))
		for _, r := range c.reads {
			reads = append(reads, r)
		}
		c.mu.Unlock()
		for _, stop := range stops {
			stop()
		}
		for _, r := range reads {
			c.b.release(r.a)
		}
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
	line, err := encode(f)
	if err != nil {
		return false
	}
	select {
	case c.out <- line:
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
		// Written here, not queued: the connection closes next, and the client is owed the reason.
		if line, err := json.Marshal(frame{Op: "error", ID: hello.ID, Message: "not authorized"}); err == nil {
			_ = c.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
			_, _ = c.nc.Write(append(line, '\n'))
		}
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
			c.reply(f.ID, c.consume(f))
		case "ack":
			c.mu.Lock()
			r := c.reads[f.Consumer]
			c.mu.Unlock()
			if r != nil {
				r.ack(f.Seq)
			}
		default:
			c.reply(f.ID, errors.New("unknown op"))
		}
	}
}

// encode writes f as a line, without escaping <, > and &: a payload then takes its own size in the frame.
func encode(f frame) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(f)
	return buf.Bytes(), err
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
	c.mu.Lock()
	full := len(c.stops) >= maxSubs
	c.mu.Unlock()
	if full {
		return errors.New("too many subscriptions")
	}
	stop, err := c.b.subscribe(filter, func(m Message) {
		c.send(frame{Op: "event", Topic: m.Topic, Payload: m.Payload, Seq: m.Seq})
	})
	if err == nil {
		c.mu.Lock()
		select {
		case <-c.gone: // closed while registering: close took its snapshot of the stops without this one
			c.mu.Unlock()
			stop()
			return nil
		default:
		}
		c.stops = append(c.stops, stop)
		c.mu.Unlock()
	}
	return err
}

// consume attaches the client's consumer f.Consumer and starts sending it what is pending: its gap, if one is,
// then the durable events after its cursor, at most window of them before an ack.
func (c *conn) consume(f frame) error {
	if f.Consumer == "" || len(f.Consumer) > maxName || strings.ContainsRune(f.Consumer, 0) || !validFilter(f.Filter) ||
		!allowed(c.grants.Subscribe, f.Filter) {
		return errNotGranted
	}
	c.mu.Lock()
	_, already := c.reads[f.Consumer]
	full := len(c.reads) >= maxReads
	c.mu.Unlock()
	if already {
		return errors.New("already consuming")
	}
	if full {
		return errors.New("too many consumers")
	}
	if !c.b.track() {
		return errors.New("bus: closed")
	}
	a, cursor, gap, err := c.b.attach(clientConsumer(c.user, f.Consumer), f.Filter)
	if err != nil {
		c.b.done.Done()
		return err
	}
	r := &reading{a: a, signal: make(chan struct{}, 1)}
	c.mu.Lock()
	c.reads[f.Consumer] = r
	c.mu.Unlock()
	go c.deliver(r, f.Consumer, f.Filter, cursor, gap)
	return nil
}

// deliver sends a consumer its gap and its events. The client acks a gap with the gap frame's seq.
func (c *conn) deliver(r *reading, name, filter string, cursor int64, gap bool) {
	b, a := c.b, r.a
	defer b.done.Done()
	defer func() {
		b.mu.Lock()
		takenOver := a.taken
		b.mu.Unlock()
		b.release(a)
		c.mu.Lock()
		if c.reads[name] == r {
			delete(c.reads, name)
		}
		c.mu.Unlock()
		if takenOver {
			c.send(frame{Op: "detached", Consumer: name}) // the client may consume the name again
		}
	}()
	sent := cursor
	var inflight []int64
	gapSent := false
	for {
		wake := b.waitFor()
		var pause <-chan time.Time
		switch {
		case gap:
			if !gapSent {
				gapToken := -b.owners.Add(1)
				r.expectGap(gapToken)
				if !c.send(frame{Op: "gap", Consumer: name, Seq: gapToken}) {
					return
				}
				gapSent = true
			}
		case len(inflight) < window:
			msgs, gapTo, err := b.read(sent)
			switch {
			case err != nil:
				b.log.Error("bus consumer read failed", "consumer", a.name, "error", err)
				pause = time.After(readRetry)
			case gapTo > 0:
				// Events after the last one sent are gone. What was sent and not acked is sent again after the gap.
				b.markGap(a, gapTo)
				cursor, sent, inflight, gap, gapSent = gapTo, gapTo, nil, true, false
				continue
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
			if len(inflight) == 0 && sent > cursor {
				cursor = sent // nothing waits for an ack: the events read were not this consumer's
				b.saveCursor(a, cursor)
			}
			if len(msgs) == batch && len(inflight) < window {
				continue
			}
		}
		select {
		case <-r.signal:
			seq, gapAcked := r.latest()
			if gap {
				if gapSent && gapAcked {
					if !b.retry(a, func() error { return b.clearGap(a) }) {
						return
					}
					r.expectGap(0)
					gap = false
				}
				continue
			}
			for len(inflight) > 0 && inflight[0] <= seq {
				inflight = inflight[1:]
			}
			if len(inflight) == 0 {
				seq = sent
			}
			if seq > cursor && seq <= sent {
				cursor = seq
				b.saveCursor(a, cursor)
			}
		case <-wake:
		case <-pause:
		case <-a.stop:
			return
		case <-c.gone:
			return
		case <-b.closed:
			return
		}
	}
}

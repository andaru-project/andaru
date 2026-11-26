package bus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// Client is a connection to the bus from another process. Events for its subscriptions and consumers arrive on
// Messages; a consumer's events, and its gap, are acked with Ack once handled. Replies never wait for Messages to
// be read.
type Client struct {
	nc       net.Conn
	writeMu  sync.Mutex
	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan error
	queue    []Delivery
	bytes    int // payload bytes in queue
	queued   *sync.Cond
	messages chan Delivery
	done     chan struct{}
	closing  chan struct{}
	once     sync.Once
	err      error
}

// clientQueue and clientQueueBytes bound the deliveries a client may leave unread before its connection is closed,
// as the bus does with a client that stops reading. A durable consumer then reads on from its cursor.
const (
	clientQueue      = 4096
	clientQueueBytes = 64 << 20
)

// Delivery is what a client got: an event, a gap or the loss of a consumer to another connection (Detached).
// Consumer names the consumer it is for, or is empty for a subscription.
type Delivery struct {
	Consumer string
	Detached bool
	Message
}

// Dial connects to the bus on a Unix socket as user, with token.
func Dial(ctx context.Context, socket, user, token string) (*Client, error) {
	nc, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := &Client{nc: nc, pending: map[int64]chan error{}, messages: make(chan Delivery), done: make(chan struct{}),
		closing: make(chan struct{})}
	c.queued = sync.NewCond(&c.mu)
	go c.read()
	go c.pump()
	if err = c.request(ctx, frame{Op: "hello", User: user, Token: token}); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Messages are the events, gaps and detachments for the client. It is closed when the connection ends.
func (c *Client) Messages() <-chan Delivery { return c.messages }

// Publish sends an event; a durable one is in the bus's log when Publish returns.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte, durable bool) error {
	return c.request(ctx, frame{Op: "publish", Topic: topic, Payload: payload, Durable: durable})
}

// Subscribe asks for the events on filter published from now on.
func (c *Client) Subscribe(ctx context.Context, filter string) error {
	return c.request(ctx, frame{Op: "subscribe", Filter: filter})
}

// Consume reads the durable consumer name: its gap if one is pending, then the durable events on filter after its
// cursor.
func (c *Client) Consume(ctx context.Context, name, filter string) error {
	return c.request(ctx, frame{Op: "consume", Consumer: name, Filter: filter})
}

// Ack says the consumer is done with its events up to seq, or with its gap when seq is the gap's.
func (c *Client) Ack(name string, seq int64) error {
	return c.write(context.Background(), frame{Op: "ack", Consumer: name, Seq: seq})
}

// Close ends the connection; deliveries not yet read are dropped.
// Err is why the connection ended, or nil while it lasts: a request refused on a live connection is the bus's answer to
// that request, not a lost connection.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) Close() error {
	c.once.Do(func() { close(c.closing) })
	return c.nc.Close()
}

func (c *Client) write(ctx context.Context, f frame) error {
	line, err := encode(f)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(writeTimeout)
	}
	_ = c.nc.SetWriteDeadline(deadline)
	if _, err = c.nc.Write(line); err != nil {
		// Part of the line may have gone: whatever follows would be read as its rest.
		_ = c.nc.Close()
	}
	return err
}

func (c *Client) request(ctx context.Context, f frame) error {
	reply := make(chan error, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.nextID++
	f.ID = c.nextID
	c.pending[f.ID] = reply
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, f.ID)
		c.mu.Unlock()
	}
	if err := c.write(ctx, f); err != nil {
		forget()
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		forget()
		return ctx.Err()
	case <-c.done:
		select {
		case err := <-reply: // a reply that came before the connection ended stands
			return err
		default:
			return c.err
		}
	}
}

// read takes frames off the connection: replies go to their requests at once, deliveries to the queue pump sends
// to Messages.
func (c *Client) read() {
	scanner := bufio.NewScanner(c.nc)
	scanner.Buffer(make([]byte, 4096), maxFrame)
	for scanner.Scan() {
		var f frame
		if json.Unmarshal(scanner.Bytes(), &f) != nil {
			break
		}
		var d *Delivery
		switch f.Op {
		case "ok", "error":
			c.mu.Lock()
			reply := c.pending[f.ID]
			delete(c.pending, f.ID)
			c.mu.Unlock()
			if reply != nil {
				if f.Op == "error" {
					reply <- errors.New("bus: " + f.Message)
				} else {
					reply <- nil
				}
			}
		case "event":
			d = &Delivery{Consumer: f.Consumer, Message: Message{Seq: f.Seq, Topic: f.Topic, Payload: f.Payload}}
		case "gap":
			d = &Delivery{Consumer: f.Consumer, Message: Message{Seq: f.Seq, Gap: true}}
		case "detached":
			d = &Delivery{Consumer: f.Consumer, Detached: true}
		}
		if d != nil {
			c.mu.Lock()
			full := len(c.queue) >= clientQueue || c.bytes+len(d.Payload) > clientQueueBytes
			if !full {
				c.queue = append(c.queue, *d)
				c.bytes += len(d.Payload)
				c.queued.Signal()
			}
			c.mu.Unlock()
			if full {
				_ = c.nc.Close() // nobody reads Messages: the connection ends, as the bus would end it
				break
			}
		}
	}
	c.mu.Lock()
	c.err = errors.New("bus: connection closed")
	c.queued.Signal()
	c.mu.Unlock()
	close(c.done)
}

// pump hands the queue to Messages in order, and closes Messages after the last delivery once the connection ends.
func (c *Client) pump() {
	defer close(c.messages)
	defer func() {
		c.mu.Lock()
		c.queue, c.bytes = nil, 0 // what nobody took is not kept
		c.mu.Unlock()
	}()
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && c.err == nil {
			c.queued.Wait()
		}
		if len(c.queue) == 0 {
			c.mu.Unlock()
			return
		}
		d := c.queue[0]
		c.queue = c.queue[1:]
		c.bytes -= len(d.Payload)
		c.mu.Unlock()
		select {
		case c.messages <- d:
		case <-c.closing: // closed by its owner: what nobody took is dropped, and durable consumers get it again
			return
		}
	}
}

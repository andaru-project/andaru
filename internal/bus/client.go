package bus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
)

// Client is a connection to the bus from another process. Events for its subscriptions and consumers arrive on
// Messages; a consumer's events are acked with Ack once handled.
type Client struct {
	nc       net.Conn
	writeMu  sync.Mutex
	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan error
	messages chan Delivery
	done     chan struct{}
	err      error
}

// Delivery is an event a client got: Consumer names the consumer it is for, or is empty for a subscription.
type Delivery struct {
	Consumer string
	Message
}

// Dial connects to the bus on a Unix socket as user, with token.
func Dial(ctx context.Context, socket, user, token string) (*Client, error) {
	nc, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := &Client{nc: nc, pending: map[int64]chan error{}, messages: make(chan Delivery, queued), done: make(chan struct{})}
	go c.read()
	if err = c.request(ctx, frame{Op: "hello", User: user, Token: token}); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Messages are the events for the client's subscriptions and consumers. It is closed when the connection ends.
func (c *Client) Messages() <-chan Delivery { return c.messages }

// Publish sends an event; a durable one is in the bus's log when Publish returns.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte, durable bool) error {
	return c.request(ctx, frame{Op: "publish", Topic: topic, Payload: payload, Durable: durable})
}

// Subscribe asks for the events on filter published from now on.
func (c *Client) Subscribe(ctx context.Context, filter string) error {
	return c.request(ctx, frame{Op: "subscribe", Filter: filter})
}

// Consume reads the durable consumer name: the durable events on filter after its cursor.
func (c *Client) Consume(ctx context.Context, name, filter string) error {
	return c.request(ctx, frame{Op: "consume", Consumer: name, Filter: filter})
}

// Ack says the consumer is done with its events up to seq.
func (c *Client) Ack(name string, seq int64) error {
	return c.write(frame{Op: "ack", Consumer: name, Seq: seq})
}

func (c *Client) Close() error { return c.nc.Close() }

func (c *Client) write(f frame) error {
	line, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.nc.Write(append(line, '\n'))
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
	if err := c.write(f); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.err
	}
}

func (c *Client) read() {
	scanner := bufio.NewScanner(c.nc)
	scanner.Buffer(make([]byte, 4096), maxFrame)
	for scanner.Scan() {
		var f frame
		if json.Unmarshal(scanner.Bytes(), &f) != nil {
			break
		}
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
			c.messages <- Delivery{Consumer: f.Consumer, Message: Message{Seq: f.Seq, Topic: f.Topic, Payload: f.Payload}}
		case "gap":
			c.messages <- Delivery{Consumer: f.Consumer, Message: Message{Gap: true}}
		}
	}
	c.mu.Lock()
	c.err = errors.New("bus: connection closed")
	c.mu.Unlock()
	close(c.done)
	close(c.messages)
}

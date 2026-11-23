// Package bus is Andaru's event bus: an MQTT 5 broker embedded in the engine,
// with its sessions and undelivered messages kept in SQLite.
//
// A durable event is published with QoS 1 and reaches every subscriber with a
// persistent session at least once, across restarts. A non-durable event is
// QoS 0 and is lost if a process dies. When the bus cannot keep a durable
// subscriber's messages (a store write fails, or the subscriber falls
// MaximumInflight messages behind), it discards that subscriber's session;
// the subscriber sees Session Present off when it reconnects and knows it may
// have missed events.
package bus

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync/atomic"

	"github.com/google/uuid"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Grants are the topic filters a client may publish to and subscribe to.
type Grants struct {
	Publish   []string
	Subscribe []string
}

type Options struct {
	// Path is the SQLite file holding sessions, undelivered messages and the outbox.
	Path string
	// Grants by MQTT user name. A client whose user name has no grants cannot connect.
	Grants map[string]Grants
	// Authenticate checks a client on Listeners. Clients connecting through Dial
	// are the engine's own and are not asked. Nil refuses every listener client.
	Authenticate func(username, password []byte) bool
	// Listeners by ID, for clients outside the engine.
	Listeners map[string]net.Listener
	// MaximumInflight is how many unacknowledged messages a client may hold
	// before its session is discarded. Zero means 8192.
	MaximumInflight uint16
	// MaximumSessionExpiry caps, in seconds, how long a disconnected session
	// is kept. Zero means seven days.
	MaximumSessionExpiry uint32
	Logger               *slog.Logger
}

// ErrStore wraps a failure to open or load the store at Options.Path: a damaged file, or a disk that fails while
// it is opened.
var ErrStore = errors.New("bus store unusable")

type Bus struct {
	server *mqtt.Server
	store  *store
	engine *pipeListener
	// subscriptions numbers inline subscriptions, which mochi tells apart by filter and number.
	subscriptions atomic.Int64
}

const engineListener = "engine"

// Open starts the broker, loads the stored sessions and publishes whatever an
// earlier run left in the outbox.
func Open(o Options) (*Bus, error) {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.MaximumSessionExpiry == 0 {
		o.MaximumSessionExpiry = 7 * 24 * 3600
	}
	capabilities := mqtt.NewDefaultServerCapabilities()
	capabilities.MaximumSessionExpiryInterval = o.MaximumSessionExpiry
	// An event is news of a change, read from state when missed; a retained one would outlive what it reports.
	capabilities.RetainAvailable = 0
	if o.MaximumInflight != 0 {
		capabilities.MaximumInflight = o.MaximumInflight
	}
	server := mqtt.New(&mqtt.Options{InlineClient: true, Logger: o.Logger, Capabilities: capabilities})

	st := &store{server: server, maximumInflight: int(capabilities.MaximumInflight)}
	if err := server.AddHook(&access{grants: o.Grants, authenticate: o.Authenticate}, nil); err != nil {
		return nil, err
	}
	if err := server.AddHook(st, o.Path); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	engine := newPipeListener()
	b := &Bus{server: server, store: st, engine: engine}
	if err := server.AddListener(listeners.NewNet(engineListener, engine)); err != nil {
		server.Close()
		return nil, err
	}
	for id, l := range o.Listeners {
		if id == engineListener {
			server.Close()
			return nil, errors.New("bus: listener ID engine is reserved")
		}
		if err := server.AddListener(listeners.NewNet(id, l)); err != nil {
			server.Close()
			return nil, err
		}
	}
	// With no listeners or hooks from configuration, Serve fails only on loading the store.
	if err := server.Serve(); err != nil {
		server.Close()
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if err := st.drain(); err != nil {
		server.Close()
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return b, nil
}

func (b *Bus) Close() error { return b.server.Close() }

// Dial connects one of the engine's own MQTT clients, in memory. Its user
// name still needs grants.
func (b *Bus) Dial() (net.Conn, error) { return b.engine.dial() }

// Publish sends an event from the engine. A durable event goes through the
// outbox, so a crash before it reaches every subscriber's session publishes it
// again on the next start.
func (b *Bus) Publish(topic string, payload []byte, durable bool) error {
	if !durable {
		return b.server.Publish(topic, payload, false, 0)
	}
	key := "engine:" + uuid.NewString()
	if err := b.store.outboxPut(key, topic, payload); err != nil {
		return err
	}
	if err := b.server.Publish(topic, payload, false, 1); err != nil {
		return err
	}
	return b.store.outboxDelete(key)
}

// Subscribe calls handler for every event on filter, in the engine and without
// acknowledgement: non-durable. A durable subscriber in the engine uses Dial.
// handler runs in the publisher's goroutine and must not block. The returned
// function ends the subscription.
func (b *Bus) Subscribe(filter string, handler func(topic string, payload []byte)) (func(), error) {
	id := int(b.subscriptions.Add(1))
	err := b.server.Subscribe(filter, id, func(_ *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
		handler(pk.TopicName, pk.Payload)
	})
	if err != nil {
		return nil, err
	}
	return func() { _ = b.server.Unsubscribe(filter, id) }, nil
}

package bus

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
	"github.com/mochi-mqtt/server/v2/packets"
)

var testGrants = map[string]Grants{
	"web":    {Subscribe: []string{"config/#"}},
	"addon":  {Publish: []string{"alias/+"}, Subscribe: []string{"alias/#"}},
	"agent":  {Publish: []string{"alias/+"}},
	"notify": {Subscribe: []string{"notification/#"}},
}

type harness struct {
	t    *testing.T
	path string
	bus  *Bus
	addr string
}

func newHarness(t *testing.T, o Options) *harness {
	h := &harness{t: t, path: filepath.Join(t.TempDir(), "bus.db")}
	h.start(o)
	return h
}

func (h *harness) start(o Options) {
	h.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	o.Path = h.path
	if o.Grants == nil {
		o.Grants = testGrants
	}
	o.Authenticate = func(_, password []byte) bool { return string(password) == "secret" }
	o.Listeners = map[string]net.Listener{"tcp": l}
	b, err := Open(o)
	if err != nil {
		h.t.Fatal(err)
	}
	h.bus, h.addr = b, l.Addr().String()
}

func (h *harness) restart(o Options) {
	h.t.Helper()
	h.bus.Close()
	h.start(o)
}

type client struct {
	*paho.Client
	received chan *paho.Publish
	connack  *paho.Connack
}

// connect opens a persistent session for user; with ack false it never
// acknowledges what it receives. conn nil dials the TCP listener.
func (h *harness) connect(user, password string, conn net.Conn, ack bool) (*client, error) {
	h.t.Helper()
	if conn == nil {
		var err error
		if conn, err = net.Dial("tcp", h.addr); err != nil {
			h.t.Fatal(err)
		}
	}
	c := &client{received: make(chan *paho.Publish, 64)}
	c.Client = paho.NewClient(paho.ClientConfig{
		Conn: conn,
		// Without manual acknowledgement paho acknowledges once the handler returns.
		EnableManualAcknowledgment: !ack,
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(p paho.PublishReceived) (bool, error) {
			c.received <- p.Packet
			return true, nil
		}},
	})
	expiry := uint32(3600)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connack, err := c.Connect(ctx, &paho.Connect{ClientID: user, KeepAlive: 30, CleanStart: false,
		Username: user, UsernameFlag: true, Password: []byte(password), PasswordFlag: password != "",
		Properties: &paho.ConnectProperties{SessionExpiryInterval: &expiry}})
	c.connack = connack
	if err != nil {
		conn.Close()
		return c, err
	}
	h.t.Cleanup(func() { c.Disconnect(&paho.Disconnect{}) })
	return c, nil
}

func (h *harness) mustConnect(user string, conn net.Conn, ack bool) *client {
	h.t.Helper()
	c, err := h.connect(user, "secret", conn, ack)
	if err != nil {
		h.t.Fatalf("connect %s: %v", user, err)
	}
	return c
}

func (c *client) subscribe(t *testing.T, filter string) byte {
	t.Helper()
	suback, err := c.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: filter, QoS: 1}}})
	if suback == nil {
		t.Fatalf("subscribe %s: %v", filter, err)
	}
	return suback.Reasons[0]
}

func (c *client) leave(t *testing.T) {
	t.Helper()
	c.Disconnect(&paho.Disconnect{})
	time.Sleep(100 * time.Millisecond)
}

func (c *client) receive(t *testing.T) *paho.Publish {
	t.Helper()
	select {
	case p := <-c.received:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
		return nil
	}
}

func (c *client) nothing(t *testing.T) {
	t.Helper()
	select {
	case p := <-c.received:
		t.Fatalf("unexpected message %s %q", p.Topic, p.Payload)
	case <-time.After(200 * time.Millisecond):
	}
}

func (h *harness) rows(table, where string) int {
	h.t.Helper()
	db, err := sql.Open("sqlite", "file:"+h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE ` + where).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		grant, filter string
		want          bool
	}{
		{"config/#", "config/head", true},
		{"config/#", "config/a/b", true},
		{"config/#", "config/#", true},
		{"config/#", "config", true},
		{"config/#", "alias/x", false},
		{"alias/+", "alias/x", true},
		{"alias/+", "alias/+", true},
		{"alias/+", "alias/#", false},
		{"alias/+", "alias/x/y", false},
		{"alias/+", "alias", false},
		{"alias/x", "alias/+", false},
		{"alias/x", "alias/x", true},
		{"#", "anything/at/all", true},
	} {
		if got := covers(c.grant, c.filter); got != c.want {
			t.Errorf("covers(%q, %q) = %v, want %v", c.grant, c.filter, got, c.want)
		}
	}
}

// A durable event for a subscriber that is offline is stored, survives a
// restart, and is delivered when the subscriber comes back.
func TestDurableEventReachesOfflineSubscriberAfterRestart(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	web.leave(t)

	if err := h.bus.Publish("config/head", []byte("v42"), true); err != nil {
		t.Fatal(err)
	}
	if n := h.rows("mqtt_state", "kind = 'IFM'"); n != 1 {
		t.Fatalf("stored messages = %d, want 1", n)
	}
	if n := h.rows("outbox", "1"); n != 0 {
		t.Fatalf("outbox = %d after publish, want 0", n)
	}
	h.restart(Options{})

	web = h.mustConnect("web", nil, true)
	if !web.connack.SessionPresent {
		t.Fatal("session lost across restart")
	}
	if p := web.receive(t); p.Topic != "config/head" || string(p.Payload) != "v42" {
		t.Fatalf("got %s %q", p.Topic, p.Payload)
	}
	time.Sleep(100 * time.Millisecond)
	if n := h.rows("mqtt_state", "kind = 'IFM'"); n != 0 {
		t.Fatalf("stored messages after ack = %d, want 0", n)
	}
}

// A message delivered but never acknowledged is delivered again after a restart.
func TestUnacknowledgedEventIsRedeliveredAfterRestart(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, false)
	web.subscribe(t, "config/#")
	if err := h.bus.Publish("config/head", []byte("v43"), true); err != nil {
		t.Fatal(err)
	}
	web.receive(t)
	web.leave(t)
	h.restart(Options{})

	web = h.mustConnect("web", nil, true)
	if p := web.receive(t); string(p.Payload) != "v43" {
		t.Fatalf("got %q", p.Payload)
	}
}

// A non-durable event is not kept for a subscriber that is away.
func TestNonDurableEventIsNotKept(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	web.leave(t)
	if err := h.bus.Publish("config/head", []byte("gone"), false); err != nil {
		t.Fatal(err)
	}
	web = h.mustConnect("web", nil, true)
	web.nothing(t)
}

// An engine publish left in the outbox by a crash is published on the next start.
func TestOutboxIsPublishedOnStart(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	web.leave(t)
	if err := h.bus.store.outboxPut("engine:crashed", "config/head", []byte("v44")); err != nil {
		t.Fatal(err)
	}
	h.restart(Options{})

	web = h.mustConnect("web", nil, true)
	if p := web.receive(t); string(p.Payload) != "v44" {
		t.Fatalf("got %q", p.Payload)
	}
	if n := h.rows("outbox", "1"); n != 0 {
		t.Fatalf("outbox = %d after start, want 0", n)
	}
}

// A durable publish from outside the engine is kept in the outbox before the
// broker acknowledges it, and leaves it once every session holds it.
func TestClientPublishGoesThroughOutbox(t *testing.T) {
	h := newHarness(t, Options{})
	addon := h.mustConnect("addon", nil, true)
	addon.subscribe(t, "alias/#")
	agent := h.mustConnect("agent", nil, true)
	if _, err := agent.Publish(context.Background(), &paho.Publish{Topic: "alias/lan", QoS: 1, Payload: []byte("changed")}); err != nil {
		t.Fatal(err)
	}
	if p := addon.receive(t); p.Topic != "alias/lan" {
		t.Fatalf("got %s", p.Topic)
	}
	if n := h.rows("outbox", "1"); n != 0 {
		t.Fatalf("outbox = %d after publish, want 0", n)
	}

	// The engine dies after the hook kept a publish and before the broker
	// delivered it: the next start publishes it.
	cl, ok := h.bus.server.Clients.Get("agent")
	if !ok {
		t.Fatal("agent not connected")
	}
	pk := packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1},
		TopicName: "alias/wan", Payload: []byte("pending"), Origin: "agent", PacketID: 7, Created: time.Now().Unix()}
	if _, err := h.bus.store.OnPublish(cl, pk); err != nil {
		t.Fatal(err)
	}
	addon.leave(t)
	h.restart(Options{})
	addon = h.mustConnect("addon", nil, true)
	if p := addon.receive(t); p.Topic != "alias/wan" || string(p.Payload) != "pending" {
		t.Fatalf("got %s %q", p.Topic, p.Payload)
	}
}

// Grants are checked when connecting, publishing and subscribing.
func TestGrants(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.connect("web", "wrong", nil, true); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := h.connect("stranger", "secret", nil, true); err == nil {
		t.Fatal("user without grants accepted")
	}
	web := h.mustConnect("web", nil, true)
	if code := web.subscribe(t, "notification/#"); code < 0x80 {
		t.Fatalf("web subscribed to notification/#: reason %#x", code)
	}
	addon := h.mustConnect("addon", nil, true)
	addon.subscribe(t, "alias/#")
	if code := addon.subscribe(t, "#"); code < 0x80 {
		t.Fatalf("addon subscribed to #: reason %#x", code)
	}
	resp, err := addon.Publish(context.Background(), &paho.Publish{Topic: "config/head", QoS: 1, Payload: []byte("x")})
	if err == nil && (resp == nil || resp.ReasonCode < 0x80) {
		t.Fatal("addon published to config/head")
	}
	addon.nothing(t)
}

// The engine's own clients connect in memory without a password, and still need grants.
func TestEngineClientDurableAcrossRestart(t *testing.T) {
	h := newHarness(t, Options{})
	conn, err := h.bus.Dial()
	if err != nil {
		t.Fatal(err)
	}
	notify, err := h.connect("notify", "", conn, true)
	if err != nil {
		t.Fatal(err)
	}
	notify.subscribe(t, "notification/#")
	notify.leave(t)
	if err := h.bus.Publish("notification/engine", []byte("disk filling"), true); err != nil {
		t.Fatal(err)
	}
	h.restart(Options{})
	if conn, err = h.bus.Dial(); err != nil {
		t.Fatal(err)
	}
	notify, err = h.connect("notify", "", conn, true)
	if err != nil {
		t.Fatal(err)
	}
	if p := notify.receive(t); string(p.Payload) != "disk filling" {
		t.Fatalf("got %q", p.Payload)
	}

	conn, _ = h.bus.Dial()
	if _, err := h.connect("stranger", "", conn, true); err == nil {
		t.Fatal("engine client without grants accepted")
	}
}

// A subscriber that falls MaximumInflight messages behind loses its session
// instead of losing messages silently.
func TestSessionDiscardedAtInflightLimit(t *testing.T) {
	h := newHarness(t, Options{MaximumInflight: 2})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	web.leave(t)
	for i := 0; i < 3; i++ {
		if err := h.bus.Publish("config/head", []byte{byte('0' + i)}, true); err != nil {
			t.Fatal(err)
		}
	}
	web = h.mustConnect("web", nil, true)
	if web.connack.SessionPresent {
		t.Fatal("session kept past the in-flight limit")
	}
	if n := h.rows("mqtt_state", "kind IN ('IFM', 'SUB')"); n != 0 {
		t.Fatalf("stored rows of the discarded session = %d, want 0", n)
	}
}

// A store write that fails discards the session of the client it was for.
func TestSessionDiscardedWhenStoreWriteFails(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	web.leave(t)

	if _, err := h.bus.store.db.Exec(`DROP TABLE mqtt_state`); err != nil {
		t.Fatal(err)
	}
	if err := h.bus.Publish("config/head", []byte("lost"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.bus.store.db.Exec(`CREATE TABLE mqtt_state (
		kind TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL, PRIMARY KEY (kind, key)) WITHOUT ROWID`); err != nil {
		t.Fatal(err)
	}
	web = h.mustConnect("web", nil, true)
	if web.connack.SessionPresent {
		t.Fatal("session kept after a failed write")
	}
	web.nothing(t)
}

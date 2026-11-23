package bus

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
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

// restart starts the bus again on the same file, with default options.
func (h *harness) restart() {
	h.t.Helper()
	h.bus.Close()
	h.start(Options{})
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

// eventually waits up to five seconds for done.
func (h *harness) eventually(what string, done func() bool) {
	h.t.Helper()
	for start := time.Now(); !done(); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			h.t.Fatalf("waited for %s", what)
		}
	}
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
	h.restart()

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
	h.restart()

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
	h.restart()

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
	// mochi acknowledges before it delivers, and the row goes once every session holds the event.
	h.eventually("the outbox to empty", func() bool { return h.rows("outbox", "1") == 0 })

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
	h.restart()
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
	h.restart()
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

// A discard finishes before the bus closes, so a restart finds nothing of the session.
func TestDiscardedSessionStaysGoneAfterRestart(t *testing.T) {
	h := newHarness(t, Options{MaximumInflight: 2})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	web.leave(t)
	for i := 0; i < 3; i++ {
		if err := h.bus.Publish("config/head", []byte{byte('0' + i)}, true); err != nil {
			t.Fatal(err)
		}
	}
	h.restart()
	if web = h.mustConnect("web", nil, true); web.connack.SessionPresent {
		t.Fatal("a discarded session came back after a restart")
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

// An inline subscription gets what is published while it lasts, and nothing of it is stored.
func TestInlineSubscription(t *testing.T) {
	h := newHarness(t, Options{})
	got := make(chan string, 4)
	stop, err := h.bus.Subscribe("config/#", func(topic string, payload []byte) { got <- topic + " " + string(payload) })
	if err != nil {
		t.Fatal(err)
	}
	if err := h.bus.Publish("config/head", []byte("7"), false); err != nil {
		t.Fatal(err)
	}
	if g := <-got; g != "config/head 7" {
		t.Fatalf("got %q", g)
	}
	if n := h.rows("mqtt_state", "1"); n != 0 {
		t.Fatalf("%d rows stored for an inline subscription", n)
	}
	stop()
	if err := h.bus.Publish("config/head", []byte("8"), false); err != nil {
		t.Fatal(err)
	}
	select {
	case g := <-got:
		t.Fatalf("got %q after the subscription ended", g)
	case <-time.After(100 * time.Millisecond):
	}
}

// Only MQTT 5 clients connect: an older one could not be told that its durable publish was refused.
func TestOnlyMQTT5Connects(t *testing.T) {
	a := &access{grants: testGrants, authenticate: func(_, _ []byte) bool { return true }}
	for version, want := range map[byte]bool{3: false, 4: false, 5: true} {
		cl := &mqtt.Client{ID: "web", Net: mqtt.ClientConnection{Listener: "tcp"}}
		cl.Properties.Username, cl.Properties.ProtocolVersion = []byte("web"), version
		if got := a.OnConnectAuthenticate(cl, packets.Packet{}); got != want {
			t.Errorf("MQTT %d: connected %v", version, got)
		}
	}
}

// A client without a session to keep leaves nothing in the store once it is gone, whatever its ID is made of.
func TestCleanSessionLeavesNothing(t *testing.T) {
	h := newHarness(t, Options{})
	conn, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	// It never acknowledges, so the bus holds what it is sent.
	c := paho.NewClient(paho.ClientConfig{Conn: conn, EnableManualAcknowledgment: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = c.Connect(ctx, &paho.Connect{ClientID: "web/é", CleanStart: true, Username: "web", UsernameFlag: true, Password: []byte("secret"), PasswordFlag: true}); err != nil {
		t.Fatal(err)
	}
	// mochi answers the connect before it stores the client.
	h.eventually("the client to be stored", func() bool { return h.rows("mqtt_state", "key='web/é'") == 1 })
	if _, err = c.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "config/#", QoS: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err = h.bus.Publish("config/head", []byte("1"), true); err != nil {
		t.Fatal(err)
	}
	h.eventually("the subscription and the held message stored", func() bool { return h.rows("mqtt_state", "key LIKE 'web/é:%'") == 2 })
	c.Disconnect(&paho.Disconnect{})
	h.eventually("nothing left of the client", func() bool { return h.rows("mqtt_state", "key LIKE 'web/é%'") == 0 })
}

// An unsubscribed filter is forgotten, so a restart does not bring it back.
func TestUnsubscribeIsForgotten(t *testing.T) {
	h := newHarness(t, Options{})
	addon := h.mustConnect("addon", nil, true)
	addon.subscribe(t, "alias/#")
	if n := h.rows("mqtt_state", "key='addon:alias/#'"); n != 1 {
		t.Fatalf("%d subscription rows", n)
	}
	if _, err := addon.Unsubscribe(context.Background(), &paho.Unsubscribe{Topics: []string{"alias/#"}}); err != nil {
		t.Fatal(err)
	}
	if n := h.rows("mqtt_state", "key='addon:alias/#'"); n != 0 {
		t.Fatalf("%d subscription rows after unsubscribing", n)
	}
}

// The engine's durable publish is in the outbox while it is delivered, so a crash then publishes it again on start;
// a non-durable one never is.
func TestEnginePublishGoesThroughOutbox(t *testing.T) {
	h := newHarness(t, Options{})
	held := make(chan int, 2)
	if _, err := h.bus.Subscribe("config/#", func(string, []byte) { held <- h.rows("outbox", "1") }); err != nil {
		t.Fatal(err)
	}
	for _, durable := range []bool{true, false} {
		if err := h.bus.Publish("config/head", []byte("1"), durable); err != nil {
			t.Fatal(err)
		}
		if n, want := <-held, map[bool]int{true: 1, false: 0}[durable]; n != want {
			t.Fatalf("durable %v: %d outbox rows during delivery", durable, n)
		}
	}
	if n := h.rows("outbox", "1"); n != 0 {
		t.Fatalf("%d outbox rows after delivery", n)
	}
}

// Nothing is retained: an event reports a change, and one missed is read from state, not kept for later.
func TestNothingIsRetained(t *testing.T) {
	h := newHarness(t, Options{})
	agent := h.mustConnect("agent", nil, true)
	_, _ = agent.Publish(context.Background(), &paho.Publish{Topic: "alias/lan", Payload: []byte("1"), Retain: true})
	time.Sleep(100 * time.Millisecond)
	addon := h.mustConnect("addon", nil, true)
	addon.subscribe(t, "alias/#")
	addon.nothing(t)
}

// A store whose rows cannot be loaded fails Open with ErrStore, so the engine can set it aside.
func TestUnloadableStoreIsErrStore(t *testing.T) {
	h := newHarness(t, Options{})
	h.bus.Close()
	db, err := sql.Open("sqlite", "file:"+h.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO mqtt_state(kind, key, value, writer) VALUES (?, 'broken', x'00ff', '')`, storage.ClientKey); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = Open(Options{Path: h.path, Grants: testGrants}); !errors.Is(err, ErrStore) {
		t.Fatalf("Open: %v", err)
	}
}

// A client may hold only its own user's sessions, and may not ask the broker to publish for it.
func TestConnectRules(t *testing.T) {
	h := newHarness(t, Options{})
	try := func(c *paho.Connect) error {
		t.Helper()
		conn, err := net.Dial("tcp", h.addr)
		if err != nil {
			t.Fatal(err)
		}
		client := paho.NewClient(paho.ClientConfig{Conn: conn})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.Username, c.UsernameFlag, c.Password, c.PasswordFlag = "agent", true, []byte("secret"), true
		_, err = client.Connect(ctx, c)
		if err == nil {
			client.Disconnect(&paho.Disconnect{})
		}
		return err
	}
	for _, id := range []string{"agent", "agent/7"} {
		if err := try(&paho.Connect{ClientID: id, CleanStart: true}); err != nil {
			t.Errorf("%s refused: %v", id, err)
		}
	}
	for _, id := range []string{"notify", "agents", "agent:7", "agent/7:x"} {
		if err := try(&paho.Connect{ClientID: id, CleanStart: true}); err == nil {
			t.Errorf("%s accepted", id)
		}
	}
	will := &paho.Connect{ClientID: "agent", CleanStart: true, WillMessage: &paho.WillMessage{Topic: "alias/lan", Payload: []byte("x")}}
	if err := try(will); err == nil {
		t.Error("a Will accepted")
	}
}

// What the bus holds for a client keeps its topic: the client gets no outbound aliases, and a held message is kept
// as long as its session, not a day.
func TestHeldMessagesKeepTopicAndAge(t *testing.T) {
	h := newHarness(t, Options{})
	conn, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	c := paho.NewClient(paho.ClientConfig{Conn: conn, EnableManualAcknowledgment: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	aliases := uint16(10)
	if _, err = c.Connect(ctx, &paho.Connect{ClientID: "web", Username: "web", UsernameFlag: true, Password: []byte("secret"), PasswordFlag: true,
		Properties: &paho.ConnectProperties{TopicAliasMaximum: &aliases}}); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect(&paho.Disconnect{})
	if _, err = c.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "config/#", QoS: 1}}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = h.bus.Publish("config/head", []byte("1"), true); err != nil {
			t.Fatal(err)
		}
	}
	cl, _ := h.bus.server.Clients.Get("web")
	h.eventually("both held", func() bool { return cl.State.Inflight.Len() == 2 })
	for _, pk := range cl.State.Inflight.GetAll(false) {
		if pk.TopicName != "config/head" {
			t.Fatalf("held message topic %q", pk.TopicName)
		}
	}
	twoDays := time.Now().Unix() + 2*24*3600
	if dropped := cl.ClearExpiredInflights(twoDays, h.bus.server.Options.Capabilities.MaximumMessageExpiryInterval); len(dropped) != 0 {
		t.Fatalf("dropped %v after two days", dropped)
	}
}

// forget removes one client's rows, by bytes: a multibyte ID loses all of its own and none of its neighbours'.
func TestForgetRemovesOneClient(t *testing.T) {
	h := newHarness(t, Options{})
	keys := map[string]string{
		"web/é:config/#": storage.SubscriptionKey, "web/é:1": storage.InflightKey,
		"web/éx:1": storage.InflightKey, "web:1": storage.InflightKey, "web/:1": storage.InflightKey,
	}
	for key, kind := range keys {
		if _, err := h.bus.store.db.Exec(`INSERT INTO mqtt_state(kind, key, value, writer) VALUES (?, ?, x'00', '')`, kind, key); err != nil {
			t.Fatal(err)
		}
	}
	h.bus.store.forget("web/é")
	if n := h.rows("mqtt_state", "key LIKE 'web/é:%'"); n != 0 {
		t.Fatalf("%d of its rows left", n)
	}
	if n := h.rows("mqtt_state", "key IN ('web/éx:1', 'web:1', 'web/:1')"); n != 3 {
		t.Fatalf("%d of 3 neighbouring rows left", n)
	}
}

// User names cannot overlap as client IDs: none may hold the slash or the colon an ID is built with.
func TestUserNamesCannotOverlap(t *testing.T) {
	for _, user := range []string{"", "agent/team", "agent:team"} {
		if _, err := Open(Options{Path: filepath.Join(t.TempDir(), "bus.db"), Grants: map[string]Grants{"agent": {}, user: {}}}); err == nil {
			t.Errorf("user name %q accepted", user)
		}
	}
}

// A client's publish that reuses the packet ID of a delivery it has not acknowledged is taken, and does not cost
// the delivery: its stored row survives the publish's acknowledgement.
func TestPublishCannotTakeAPendingDeliverysPacketID(t *testing.T) {
	h := newHarness(t, Options{})
	conn, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	addon := h.mustConnect("addon", conn, false)
	addon.subscribe(t, "alias/#")
	if err = h.bus.Publish("alias/wan", []byte("held"), true); err != nil {
		t.Fatal(err)
	}
	held := addon.receive(t)
	// paho numbers its own publishes, so the colliding one is written by hand on the client's connection.
	var raw bytes.Buffer
	publish := packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, TopicName: "alias/lan",
		PacketID: held.PacketID, Payload: []byte("mine"), ProtocolVersion: 5}
	if err = publish.PublishEncode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	// The publish is taken: the client's own subscription brings it back.
	if p := addon.receive(t); string(p.Payload) != "mine" {
		t.Fatalf("got %s %q", p.Topic, p.Payload)
	}
	// The delivery outlives a restart, so its stored row outlived the publish's acknowledgement.
	h.restart()
	addon = h.mustConnect("addon", nil, true)
	for string(addon.receive(t).Payload) != "held" {
	}
}

// The bus offers at most QoS 1: at QoS 2 mochi could overwrite a delivery with a client's publish of its ID.
func TestQoSIsAtMostOne(t *testing.T) {
	h := newHarness(t, Options{})
	addon := h.mustConnect("addon", nil, true)
	suback, err := addon.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "alias/#", QoS: 2}}})
	if err != nil || suback.Reasons[0] != 1 {
		t.Fatalf("QoS 2 subscription: %v, %v", suback, err)
	}
}

// A discard deletes the client's row before the drop runs, so a crash in between finds no session.
func TestDiscardDeletesTheClientRowAtOnce(t *testing.T) {
	h := newHarness(t, Options{MaximumInflight: 2})
	held := make(chan struct{})
	drop := h.bus.store.drop
	h.bus.store.drop = func(cl *mqtt.Client, why packets.Code) *mqtt.Client { <-held; return drop(cl, why) }
	defer close(held)
	web := h.mustConnect("web", nil, false)
	web.subscribe(t, "config/#")
	for i := 0; i < 3; i++ {
		if err := h.bus.Publish("config/head", []byte{byte('0' + i)}, true); err != nil {
			t.Fatal(err)
		}
	}
	h.eventually("the client row deleted", func() bool { return h.rows("mqtt_state", "kind = 'CL' AND key = 'web'") == 0 })
}

// Discarding a client whose session was taken over drops the session where it is now, rows and all.
func TestDiscardFollowsTheSession(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	old, _ := h.bus.server.Clients.Get("web")
	h.mustConnect("web", nil, true) // takes the session over
	h.bus.store.discard(old, packets.ErrImplementationSpecificError)
	h.eventually("the session forgotten", func() bool { return h.rows("mqtt_state", "key LIKE 'web%'") == 0 })
	h.restart()
	if web = h.mustConnect("web", nil, true); web.connack.SessionPresent {
		t.Fatal("the dropped session came back after a restart")
	}
}

// A dropped connection's own late UNSUBSCRIBE leaves a successor's rows alone; only the drop forgets.
func TestDroppedClientsUnsubscribeForgetsNothing(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	old, _ := h.bus.server.Clients.Get("web")
	if h.bus.server.DropSession(old, packets.ErrImplementationSpecificError) != old {
		t.Fatal("not dropped")
	}
	web = h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	h.bus.store.OnUnsubscribed(old, packets.Packet{PacketID: 7, Filters: packets.Subscriptions{{Filter: "config/#"}}})
	if n := h.rows("mqtt_state", "key = 'web' OR key = 'web:config/#'"); n != 2 {
		t.Fatalf("%d of the successor's 2 rows left", n)
	}
}

// Rows left without their client by a crash do not come back as a session's subscriptions.
func TestOrphanRowsAreRemovedOnOpen(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.bus.store.db.Exec(`INSERT INTO mqtt_state(kind, key, value, writer) VALUES (?, 'ghost:config/#', x'00', ''), (?, 'ghost:1', x'00', '')`,
		storage.SubscriptionKey, storage.InflightKey); err != nil {
		t.Fatal(err)
	}
	h.restart()
	if n := h.rows("mqtt_state", "key LIKE 'ghost%'"); n != 0 {
		t.Fatalf("%d orphan rows left", n)
	}
}

// A takeover clears the old client's inflight messages after its successor inherited them under the same keys;
// their rows stay.
func TestTakeoverKeepsTheDeliveriesRows(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, false)
	web.subscribe(t, "config/#")
	if err := h.bus.Publish("config/head", []byte("held"), true); err != nil {
		t.Fatal(err)
	}
	held := web.receive(t)
	old, _ := h.bus.server.Clients.Get("web")
	successor := h.mustConnect("web", nil, false) // takes the session over
	successor.receive(t)                          // its re-sent delivery: the resend has rewritten the row
	h.bus.store.OnQosDropped(old, packets.Packet{PacketID: held.PacketID})
	if n := h.rows("mqtt_state", "kind = 'IFM'"); n != 1 {
		t.Fatalf("%d delivery rows, want 1", n)
	}
}

// A connection's late hooks delete only the rows it wrote: not a clean-start successor's client row, nor a
// delivery row its successor stored under the same key.
func TestLateHooksDeleteOnlyTheirOwnRows(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	old, _ := h.bus.server.Clients.Get("web")

	// A clean start replaces the session; discarding the old client leaves the new one's row.
	conn, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	successor := &client{received: make(chan *paho.Publish, 8)}
	successor.Client = paho.NewClient(paho.ClientConfig{Conn: conn, EnableManualAcknowledgment: true,
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(p paho.PublishReceived) (bool, error) {
			successor.received <- p.Packet
			return true, nil
		}}})
	expiry := uint32(3600)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = successor.Connect(ctx, &paho.Connect{ClientID: "web", CleanStart: true, Username: "web", UsernameFlag: true,
		Password: []byte("secret"), PasswordFlag: true, Properties: &paho.ConnectProperties{SessionExpiryInterval: &expiry}}); err != nil {
		t.Fatal(err)
	}
	defer successor.Disconnect(&paho.Disconnect{})
	h.eventually("the successor's row", func() bool { return h.rows("mqtt_state", "kind = 'CL' AND key = 'web'") == 1 })
	h.bus.store.discard(old, packets.ErrImplementationSpecificError)
	h.bus.store.drops.Wait()
	if n := h.rows("mqtt_state", "kind = 'CL' AND key = 'web'"); n != 1 {
		t.Fatal("discarding the replaced client deleted its successor's row")
	}

	// A stale connection completing a packet ID deletes no delivery row its successor stored with that ID.
	successor.subscribe(t, "config/#")
	if err = h.bus.Publish("config/head", []byte("held"), true); err != nil {
		t.Fatal(err)
	}
	held := successor.receive(t)
	stale := &mqtt.Client{ID: "web", State: mqtt.ClientState{Inflight: mqtt.NewInflights()}}
	h.bus.store.OnQosComplete(stale, packets.Packet{PacketID: held.PacketID})
	if n := h.rows("mqtt_state", "kind = 'IFM'"); n != 1 {
		t.Fatal("a stale connection's completion deleted its successor's delivery row")
	}
}

// A store from before rows recorded their writer gets the column, and its sessions keep working.
func TestStoreWithoutWriterColumnIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE mqtt_state (kind TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL, PRIMARY KEY (kind, key)) WITHOUT ROWID`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	h := &harness{t: t, path: path}
	h.start(Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	h.eventually("the session stored", func() bool { return h.rows("mqtt_state", "key LIKE 'web%' AND writer != ''") == 2 })
}

// An offline session's discard deletes its client row before the drop too: the client's writer name outlives
// its connection.
func TestOfflineDiscardDeletesTheClientRowAtOnce(t *testing.T) {
	h := newHarness(t, Options{MaximumInflight: 2})
	held := make(chan struct{})
	drop := h.bus.store.drop
	h.bus.store.drop = func(cl *mqtt.Client, why packets.Code) *mqtt.Client { <-held; return drop(cl, why) }
	defer close(held)
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	web.leave(t)
	for i := 0; i < 3; i++ {
		if err := h.bus.Publish("config/head", []byte{byte('0' + i)}, true); err != nil {
			t.Fatal(err)
		}
	}
	h.eventually("the client row deleted", func() bool { return h.rows("mqtt_state", "kind = 'CL' AND key = 'web'") == 0 })
}

// A session loaded after a restart is its client's: discarding it offline deletes its client row before the drop.
func TestLoadedSessionDiscardDeletesTheClientRowAtOnce(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.mustConnect("web", nil, true)
	web.subscribe(t, "config/#")
	web.leave(t)
	h.bus.Close()
	h.start(Options{MaximumInflight: 2})
	held := make(chan struct{})
	drop := h.bus.store.drop
	h.bus.store.drop = func(cl *mqtt.Client, why packets.Code) *mqtt.Client { <-held; return drop(cl, why) }
	defer close(held)
	for i := 0; i < 3; i++ {
		if err := h.bus.Publish("config/head", []byte{byte('0' + i)}, true); err != nil {
			t.Fatal(err)
		}
	}
	h.eventually("the client row deleted", func() bool { return h.rows("mqtt_state", "kind = 'CL' AND key = 'web'") == 0 })
}

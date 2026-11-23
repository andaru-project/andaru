package bus

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var testGrants = map[string]Grants{
	"web":   {Subscribe: []string{"config/#"}},
	"addon": {Publish: []string{"alias/+"}, Subscribe: []string{"alias/#"}},
}

type harness struct {
	t      *testing.T
	dir    string
	bus    *Bus
	socket string
	opt    Options
}

func newHarness(t *testing.T, o Options) *harness {
	dir, err := os.MkdirTemp("", "bus") // a Unix socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &harness{t: t, dir: dir, opt: o}
	h.start()
	t.Cleanup(func() { h.bus.Close() })
	return h
}

func (h *harness) start() {
	h.t.Helper()
	h.socket = filepath.Join(h.dir, "bus.sock")
	_ = os.Remove(h.socket)
	l, err := net.Listen("unix", h.socket)
	if err != nil {
		h.t.Fatal(err)
	}
	o := h.opt
	o.Path = filepath.Join(h.dir, "bus.db")
	if o.Grants == nil {
		o.Grants = testGrants
	}
	o.Authenticate = func(_, token string) bool { return token == "secret" }
	o.Listeners = map[string]net.Listener{"test": l}
	if h.bus, err = Open(o); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) restart() {
	h.t.Helper()
	if err := h.bus.Close(); err != nil {
		h.t.Fatal(err)
	}
	h.start()
}

func (h *harness) dial(user string) *Client {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, h.socket, user, "secret")
	if err != nil {
		h.t.Fatalf("dial %s: %v", user, err)
	}
	h.t.Cleanup(func() { c.Close() })
	return c
}

func (h *harness) publish(topic, payload string, durable bool) {
	h.t.Helper()
	if err := h.bus.Publish(topic, []byte(payload), durable); err != nil {
		h.t.Fatal(err)
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("closed")
		}
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("nothing received")
	}
	var zero T
	return zero
}

func nothing[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %+v", v)
	case <-time.After(200 * time.Millisecond):
	}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestTopics(t *testing.T) {
	for _, c := range []struct {
		filter, topic string
		want          bool
	}{
		{"config/#", "config/head", true}, {"config/#", "config", true}, {"config/+", "config/head", true},
		{"config/+", "config/head/x", false}, {"alias/+", "config/head", false}, {"#", "a/b", true},
	} {
		if got := match(c.filter, c.topic); got != c.want {
			t.Errorf("match(%q, %q) = %v", c.filter, c.topic, got)
		}
	}
	for _, c := range []struct {
		grant, filter string
		want          bool
	}{
		{"alias/#", "alias/+", true}, {"alias/+", "alias/#", false}, {"alias/+", "alias/lan", true}, {"#", "anything/#", true},
	} {
		if got := covers(c.grant, c.filter); got != c.want {
			t.Errorf("covers(%q, %q) = %v", c.grant, c.filter, got)
		}
	}
	for topic, want := range map[string]bool{"config/head": true, "config/+": false, "config//head": false, "": false} {
		if validTopic(topic) != want {
			t.Errorf("validTopic(%q) != %v", topic, want)
		}
	}
	for filter, want := range map[string]bool{"config/#": true, "#/x": false, "a+/b": false, "+/b": true} {
		if validFilter(filter) != want {
			t.Errorf("validFilter(%q) != %v", filter, want)
		}
	}
}

func TestPublishRefusesWhatIsNotAnEvent(t *testing.T) {
	h := newHarness(t, Options{})
	if h.bus.Publish("config/head", []byte("not json"), true) == nil {
		t.Error("a payload that is not JSON was published")
	}
	if h.bus.Publish("config/+", []byte("{}"), true) == nil {
		t.Error("a wildcard topic was published")
	}
}

// A live subscription gets durable and non-durable events from now on, and nothing once it ends.
func TestSubscribe(t *testing.T) {
	h := newHarness(t, Options{})
	got := make(chan string, 4)
	stop, err := h.bus.Subscribe("config/#", func(topic string, payload []byte) { got <- topic + " " + string(payload) })
	if err != nil {
		t.Fatal(err)
	}
	h.publish("config/head", "1", false)
	h.publish("config/head", "2", true)
	h.publish("alias/lan", "3", true)
	if g := receive(t, got); g != "config/head 1" {
		t.Fatal(g)
	}
	if g := receive(t, got); g != "config/head 2" {
		t.Fatal(g)
	}
	nothing(t, got)
	stop()
	h.publish("config/head", "4", false)
	nothing(t, got)
}

// A durable consumer gets each durable event after its cursor once handled, in order, across restarts; a new one
// starts at the end of the log; a handler's error retries the event.
func TestConsume(t *testing.T) {
	h := newHarness(t, Options{})
	h.publish("config/head", "0", true) // before the consumer exists: not its
	got := make(chan string, 8)
	fail := true
	stop, err := h.bus.Consume("notify", "config/#", func(m Message) error {
		if string(m.Payload) == "2" && fail {
			fail = false
			return errors.New("not now")
		}
		got <- string(m.Payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h.publish("config/head", "1", true)
	h.publish("config/head", "9", false) // not durable: not a consumer's
	h.publish("alias/lan", "8", true)    // not its filter
	h.publish("config/head", "2", true)
	for _, want := range []string{"1", "2"} {
		if g := receive(t, got); g != want {
			t.Fatalf("got %s, want %s", g, want)
		}
	}
	nothing(t, got)
	stop()

	h.publish("config/head", "3", true) // while nobody reads
	h.restart()
	if _, err = h.bus.Consume("notify", "config/#", func(m Message) error { got <- string(m.Payload); return nil }); err != nil {
		t.Fatal(err)
	}
	if g := receive(t, got); g != "3" {
		t.Fatalf("after a restart got %s, want 3", g)
	}
	nothing(t, got)
}

// A consumer behind the log's retention is told of the gap, then reads on from what is kept.
func TestGap(t *testing.T) {
	h := newHarness(t, Options{Retention: time.Hour})
	stop, err := h.bus.Consume("notify", "config/#", func(Message) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	stop()
	h.publish("config/head", "10", true)
	if err = h.bus.prune(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.publish("config/head", "11", true)
	got := make(chan Message, 4)
	if _, err = h.bus.Consume("notify", "config/#", func(m Message) error { got <- m; return nil }); err != nil {
		t.Fatal(err)
	}
	if m := receive(t, got); !m.Gap {
		t.Fatalf("got %+v, want the gap first", m)
	}
	if m := receive(t, got); string(m.Payload) != "11" {
		t.Fatalf("got %+v", m)
	}
}

// Pruning keeps what a consumer has not read, drops what every consumer is done with, and drops a consumer
// nobody attached for the retention.
func TestPrune(t *testing.T) {
	h := newHarness(t, Options{Retention: time.Hour})
	done := make(chan string, 4)
	stop, err := h.bus.Consume("fast", "config/#", func(Message) error { done <- "fast"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	unblock := make(chan struct{})
	slow, err := h.bus.Consume("slow", "config/#", func(Message) error { <-unblock; done <- "slow"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.publish("config/head", "1", true)
	receive(t, done)
	if err = h.bus.prune(time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := rows(t, h, "events"); n != 1 {
		t.Fatalf("%d events kept, want 1: slow has not read it", n)
	}
	close(unblock)
	receive(t, done)
	h.eventually(func() bool { return h.bus.prune(time.Now()) == nil && rows(t, h, "events") == 0 })
	slow()
	stop()
	if err = h.bus.prune(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := rows(t, h, "consumers"); n != 0 {
		t.Fatalf("%d consumers kept past the retention", n)
	}
}

func (h *harness) eventually(done func() bool) {
	h.t.Helper()
	for start := time.Now(); !done(); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			h.t.Fatal("waited in vain")
		}
	}
}

func rows(t *testing.T, h *harness, table string) int {
	t.Helper()
	var n int
	if err := h.bus.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A client authenticates, publishes and subscribes within its grants only.
func TestClientGrants(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := Dial(ctx(t), h.socket, "web", "wrong"); err == nil {
		t.Error("a wrong token was accepted")
	}
	if _, err := Dial(ctx(t), h.socket, "stranger", "secret"); err == nil {
		t.Error("a user without grants was accepted")
	}
	web, addon := h.dial("web"), h.dial("addon")
	if err := web.Subscribe(ctx(t), "config/#"); err != nil {
		t.Fatal(err)
	}
	if web.Subscribe(ctx(t), "alias/#") == nil {
		t.Error("web subscribed to alias/#")
	}
	if addon.Subscribe(ctx(t), "alias/lan/#") != nil || addon.Subscribe(ctx(t), "#") == nil {
		t.Error("the addon's subscribe grants were not applied")
	}
	if addon.Publish(ctx(t), "config/head", []byte("{}"), false) == nil {
		t.Error("addon published to config/head")
	}
	if err := addon.Publish(ctx(t), "alias/lan", []byte(`{"v":1}`), true); err != nil {
		t.Fatal(err)
	}
	if web.Consume(ctx(t), "mine", "alias/#") == nil {
		t.Error("web consumed alias/#")
	}
	h.publish("config/head", "1", false)
	if d := receive(t, web.Messages()); d.Topic != "config/head" || string(d.Payload) != "1" {
		t.Fatalf("got %+v", d)
	}
	nothing(t, web.Messages())
}

// A client's durable consumer gets what it has not acked again when it reconnects, and not what it acked.
func TestClientConsume(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.dial("web")
	if err := web.Consume(ctx(t), "ui", "config/#"); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		h.publish("config/head", strconv.Itoa(i), true)
	}
	var last Delivery
	for range 3 {
		last = receive(t, web.Messages())
		if last.Consumer != "ui" {
			t.Fatalf("got %+v", last)
		}
	}
	if err := web.Ack("ui", last.Seq-1); err != nil { // the first two
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	web.Close()
	h.restart()
	web = h.dial("web")
	if err := web.Consume(ctx(t), "ui", "config/#"); err != nil {
		t.Fatal(err)
	}
	if d := receive(t, web.Messages()); string(d.Payload) != "2" {
		t.Fatalf("got %+v, want only the unacked 2", d)
	}
	nothing(t, web.Messages())
}

// A consumer gets at most window events before it acks.
func TestClientConsumeWindow(t *testing.T) {
	h := newHarness(t, Options{})
	web := h.dial("web")
	if err := web.Consume(ctx(t), "ui", "config/#"); err != nil {
		t.Fatal(err)
	}
	for i := range window + 10 {
		h.publish("config/head", strconv.Itoa(i), true)
	}
	var last Delivery
	for range window {
		last = receive(t, web.Messages())
	}
	nothing(t, web.Messages())
	if err := web.Ack("ui", last.Seq); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		receive(t, web.Messages())
	}
}

// A consumer's name is read by one attachment at a time: a new one takes it over.
func TestConsumerTakeover(t *testing.T) {
	h := newHarness(t, Options{})
	first, second := h.dial("web"), h.dial("web")
	if err := first.Consume(ctx(t), "ui", "config/#"); err != nil {
		t.Fatal(err)
	}
	if err := second.Consume(ctx(t), "ui", "config/#"); err != nil {
		t.Fatal(err)
	}
	h.publish("config/head", "1", true)
	receive(t, second.Messages())
	nothing(t, first.Messages())
}

// A store the bus cannot open is ErrStore, so the engine can set it aside.
func TestUnusableStoreIsErrStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.db")
	if err := os.WriteFile(path, []byte("not a database, not even close to one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Path: path}); !errors.Is(err, ErrStore) {
		t.Fatalf("Open: %v", err)
	}
}

// A consumer that is kept but whose events aged out of the log is told of the gap.
func TestGapByAge(t *testing.T) {
	h := newHarness(t, Options{Retention: time.Hour})
	stop, err := h.bus.Consume("notify", "config/#", func(Message) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	stop()
	h.publish("config/head", "1", true)
	if _, err = h.bus.db.Exec(`UPDATE events SET time = time - 7200`); err != nil {
		t.Fatal(err)
	}
	if err = h.bus.prune(time.Now()); err != nil {
		t.Fatal(err)
	}
	got := make(chan Message, 2)
	if _, err = h.bus.Consume("notify", "config/#", func(m Message) error { got <- m; return nil }); err != nil {
		t.Fatal(err)
	}
	if m := receive(t, got); !m.Gap {
		t.Fatalf("got %+v, want the gap", m)
	}
	nothing(t, got)
}

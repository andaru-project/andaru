package outbound

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/delivery"
)

type bench struct {
	t       *testing.T
	dir     string
	bus     *bus.Bus
	socket  string
	cache   string
	raised  chan string // "<type> <key>" of each notification andaru-outbound publishes
	cancel  context.CancelFunc
	stopped chan struct{}
}

func newBench(t *testing.T) *bench {
	dir, err := os.MkdirTemp("", "ob") // a Unix socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b := &bench{t: t, dir: dir, cache: filepath.Join(dir, "cache"), raised: make(chan string, 64)}
	if err := os.Mkdir(b.cache, 0o700); err != nil {
		t.Fatal(err)
	}
	b.open(false)
	return b
}

func (b *bench) open(lost bool) {
	b.t.Helper()
	b.socket = filepath.Join(b.dir, "bus.sock")
	_ = os.Remove(b.socket)
	l, err := net.Listen("unix", b.socket)
	if err != nil {
		b.t.Fatal(err)
	}
	b.bus, err = bus.Open(bus.Options{
		Path:         filepath.Join(b.dir, "bus.db"),
		Grants:       map[string]bus.Grants{User: {Publish: []string{"notification/delivery"}, Subscribe: []string{delivery.Filter}}},
		Authenticate: func(_, _ string) bool { return true },
		Listeners:    map[string]net.Listener{"test": l},
		LostHistory:  lost,
	})
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { b.bus.Close() })
	if _, err := b.bus.Subscribe("notification/#", func(_ string, payload []byte) {
		var e bus.Event
		var r struct{ Key string }
		_ = json.Unmarshal(payload, &e)
		data, _ := json.Marshal(e.Data)
		_ = json.Unmarshal(data, &r)
		b.raised <- e.Type + " " + r.Key
	}); err != nil {
		b.t.Fatal(err)
	}
}

func (b *bench) run(s *Service) {
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel, b.stopped = cancel, make(chan struct{})
	s.Socket, s.CacheDir = b.socket, b.cache
	// A new consumer starts at the end of the log: publish only once it is attached.
	ready := make(chan struct{}, 1)
	s.Ready = func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	defer func() {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			b.t.Fatal("andaru-outbound did not attach")
		}
	}()
	if s.Backoff == nil {
		s.Backoff = func(int) time.Duration { return time.Millisecond }
	}
	go func() {
		defer close(b.stopped)
		if err := s.Run(ctx); err != nil {
			b.t.Error(err)
		}
	}()
	b.t.Cleanup(b.stop)
}

func (b *bench) stop() {
	if b.cancel != nil {
		b.cancel()
		<-b.stopped
		b.cancel = nil
	}
}

func (b *bench) publish(job delivery.Job) {
	b.t.Helper()
	payload, err := bus.NewEvent(delivery.JobType, "notifications", job)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := b.bus.Publish(delivery.Topic(job.Channel), payload, true); err != nil {
		b.t.Fatal(err)
	}
}

// waitSent waits until andaru-outbound has recorded id as sent.
func (b *bench) waitSent(id string) {
	b.t.Helper()
	for range 1000 {
		if data, err := os.ReadFile(filepath.Join(b.cache, "sent.json")); err == nil && strings.Contains(string(data), `"`+id+`"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.t.Fatalf("%s was not recorded as sent", id)
}

func (b *bench) wantRaised(want string) {
	b.t.Helper()
	select {
	case got := <-b.raised:
		if got != want {
			b.t.Fatalf("raised %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		b.t.Fatalf("nothing raised, want %q", want)
	}
}

// hook is a webhook receiver that answers each request with the next status in its list, then 204.
type hook struct {
	mu       sync.Mutex
	statuses []int
	bodies   []delivery.Message
	headers  []string
	got      chan struct{}
}

func newHook(t *testing.T, statuses ...int) (*hook, string) {
	h := &hook{statuses: statuses, got: make(chan struct{}, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		status := http.StatusNoContent
		if len(h.statuses) > 0 {
			status, h.statuses = h.statuses[0], h.statuses[1:]
		}
		if status/100 == 2 {
			var m delivery.Message
			_ = json.NewDecoder(r.Body).Decode(&m)
			h.bodies = append(h.bodies, m)
			h.headers = append(h.headers, r.Header.Get("Authorization"))
		}
		h.mu.Unlock()
		w.WriteHeader(status)
		h.got <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return h, srv.URL
}

func (h *hook) wait(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-h.got:
		case <-time.After(10 * time.Second):
			t.Fatal("the webhook was not called")
		}
	}
}

func (h *hook) subjects() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, m := range h.bodies {
		out = append(out, m.Subject)
	}
	return out
}

func job(id, channel, url string) delivery.Job {
	return delivery.Job{ID: id, Channel: channel, Name: "Ops", Kind: delivery.Webhook,
		Message: delivery.Message{Source: "apply", Key: "k", Severity: "warning", Subject: "subject " + id, Raised: time.Unix(1, 0).UTC()},
		Webhook: &delivery.WebhookTarget{URL: url, Headers: map[string]string{"Authorization": "Bearer t"}}}
}

func TestWebhookJobIsSentOnce(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t)
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	h.wait(t, 1)
	// The same job again, as after a crash of the layer between publishing and its ack, is not sent twice; a new one is.
	b.publish(job("e1:ch", "ch", url))
	b.publish(job("e2:ch", "ch", url))
	h.wait(t, 1)
	time.Sleep(50 * time.Millisecond)
	if got := h.subjects(); !slices.Equal(got, []string{"subject e1:ch", "subject e2:ch"}) || h.headers[0] != "Bearer t" {
		t.Fatalf("sent %q with %q", got, h.headers)
	}
}

func TestSentJobsSurviveARestart(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t)
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	h.wait(t, 1)
	b.waitSent("e1:ch")
	b.stop()
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	b.publish(job("e2:ch", "ch", url))
	h.wait(t, 1)
	time.Sleep(50 * time.Millisecond)
	if got := h.subjects(); !slices.Equal(got, []string{"subject e1:ch", "subject e2:ch"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestFailedSendIsRetried(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t, 500, 503)
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	h.wait(t, 3)
	if got := h.subjects(); !slices.Equal(got, []string{"subject e1:ch"}) {
		t.Fatalf("sent %q", got)
	}
}

// Three jobs given up in a row raise the channel's notification; the next success clears it.
func TestFailingChannelRaisesItsNotification(t *testing.T) {
	b := newBench(t)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer failing.Close()
	h, url := newHook(t)
	b.run(&Service{RetryFor: 5 * time.Millisecond, Backoff: func(int) time.Duration { return 3 * time.Millisecond }})
	for _, id := range []string{"e1", "e2", "e3"} {
		b.publish(job(id+":ch", "ch", failing.URL))
	}
	b.wantRaised("notification.raised.v1 channel/ch")
	b.publish(job("e4:ch", "ch", url))
	h.wait(t, 1)
	b.wantRaised("notification.cleared.v1 channel/ch")
}

func TestUnusableJobIsGivenUpAtOnce(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t)
	b.run(&Service{RetryFor: time.Hour})
	bad := job("e1:ch", "ch", "ftp://example.invalid/")
	b.publish(bad)
	b.publish(job("e2:ch", "ch", url))
	h.wait(t, 1)
	if got := h.subjects(); !slices.Equal(got, []string{"subject e2:ch"}) {
		t.Fatalf("sent %q", got)
	}
}

// Jobs the bus lost before andaru-outbound read them raise a notification of their own.
func TestGapRaisesMissedDeliveries(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t)
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	h.wait(t, 1)
	b.stop()
	b.bus.Close()
	// The store is lost and replaced: the consumer comes back to an empty log.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(b.dir, "bus.db"+suffix))
	}
	b.open(true)
	b.run(&Service{})
	b.wantRaised("notification.raised.v1 missed")
}

// A slow job does not hold up the next, and the cursor waits for it: stopped then, the slow job comes back.
func TestSlowJobIsNotSkipped(t *testing.T) {
	b := newBench(t)
	var slowCalls atomic.Int32
	fastDone := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m delivery.Message
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m.Subject == "subject slow:ch" {
			if slowCalls.Add(1) == 1 {
				<-r.Context().Done() // the first attempt never finishes
			}
			return
		}
		fastDone <- struct{}{}
	}))
	defer srv.Close()
	b.run(&Service{})
	b.publish(job("slow:ch", "ch", srv.URL))
	b.publish(job("fast:ch", "ch", srv.URL))
	<-fastDone
	b.waitSent("fast:ch")
	b.stop() // the slow job is unfinished and unacked
	b.run(&Service{})
	b.waitSent("slow:ch")
	if n := slowCalls.Load(); n != 2 {
		t.Fatalf("the slow job was tried %d times, want once more after the restart", n)
	}
}

func TestOrderAcksOnlyAPrefix(t *testing.T) {
	var o order
	for _, seq := range []int64{1, 2, 3} {
		o.start(seq)
	}
	for _, s := range []struct{ finish, want int64 }{{2, 0}, {1, 2}, {3, 3}} {
		if got := o.finish(s.finish); got != s.want {
			t.Fatalf("finish(%d) = %d, want %d", s.finish, got, s.want)
		}
	}
}

// A copy of a job delivered while the first is still being sent is not sent again.
func TestCopyInFlightIsNotSentTwice(t *testing.T) {
	b := newBench(t)
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
	}))
	defer srv.Close()
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", srv.URL))
	b.publish(job("e1:ch", "ch", srv.URL))
	time.Sleep(100 * time.Millisecond)
	close(release)
	b.waitSent("e1:ch")
	if n := calls.Load(); n != 1 {
		t.Fatalf("sent %d times", n)
	}
}

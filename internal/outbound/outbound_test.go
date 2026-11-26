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
	"github.com/andaru-project/andaru/internal/notify"
)

type raised struct{ typ, key, detail string }

type bench struct {
	t       *testing.T
	dir     string
	bus     *bus.Bus
	socket  string
	cache   string
	raised  chan raised // what andaru-outbound publishes about itself
	cancel  context.CancelFunc
	stopped chan struct{}
}

func newBench(t *testing.T) *bench {
	dir, err := os.MkdirTemp("", "ob") // a Unix socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b := &bench{t: t, dir: dir, cache: filepath.Join(dir, "cache"), raised: make(chan raised, 256)}
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
		var e struct {
			Type string
			Data notify.Raise
		}
		_ = json.Unmarshal(payload, &e)
		b.raised <- raised{e.Type, e.Data.Key, e.Data.Detail}
	}); err != nil {
		b.t.Fatal(err)
	}
}

func (b *bench) run(s *Service) {
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel, b.stopped = cancel, make(chan struct{})
	s.Socket, s.CacheDir = b.socket, b.cache
	if s.Backoff == nil {
		s.Backoff = func(int) time.Duration { return time.Millisecond }
	}
	// A new consumer starts at the end of the log: publish only once it is attached.
	ready := make(chan struct{}, 1)
	s.Ready = func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	go func() {
		defer close(b.stopped)
		if err := s.Run(ctx); err != nil {
			b.t.Error(err)
		}
	}()
	b.t.Cleanup(b.stop)
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		b.t.Fatal("andaru-outbound did not attach")
	}
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

// wantRaised waits for andaru-outbound to publish typ for key, skipping anything else, and returns it.
func (b *bench) wantRaised(typ, key string) raised {
	b.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case r := <-b.raised:
			if r.typ == typ && r.key == key {
				return r
			}
		case <-deadline:
			b.t.Fatalf("no %s for %q", typ, key)
		}
	}
}

// noRaise checks nothing raised key so far.
func (b *bench) noRaise(key string) {
	b.t.Helper()
	for {
		select {
		case r := <-b.raised:
			if r.typ == notify.RaisedType && r.key == key {
				b.t.Fatalf("%q raised too early", key)
			}
		default:
			return
		}
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

func (b *bench) sentHas(id string) bool {
	data, _ := os.ReadFile(filepath.Join(b.cache, "sent.json"))
	return strings.Contains(string(data), `"`+id+`"`)
}

// queued is how many jobs wait in the retry queue.
func (b *bench) queued() int {
	entries, _ := os.ReadDir(filepath.Join(b.cache, "retry"))
	return len(entries)
}

func (b *bench) waitQueued(n int) {
	b.t.Helper()
	for range 1000 {
		if b.queued() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.t.Fatalf("%d jobs queued, want %d", b.queued(), n)
}

// hook is a webhook receiver that answers each request with the next status in its list, then 204.
type hook struct {
	mu       sync.Mutex
	statuses []int
	subjects []string
	headers  []string
	got      chan struct{}
}

func newHook(t *testing.T, statuses ...int) (*hook, string) {
	h := &hook{statuses: statuses, got: make(chan struct{}, 256)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		status := http.StatusNoContent
		if len(h.statuses) > 0 {
			status, h.statuses = h.statuses[0], h.statuses[1:]
		}
		if status/100 == 2 {
			h.subjects = append(h.subjects, subjectOf(r))
			h.headers = append(h.headers, r.Header.Get("Authorization"))
		}
		h.mu.Unlock()
		w.WriteHeader(status)
		h.got <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return h, srv.URL
}

// subjectOf reads the subject from a webhook's body: the notification's event as the bus carries it.
func subjectOf(r *http.Request) string {
	var e struct {
		Type string
		Data notify.Raise
	}
	_ = json.NewDecoder(r.Body).Decode(&e)
	if e.Type != notify.RaisedType {
		return "not a notification event"
	}
	return e.Data.Subject
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

func (h *hook) sent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.subjects)
}

func job(id, channel, url string) delivery.Job {
	r := notify.Raise{Key: "k", Severity: notify.Warning, Subject: "subject " + id}
	event, _ := bus.NewEvent(notify.RaisedType, "apply", r)
	return delivery.Job{ID: id, Channel: channel, Name: "Ops", Kind: delivery.Webhook, Event: event,
		Message: delivery.Message{Source: "apply", Key: r.Key, Severity: r.Severity, Subject: r.Subject, Raised: time.Unix(1, 0).UTC()},
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
	b.waitSent("e2:ch")
	if got := h.sent(); !slices.Equal(got, []string{"subject e1:ch", "subject e2:ch"}) || h.headers[0] != "Bearer t" {
		t.Fatalf("sent %q with %q", got, h.headers)
	}
}

func TestSentJobsSurviveARestart(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t)
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	b.waitSent("e1:ch")
	b.stop()
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	b.publish(job("e2:ch", "ch", url))
	b.waitSent("e2:ch")
	if got := h.sent(); !slices.Equal(got, []string{"subject e1:ch", "subject e2:ch"}) {
		t.Fatalf("sent %q", got)
	}
}

func TestFailedSendIsRetried(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t, 500, 503)
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	b.waitSent("e1:ch")
	b.waitQueued(0)
	if got := h.sent(); !slices.Equal(got, []string{"subject e1:ch"}) {
		t.Fatalf("sent %q", got)
	}
}

// A queued job is retried after a restart, from the queue: the bus has its ack already.
func TestQueuedJobSurvivesARestart(t *testing.T) {
	b := newBench(t)
	var down atomic.Bool
	down.Store(true)
	var subjects []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(503)
			return
		}
		mu.Lock()
		subjects = append(subjects, subjectOf(r))
		mu.Unlock()
	}))
	defer srv.Close()
	b.run(&Service{Backoff: func(int) time.Duration { return time.Hour }})
	b.publish(job("e1:ch", "ch", srv.URL))
	b.waitQueued(1)
	b.stop()
	down.Store(false)
	b.run(&Service{})
	b.waitSent("e1:ch")
	b.waitQueued(0)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(subjects, []string{"subject e1:ch"}) {
		t.Fatalf("sent %q", subjects)
	}
}

// One dead channel holds up no other: more jobs than the bus's window go through while it fails.
func TestDeadChannelDoesNotHoldUpOthers(t *testing.T) {
	b := newBench(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer dead.Close()
	h, url := newHook(t)
	b.run(&Service{Backoff: func(int) time.Duration { return time.Hour }})
	b.publish(job("a1:a", "a", dead.URL))
	for i := range 100 {
		b.publish(job("b"+strings.Repeat("x", i)+":b", "b", url))
	}
	h.wait(t, 100)
}

// Three jobs given up in a row raise the channel's notification, not fewer; the next success clears it. The error
// in the notification does not show the webhook's URL, which is often its secret.
func TestFailingChannelRaisesItsNotification(t *testing.T) {
	b := newBench(t)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL + "/hooks/SECRETTOKEN?k=QSECRET"
	dead.Close() // refused connections: Go's error names the URL
	h, url := newHook(t)
	b.run(&Service{RetryFor: 100 * time.Millisecond, Backoff: func(int) time.Duration { return 20 * time.Millisecond }})
	for _, id := range []string{"e1", "e2"} {
		b.publish(job(id+":ch", "ch", deadURL))
		b.waitQueued(1)
		b.waitQueued(0)
	}
	time.Sleep(20 * time.Millisecond)
	b.noRaise("channel/ch")
	b.publish(job("e3:ch", "ch", deadURL))
	r := b.wantRaised(notify.RaisedType, "channel/ch")
	if strings.Contains(r.detail, "SECRET") || !strings.Contains(r.detail, "connection refused") {
		t.Fatalf("detail %q", r.detail)
	}
	b.publish(job("e4:ch", "ch", url))
	h.wait(t, 1)
	b.wantRaised(notify.ClearedType, "channel/ch")
}

// After a restart nothing remembers the channel's notification: its first success clears it anyway.
func TestFirstSuccessClearsAChannelNotification(t *testing.T) {
	b := newBench(t)
	_, url := newHook(t)
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	b.wantRaised(notify.ClearedType, "channel/ch")
}

func TestUnusableJobIsGivenUpAtOnce(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t)
	b.run(&Service{RetryFor: time.Hour})
	b.publish(job("e1:ch", "ch", "ftp://example.invalid/"))
	b.publish(job("e2:ch", "ch", url))
	b.waitSent("e2:ch")
	time.Sleep(20 * time.Millisecond)
	if got := h.sent(); !slices.Equal(got, []string{"subject e2:ch"}) || b.queued() != 0 {
		t.Fatalf("sent %q, %d queued", got, b.queued())
	}
}

// A redirect is not followed: the job's headers stay with the host it names, and a 3xx is no delivery.
func TestRedirectIsNotFollowed(t *testing.T) {
	b := newBench(t)
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.RedirectHandler(other.URL, http.StatusTemporaryRedirect))
	defer srv.Close()
	b.run(&Service{Backoff: func(int) time.Duration { return time.Hour }})
	// A 3xx comes back the same on a retry: each job is given up at once, so three raise the channel's notification.
	for _, id := range []string{"e1", "e2", "e3"} {
		b.publish(job(id+":ch", "ch", srv.URL))
	}
	b.wantRaised(notify.RaisedType, "channel/ch")
	if n := elsewhere.Load(); n != 0 || b.queued() != 0 || b.sentHas("e1:ch") {
		t.Fatalf("the redirect target was called %d times, %d queued, e1 sent %v", n, b.queued(), b.sentHas("e1:ch"))
	}
}

// Jobs the bus lost before andaru-outbound read them raise a notification of their own, and the consumer goes on.
func TestGapRaisesMissedDeliveries(t *testing.T) {
	b := newBench(t)
	h, url := newHook(t)
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", url))
	b.waitSent("e1:ch")
	b.stop()
	b.bus.Close()
	// The store is lost and replaced: the consumer comes back to an empty log.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(b.dir, "bus.db"+suffix))
	}
	b.open(true)
	b.run(&Service{})
	b.wantRaised(notify.RaisedType, "missed")
	b.publish(job("e2:ch", "ch", url))
	b.waitSent("e2:ch")
	if got := h.sent(); !slices.Equal(got, []string{"subject e1:ch", "subject e2:ch"}) {
		t.Fatalf("sent %q", got)
	}
}

// A slow job does not hold up the next, and the cursor waits for it: stopped then, the slow job comes back.
func TestSlowJobIsNotSkipped(t *testing.T) {
	b := newBench(t)
	var slowCalls atomic.Int32
	fastDone := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subjectOf(r) == "subject slow:ch" {
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

func TestOrderAcksOnlyAPrefix(t *testing.T) {
	var o order
	for _, seq := range []int64{1, 2, 3} {
		o.start(seq)
	}
	if !o.has(2) || o.has(4) {
		t.Fatal("has")
	}
	for _, s := range []struct{ finish, want int64 }{{2, 0}, {1, 2}, {3, 3}} {
		if got := o.finish(s.finish); got != s.want {
			t.Fatalf("finish(%d) = %d, want %d", s.finish, got, s.want)
		}
	}
}

// A queue write that fails loses nothing while the service runs: the job is retried from memory, and the other jobs
// go through.
func TestFailedQueueWriteIsRetriedFromMemory(t *testing.T) {
	b := newBench(t)
	var tries atomic.Int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if tries.Add(1) <= 2 {
			w.WriteHeader(503)
		}
	}))
	defer flaky.Close()
	h, url := newHook(t)
	b.run(&Service{Backoff: func(int) time.Duration { return time.Millisecond }})
	// The queue file's temporary name is taken by a directory: every write of this job fails.
	q := &retries{dir: filepath.Join(b.cache, "retry")}
	if err := os.MkdirAll(filepath.Join(q.file("a1:a")+".tmp", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	b.publish(job("a1:a", "a", flaky.URL))
	b.wantRaised(notify.RaisedType, "queue")
	for i := range 100 {
		b.publish(job("b"+strings.Repeat("x", i)+":b", "b", url))
	}
	h.wait(t, 100)
	b.waitSent("a1:a")
	// Sent, a1 is no longer held in memory only.
	b.wantRaised(notify.ClearedType, "queue")
}

// A crash between recording a retried job as sent and taking it out of the queue does not send it again.
func TestSentJobLeftInTheQueueIsNotSentAgain(t *testing.T) {
	b := newBench(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	q, err := openRetries(b.cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.save(&retry{Job: job("e1:ch", "ch", srv.URL), First: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sent, _ := openSent(b.cache)
	if err := sent.add("e1:ch", time.Now()); err != nil {
		t.Fatal(err)
	}
	b.run(&Service{})
	b.waitQueued(0)
	if n := calls.Load(); n != 0 {
		t.Fatalf("sent %d times", n)
	}
}

// A job the bus delivers again while it waits in the queue is not sent from the bus a second time.
func TestQueuedJobIsNotSentAgainFromTheBus(t *testing.T) {
	b := newBench(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	b.run(&Service{Backoff: func(int) time.Duration { return time.Hour }})
	b.publish(job("e1:ch", "ch", srv.URL))
	b.waitQueued(1)
	b.publish(job("e1:ch", "ch", srv.URL))
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("tried %d times", n)
	}
}

// What a crash leaves in the queue: a damaged entry and a temporary file are removed, not sent.
func TestDamagedQueueEntriesAreRemoved(t *testing.T) {
	dir := t.TempDir()
	q, err := openRetries(dir)
	if err != nil {
		t.Fatal(err)
	}
	good := &retry{Job: job("e1:ch", "ch", "http://example.invalid/")}
	if err := q.save(good); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(q.file("e1:ch"))
	for name, body := range map[string][]byte{"bad.json": []byte("{"), q.file("e2:ch")[len(q.dir)+1:] + ".tmp": data} {
		if err := os.WriteFile(filepath.Join(q.dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if q, err = openRetries(dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(q.dir)
	if len(q.items) != 1 || !q.has("e1:ch") || len(entries) != 1 {
		t.Fatalf("%d kept, %d files", len(q.items), len(entries))
	}
}

func TestBusyRetryIsNotDueTwice(t *testing.T) {
	q, err := openRetries(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.save(&retry{Job: job("e1:ch", "ch", "http://example.invalid/")}); err != nil {
		t.Fatal(err)
	}
	if first, second := q.due(time.Now()), q.due(time.Now()); len(first) != 1 || len(second) != 0 {
		t.Fatalf("due %d then %d", len(first), len(second))
	}
}

// A queued job whose hour ran out while the service was down is given up, not sent late.
func TestExpiredQueuedJobIsGivenUp(t *testing.T) {
	b := newBench(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	q, err := openRetries(b.cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.save(&retry{Job: job("e1:ch", "ch", srv.URL), First: time.Now().Add(-2 * time.Hour), Attempts: 3}); err != nil {
		t.Fatal(err)
	}
	b.run(&Service{RetryFor: time.Hour})
	b.waitQueued(0)
	if n := calls.Load(); n != 0 || b.sentHas("e1:ch") {
		t.Fatalf("sent %d times", n)
	}
}

// After a restart, a failure does not clear a warning a previous run raised: only a success does.
func TestRestartedFailureKeepsTheChannelWarning(t *testing.T) {
	b := newBench(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer dead.Close()
	b.run(&Service{})
	b.publish(job("e1:ch", "ch", dead.URL)) // a 404 is given up at once
	b.publish(job("e2:ch", "ch", dead.URL))
	time.Sleep(100 * time.Millisecond)
	for {
		select {
		case r := <-b.raised:
			if r.key == "channel/ch" {
				t.Fatalf("a failure published %s for the channel", r.typ)
			}
			continue
		default:
		}
		break
	}
}

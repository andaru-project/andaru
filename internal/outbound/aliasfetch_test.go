package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/aliasfeed"
	"github.com/andaru-project/andaru/internal/bus"
)

func serve(t *testing.T, tls bool, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	if tls {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

func body(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(text)) }
}

func TestFetchList(t *testing.T) {
	many := strings.Repeat("10.0.0.1\n", aliasfeed.MaxMembers)
	var unique, dups strings.Builder
	for i := range aliasfeed.MaxMembers + 1 {
		fmt.Fprintf(&unique, "10.%d.%d.1\n", i/256, i%256)
	}
	for i := range 10500 { // 9,900 entries, 600 of them twice, once in another form
		if i < 9900 {
			fmt.Fprintf(&dups, "10.%d.%d.1\n", i/256, i%256)
		} else {
			fmt.Fprintf(&dups, "::ffff:10.%d.%d.1\n", (i-9900)/256, (i-9900)%256)
		}
	}
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
		err        string
	}{
		{name: "list", body: "# Cloudflare\n\n203.0.113.7\n198.51.100.0/24 # a network\r\n; old\n2001:db8::/32\n\t2001:db8::1\t#host\n10.1.2.3/8\n",
			want: []string{"10.0.0.0/8", "198.51.100.0/24", "2001:db8::/32", "2001:db8::1", "203.0.113.7"}},
		{name: "empty", body: "", want: []string{}},
		{name: "comments only", body: "# nothing\n; here\n\n", want: []string{}},
		{name: "html", body: "  <!doctype html><p>hi", err: "not a list"},
		{name: "html in a comment", body: "# not <html>\n10.0.0.1 # nor <html>\n", want: []string{"10.0.0.1"}},
		{name: "bom", body: "\ufeff10.0.0.1\n", want: []string{"10.0.0.1"}},
		{name: "garbage", body: "10.0.0.1\n\nexample.com\n", err: "line 3: "},
		{name: "glued comment", body: "10.0.0.1#x\n", err: "line 1: "},
		{name: "most entries", body: many, want: []string{"10.0.0.1"}},
		{name: "too many entries", body: unique.String(), err: "more than 10000"},
		{name: "duplicates within the limit", body: dups.String(), want: nil}, // checked by length below
		{name: "too large", body: "#" + strings.Repeat("x", maxList), err: "list larger than 1 MiB"},
		{name: "status", status: http.StatusNotFound, err: "status 404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var agent string
			srv := serve(t, false, func(w http.ResponseWriter, r *http.Request) {
				agent = r.UserAgent()
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := fetchList(context.Background(), aliasClient(nil), srv.URL+"/list")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error %v, want %q", err, tc.err)
				}
				return
			}
			if tc.name == "duplicates within the limit" {
				if err != nil || len(got) != 9900 {
					t.Fatalf("%d entries, %v", len(got), err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if agent != User {
				t.Errorf("User-Agent %q", agent)
			}
		})
	}
}

// A line that does not parse is quoted cut: a long one-line body still gives a result small enough to publish.
func TestFetchListLongLine(t *testing.T) {
	srv := serve(t, false, body("é"+strings.Repeat("x", 600<<10)))
	_, err := fetchList(context.Background(), aliasClient(nil), srv.URL)
	if err == nil || !strings.HasPrefix(err.Error(), `line 1: "é`) || len(err.Error()) > 120 {
		t.Fatalf("error %.200v (%d bytes)", err, len(fmt.Sprint(err)))
	}
}

func TestFetchRedirects(t *testing.T) {
	list := body("192.0.2.1\n")
	toURL := func(u string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, u, http.StatusFound) }
	}
	secure := serve(t, true, list)
	plain := serve(t, false, list)
	trusting := aliasClient(secure.Client().Transport) // trusts httptest's certificate, which every TLS server shares
	for _, tc := range []struct {
		name string
		from *httptest.Server
		err  string
	}{
		{"https to https", serve(t, true, toURL(secure.URL+"/l")), ""},
		{"https to http", serve(t, true, toURL(plain.URL+"/l")), "redirect from https to http refused"},
		{"http to https", serve(t, false, toURL(secure.URL+"/l")), ""},
		{"http to http", serve(t, false, toURL(plain.URL+"/l")), ""},
		{"other scheme", serve(t, false, toURL("ftp://example.com/l")), `redirect to a "ftp" address refused`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fetchList(context.Background(), trusting, tc.from.URL+"/?token=s3cret")
			if tc.err == "" {
				if err != nil || !slices.Equal(got, []string{"192.0.2.1"}) {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) || strings.Contains(err.Error(), "s3cret") {
				t.Fatalf("error %v, want %q without the query", err, tc.err)
			}
		})
	}

	t.Run("a Location that does not parse", func(t *testing.T) {
		srv := serve(t, false, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "/%zz?token=secret")
			w.WriteHeader(http.StatusFound)
		})
		_, err := fetchList(context.Background(), trusting, srv.URL)
		if err == nil || err.Error() != "a redirect's Location does not parse" {
			t.Fatalf("error %v", err)
		}
	})

	t.Run("five redirects, then six", func(t *testing.T) {
		var hops int
		var srv *httptest.Server
		srv = serve(t, true, func(w http.ResponseWriter, r *http.Request) {
			if n := len(r.URL.Query()["h"]); n < hops {
				http.Redirect(w, r, srv.URL+"/?"+r.URL.RawQuery+"&h=1", http.StatusFound)
				return
			}
			list(w, r)
		})
		hops = maxRedirects
		if _, err := fetchList(context.Background(), trusting, srv.URL+"/?token=s3cret"); err != nil {
			t.Fatalf("5 redirects: %v", err)
		}
		hops = maxRedirects + 1
		_, err := fetchList(context.Background(), trusting, srv.URL+"/?token=s3cret")
		if err == nil || !strings.Contains(err.Error(), "more than 5 redirects") || strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("6 redirects: %v", err)
		}
	})
}

func TestFetchUntrustedCertificate(t *testing.T) {
	srv := serve(t, true, body("192.0.2.1\n"))
	_, err := fetchList(context.Background(), aliasClient(nil), srv.URL+"/?token=s3cret")
	if err == nil || !strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error %v", err)
	}
}

func fakeLookup(answers map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, name string) ([]netip.Addr, error) {
		got, ok := answers[name]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: name, Server: "127.0.0.53:53", IsNotFound: true}
		}
		if len(got) == 1 && got[0] == "timeout" {
			return nil, errors.New("i/o timeout")
		}
		var out []netip.Addr
		for _, a := range got {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
}

func TestResolve(t *testing.T) {
	if !goResolver.PreferGo {
		t.Fatal("the C library's resolver would be used")
	}
	lookup := fakeLookup(map[string][]string{"a.example": {"192.0.2.1", "2001:db8::1", "::ffff:192.0.2.2"}, "empty.example": {}, "slow.example": {"timeout"},
		"zoned.example": {"fe80::1%eth0", "192.0.2.5"}, "zone-only.example": {"fe80::1%eth0"}})
	s := &Service{Lookup: lookup, Log: slog.New(slog.DiscardHandler)}
	req := aliasfeed.Request{Hash: "h", Kind: "hostnames"}

	req.Names = []string{"a.example", "nx.example"}
	r, entries := s.fetch(context.Background(), "x", req)
	if !r.OK || entries != 3 || r.Names["nx.example"].Error != "no such host" ||
		!slices.Equal(r.Names["a.example"].Addresses, []string{"192.0.2.1", "2001:db8::1", "192.0.2.2"}) {
		t.Fatalf("one of two failing: %+v, %d", r, entries)
	}

	req.Names = []string{"nx.example", "empty.example", "slow.example", "zone-only.example"}
	r, _ = s.fetch(context.Background(), "x", req)
	if r.OK || r.Error != "nx.example: no such host; empty.example: no addresses; slow.example: i/o timeout; zone-only.example: no addresses" || len(r.Names) != 4 {
		t.Fatalf("all failing: %+v", r)
	}

	// A zoned address (fe80::1%eth0 in /etc/hosts) fits no set: dropped, so the engine takes the rest.
	req.Names = []string{"zoned.example"}
	r, _ = s.fetch(context.Background(), "x", req)
	if !r.OK || !slices.Equal(r.Names["zoned.example"].Addresses, []string{"192.0.2.5"}) {
		t.Fatalf("zoned: %+v", r)
	}
}

// logBuf records a Service's log, safe for concurrent use.
type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.b.String(), s)
}

func (l *logBuf) logger() *slog.Logger { return slog.New(slog.NewTextHandler(l, nil)) }

// aliasBench is a bench that sends alias requests and reads their results.
type aliasBench struct {
	*bench
	results chan bus.Message
}

func newAliasBench(t *testing.T) *aliasBench {
	b := &aliasBench{newBench(t), make(chan bus.Message, 64)}
	b.subscribe()
	return b
}

func (b *aliasBench) subscribe() {
	if _, err := b.bus.Subscribe(aliasfeed.ResultFilter, func(topic string, payload []byte) {
		b.results <- bus.Message{Topic: topic, Payload: payload}
	}); err != nil {
		b.t.Fatal(err)
	}
}

func (b *aliasBench) send(req aliasfeed.Request) {
	b.t.Helper()
	payload, err := bus.NewEvent(aliasfeed.RequestType, "engine", req)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := b.bus.Publish(aliasfeed.Topic(req.Alias), payload, true); err != nil {
		b.t.Fatal(err)
	}
}

func (b *aliasBench) next(wait time.Duration) (string, aliasfeed.Result) {
	b.t.Helper()
	select {
	case m := <-b.results:
		var r aliasfeed.Result
		if e := (bus.Event{Data: &r}); json.Unmarshal(m.Payload, &e) != nil || e.Type != aliasfeed.ResultType {
			b.t.Fatalf("not a result: %s", m.Payload)
		}
		return m.Topic, r
	case <-time.After(wait):
		b.t.Fatal("no result")
	}
	return "", aliasfeed.Result{}
}

func (b *aliasBench) none(wait time.Duration) {
	b.t.Helper()
	select {
	case m := <-b.results:
		b.t.Fatalf("an unexpected result: %s", m.Payload)
	case <-time.After(wait):
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal(what)
		}
	}
}

// TestAliasRequests sends requests through a real bus: each result comes back on its alias's result topic with its
// request and hash, and requests for a source waiting for a fetch slot are answered by one fetch, as its newest.
func TestAliasRequests(t *testing.T) {
	b := newAliasBench(t)
	release := make(chan struct{})
	var fetches atomic.Int32
	srv := serve(t, false, func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if r.URL.Path == "/slow" {
			<-release
		}
		_, _ = w.Write([]byte("192.0.2.0/24\n"))
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	b.run(&Service{Lookup: fakeLookup(map[string][]string{"vpn.example": {"198.51.100.9"}}), Log: slog.New(slog.DiscardHandler)})

	b.send(aliasfeed.Request{ID: "r1", Alias: "a1", Hash: "h1", Kind: "url", URL: srv.URL + "/list"})
	topic, r := b.next(10 * time.Second)
	if topic != aliasfeed.ResultTopic("a1") || r.Request != "r1" || r.Hash != "h1" || !r.OK || !slices.Equal(r.Members, []string{"192.0.2.0/24"}) {
		t.Fatalf("%s: %+v", topic, r)
	}
	b.send(aliasfeed.Request{ID: "r2", Alias: "a2", Hash: "h2", Kind: "hostnames", Names: []string{"vpn.example"}})
	topic, r = b.next(10 * time.Second)
	if topic != aliasfeed.ResultTopic("a2") || r.Request != "r2" || !r.OK || !slices.Equal(r.Names["vpn.example"].Addresses, []string{"198.51.100.9"}) {
		t.Fatalf("%s: %+v", topic, r)
	}

	// Every slot busy, then three requests for one source: one fetch once a slot frees, answered as the newest.
	fetches.Store(0)
	for i := range maxFetches {
		b.send(aliasfeed.Request{ID: fmt.Sprint("f", i), Alias: fmt.Sprint("f", i), Hash: "h", Kind: "url", URL: srv.URL + "/slow"})
	}
	eventually(t, "the slots did not fill", func() bool { return fetches.Load() == maxFetches })
	for i := range 3 {
		b.send(aliasfeed.Request{ID: fmt.Sprint("s", i), Alias: "a3", Hash: "h3", Kind: "url", URL: srv.URL + "/slow"})
	}
	time.Sleep(200 * time.Millisecond) // the bus hands all three over before a slot frees
	close(release)
	var a3 []string
	for range maxFetches + 1 {
		if topic, r := b.next(10 * time.Second); topic == aliasfeed.ResultTopic("a3") {
			a3 = append(a3, r.Request)
		}
	}
	if !slices.Equal(a3, []string{"s2"}) || fetches.Load() != maxFetches+1 {
		t.Fatalf("a3 answered %q after %d fetches", a3, fetches.Load())
	}
	b.none(200 * time.Millisecond)

	// Every request was acked, the ones a fetch joined too: a new session gets none of them again.
	b.stop()
	b.run(&Service{Log: slog.New(slog.DiscardHandler)})
	b.none(300 * time.Millisecond)
}

// A request that arrives once the fetch of its source has started gets a fetch of its own, not the older data.
func TestAliasRefreshDuringFetch(t *testing.T) {
	b := newAliasBench(t)
	gate := make(chan struct{})
	var fetches atomic.Int32
	srv := serve(t, false, func(w http.ResponseWriter, _ *http.Request) {
		if fetches.Add(1) == 1 {
			<-gate
		}
		_, _ = w.Write([]byte("192.0.2.1\n"))
	})
	t.Cleanup(func() { close(gate) }) // before the server closes, which waits for the handler
	b.run(&Service{Log: slog.New(slog.DiscardHandler)})
	b.send(aliasfeed.Request{ID: "t0", Alias: "a", Hash: "h", Kind: "url", URL: srv.URL})
	eventually(t, "no fetch", func() bool { return fetches.Load() == 1 })
	b.send(aliasfeed.Request{ID: "t1", Alias: "a", Hash: "h", Kind: "url", URL: srv.URL})
	if _, r := b.next(5 * time.Second); r.Request != "t1" || fetches.Load() != 2 {
		t.Fatalf("%+v after %d fetches", r, fetches.Load())
	}
}

// A result the bus refuses ends the session, so the requests come again instead of holding the acks up.
func TestAliasPublishFailureEndsSession(t *testing.T) {
	b := newAliasBench(t)
	srv := serve(t, false, body("192.0.2.1\n"))
	var log logBuf
	b.run(&Service{Log: log.logger()})
	b.send(aliasfeed.Request{ID: "r", Alias: "a/b", Hash: "h", Kind: "url", URL: srv.URL}) // alias-result/+ refuses a/b
	eventually(t, "the session went on", func() bool { return log.count("bus connection lost") > 0 })
	if log.count("publish the result for alias a/b") == 0 {
		t.Fatal("the error was not logged")
	}
}

// A session that ends during a fetch publishes nothing; the next one fetches again.
func TestAliasSessionEndsDuringFetch(t *testing.T) {
	b := newAliasBench(t)
	var fetches atomic.Int32
	srv := serve(t, false, func(w http.ResponseWriter, r *http.Request) {
		if fetches.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("192.0.2.1\n"))
	})
	b.run(&Service{Log: slog.New(slog.DiscardHandler)})
	b.send(aliasfeed.Request{ID: "r", Alias: "a", Hash: "h", Kind: "url", URL: srv.URL})
	eventually(t, "no fetch", func() bool { return fetches.Load() == 1 })
	b.stop()
	b.none(200 * time.Millisecond)
	b.run(&Service{Log: slog.New(slog.DiscardHandler)})
	if _, r := b.next(10 * time.Second); r.Request != "r" || !r.OK {
		t.Fatalf("%+v", r)
	}
}

// Each name that does not resolve is logged once.
func TestAliasFailedNameLoggedOnce(t *testing.T) {
	b := newAliasBench(t)
	var log logBuf
	b.run(&Service{Lookup: fakeLookup(nil), Log: log.logger()})
	b.send(aliasfeed.Request{ID: "r", Alias: "a", Hash: "h", Kind: "hostnames", Names: []string{"nx.example"}})
	if _, r := b.next(10 * time.Second); r.OK {
		t.Fatalf("%+v", r)
	}
	eventually(t, "no summary logged", func() bool { return log.count("alias=a") == 2 }) // the name's line and the alias's
	if n := log.count("nx.example"); n != 1 {
		t.Fatalf("nx.example logged %d times", n)
	}
}

// A gap in the alias requests is acked: the next session is not told of it again.
func TestAliasGapAcked(t *testing.T) {
	b := newAliasBench(t)
	b.run(&Service{Log: slog.New(slog.DiscardHandler)})
	b.stop()
	b.bus.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(b.dir, "bus.db"+suffix))
	}
	b.open(true)
	b.subscribe()
	var first, second logBuf
	b.run(&Service{Log: first.logger()})
	eventually(t, "no gap", func() bool { return first.count("alias requests may have been missed") == 1 })
	time.Sleep(100 * time.Millisecond) // the ack reaches the bus
	b.stop()
	b.run(&Service{Log: second.logger()})
	time.Sleep(300 * time.Millisecond)
	if second.count("alias requests may have been missed") != 0 {
		t.Fatal("the gap came again")
	}
}

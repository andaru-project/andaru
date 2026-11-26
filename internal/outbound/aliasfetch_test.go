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
	"slices"
	"strings"
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
		{name: "html inside", body: "10.0.0.1\nfoo <HTML>\n", err: "not a list"},
		{name: "garbage", body: "10.0.0.1\n\nexample.com\n", err: "line 3: "},
		{name: "glued comment", body: "10.0.0.1#x\n", err: "line 1: "},
		{name: "most entries", body: many, want: []string{"10.0.0.1"}},
		{name: "too many entries", body: many + "10.0.0.2\n", err: "more than 10000 entries"},
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
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if agent != User {
				t.Errorf("User-Agent %q", agent)
			}
		})
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
	lookup := fakeLookup(map[string][]string{"a.example": {"192.0.2.1", "2001:db8::1", "::ffff:192.0.2.2"}, "empty.example": {}, "slow.example": {"timeout"}})
	s := &Service{Lookup: lookup, Log: slog.New(slog.DiscardHandler)}
	req := aliasfeed.Request{Hash: "h", Kind: "hostnames"}

	req.Names = []string{"a.example", "nx.example"}
	r, entries := s.fetch(context.Background(), "x", req)
	if !r.OK || entries != 3 || r.Names["nx.example"].Error != "no such host" ||
		!slices.Equal(r.Names["a.example"].Addresses, []string{"192.0.2.1", "2001:db8::1", "192.0.2.2"}) {
		t.Fatalf("one of two failing: %+v, %d", r, entries)
	}

	req.Names = []string{"nx.example", "empty.example", "slow.example"}
	r, _ = s.fetch(context.Background(), "x", req)
	if r.OK || r.Error != "nx.example: no such host; empty.example: no addresses; slow.example: i/o timeout" || len(r.Names) != 3 {
		t.Fatalf("all failing: %+v", r)
	}
}

// TestAliasRequests sends requests through a real bus: each result comes back on its alias's result topic with its
// request and hash, and requests for a source being fetched are answered by that fetch, as its newest.
func TestAliasRequests(t *testing.T) {
	b := newBench(t)
	results := make(chan bus.Message, 16)
	if _, err := b.bus.Subscribe(aliasfeed.ResultFilter, func(topic string, payload []byte) {
		results <- bus.Message{Topic: topic, Payload: payload}
	}); err != nil {
		t.Fatal(err)
	}
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
	send := func(req aliasfeed.Request) {
		t.Helper()
		payload, err := bus.NewEvent(aliasfeed.RequestType, "engine", req)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.bus.Publish(aliasfeed.Topic(req.Alias), payload, true); err != nil {
			t.Fatal(err)
		}
	}
	next := func() (string, aliasfeed.Result) {
		t.Helper()
		select {
		case m := <-results:
			var r aliasfeed.Result
			if e := (bus.Event{Data: &r}); json.Unmarshal(m.Payload, &e) != nil || e.Type != aliasfeed.ResultType {
				t.Fatalf("not a result: %s", m.Payload)
			}
			return m.Topic, r
		case <-time.After(10 * time.Second):
			t.Fatal("no result")
		}
		return "", aliasfeed.Result{}
	}

	send(aliasfeed.Request{ID: "r1", Alias: "a1", Hash: "h1", Kind: "url", URL: srv.URL + "/list"})
	topic, r := next()
	if topic != aliasfeed.ResultTopic("a1") || r.Request != "r1" || r.Hash != "h1" || !r.OK || !slices.Equal(r.Members, []string{"192.0.2.0/24"}) {
		t.Fatalf("%s: %+v", topic, r)
	}
	send(aliasfeed.Request{ID: "r2", Alias: "a2", Hash: "h2", Kind: "hostnames", Names: []string{"vpn.example"}})
	topic, r = next()
	if topic != aliasfeed.ResultTopic("a2") || r.Request != "r2" || !r.OK || !slices.Equal(r.Names["vpn.example"].Addresses, []string{"198.51.100.9"}) {
		t.Fatalf("%s: %+v", topic, r)
	}

	// Three requests for one source while its fetch hangs: one fetch, answered as the newest.
	fetches.Store(0)
	for i := range 3 {
		send(aliasfeed.Request{ID: fmt.Sprint("s", i), Alias: "a3", Hash: "h3", Kind: "url", URL: srv.URL + "/slow"})
	}
	time.Sleep(200 * time.Millisecond) // the bus hands all three over before the fetch ends
	close(release)
	topic, r = next()
	if topic != aliasfeed.ResultTopic("a3") || r.Request != "s2" || fetches.Load() != 1 {
		t.Fatalf("%s: %+v after %d fetches", topic, r, fetches.Load())
	}
	select {
	case m := <-results:
		t.Fatalf("a second result: %s", m.Payload)
	case <-time.After(200 * time.Millisecond):
	}

	// Every request was acked: a new session gets none of them again.
	b.stop()
	b.run(&Service{Log: slog.New(slog.DiscardHandler)})
	select {
	case m := <-results:
		t.Fatalf("answered again after a restart: %s", m.Payload)
	case <-time.After(300 * time.Millisecond):
	}
}

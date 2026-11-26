package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/aliasfeed"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/notify"
)

type published struct {
	topic   string
	payload []byte
	durable bool
}

type feedBench struct {
	t        *testing.T
	f        *aliasFeeds
	list     []config.DynamicAlias
	sent     []published
	now      time.Time
	realized int
}

const urlAlias, hostAlias = "adr_00000001-0000-4000-8000-000000000001", "adr_00000002-0000-4000-8000-000000000002"

func newFeedBench(t *testing.T) *feedBench {
	b := &feedBench{t: t, now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC), list: []config.DynamicAlias{
		{ID: urlAlias, Name: "cloudflare", Kind: "url", URL: "https://www.cloudflare.com/ips-v4", RefreshSeconds: 3600},
		{ID: hostAlias, Name: "vpn", Kind: "hostnames", Names: []string{"a.example", "b.example"}, RefreshSeconds: 60},
	}}
	b.f = &aliasFeeds{stateDir: t.TempDir(), log: slog.New(slog.DiscardHandler), poke: make(chan struct{}, 1),
		aliases: func(context.Context) ([]config.DynamicAlias, error) { return slices.Clone(b.list), nil },
		publish: func(topic string, payload []byte, durable bool) error {
			b.sent = append(b.sent, published{topic, payload, durable})
			return nil
		},
		realize: func(context.Context) error { b.realized++; return nil },
		now:     func() time.Time { return b.now }}
	return b
}

// requests returns the requests published since the last call.
func (b *feedBench) requests() []aliasfeed.Request {
	var out []aliasfeed.Request
	for _, p := range b.sent {
		if strings.HasPrefix(p.topic, "alias-fetch/") {
			var r aliasfeed.Request
			if e := (bus.Event{Data: &r}); json.Unmarshal(p.payload, &e) != nil || e.Type != aliasfeed.RequestType || !p.durable || p.topic != aliasfeed.Topic(r.Alias) {
				b.t.Fatalf("request %s %s", p.topic, p.payload)
			}
			out = append(out, r)
		}
	}
	b.sent = slices.DeleteFunc(b.sent, func(p published) bool { return strings.HasPrefix(p.topic, "alias-fetch/") })
	return out
}

func (b *feedBench) ask(id string) string {
	b.t.Helper()
	request, ok, err := b.f.refresh(b.t.Context(), id)
	if err != nil || !ok {
		b.t.Fatal(ok, err)
	}
	b.requests()
	return request
}

func (b *feedBench) answer(id string, r aliasfeed.Result) aliasfeed.State {
	b.t.Helper()
	payload, err := bus.NewEvent(aliasfeed.ResultType, "andaru-outbound", r)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := b.f.handle(bus.Message{Seq: 1, Topic: aliasfeed.ResultTopic(id), Payload: payload}); err != nil {
		b.t.Fatal(err)
	}
	s, err := b.f.load(id)
	if err != nil {
		b.t.Fatal(err)
	}
	return s
}

// notices returns the alias notifications published since the last call: the key and the severity, "" for a clear.
func (b *feedBench) notices() []string {
	var out []string
	for _, p := range b.sent {
		if p.topic == "notification/"+aliasSource {
			var r notify.Raise
			if e := (bus.Event{Data: &r}); json.Unmarshal(p.payload, &e) != nil || !p.durable {
				b.t.Fatalf("notice %s", p.payload)
			}
			out = append(out, r.Key+" "+r.Severity)
		}
	}
	b.sent = slices.DeleteFunc(b.sent, func(p published) bool { return p.topic == "notification/"+aliasSource })
	return out
}

func TestAliasScheduleIsDueOnItsInterval(t *testing.T) {
	b := newFeedBench(t)
	// Never fetched: both at start.
	if wait := b.f.schedule(t.Context()); wait != time.Minute || len(b.requests()) != 2 {
		t.Fatalf("wait %v", wait)
	}
	b.now = b.now.Add(30 * time.Second)
	if wait := b.f.schedule(t.Context()); wait != 30*time.Second || len(b.requests()) != 0 {
		t.Fatalf("wait %v", wait)
	}
	b.now = b.now.Add(30 * time.Second)
	got := b.requests()
	if wait := b.f.schedule(t.Context()); wait != time.Minute {
		t.Fatalf("wait %v", wait)
	}
	if got = b.requests(); len(got) != 1 || got[0].Alias != hostAlias || !slices.Equal(got[0].Names, []string{"a.example", "b.example"}) || got[0].Kind != "hostnames" {
		t.Fatalf("requests %+v", got)
	}
	// A changed URL is fetched at once, long before its interval.
	b.list[0].URL = "https://www.cloudflare.com/ips-v6"
	b.now = b.now.Add(time.Second)
	b.f.schedule(t.Context())
	if got = b.requests(); len(got) != 1 || got[0].Alias != urlAlias || got[0].URL != b.list[0].URL || got[0].Kind != "url" {
		t.Fatalf("requests %+v", got)
	}
}

func TestAliasDue(t *testing.T) {
	a := config.DynamicAlias{Kind: "url", URL: "https://x.example/list", RefreshSeconds: 600}
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		state aliasfeed.State
		due   bool
		wait  time.Duration
	}{
		{aliasfeed.State{}, true, 0},
		{aliasfeed.State{Hash: aliasHash(a), LastAttempt: now.Add(-time.Minute)}, false, 9 * time.Minute},
		{aliasfeed.State{Hash: aliasHash(a), LastAttempt: now.Add(-10 * time.Minute)}, true, 0},
		{aliasfeed.State{Hash: "other", LastAttempt: now}, true, 0},
	} {
		if due, wait := due(a, row.state, now); due != row.due || (!due && wait != row.wait) {
			t.Errorf("%+v: due %v in %v", row.state, due, wait)
		}
	}
}

func TestAliasResultsKeepTheLastGoodList(t *testing.T) {
	b := newFeedBench(t)
	good := []string{"192.0.2.0/24", "2001:db8::/32"}
	s := b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true, Members: good, FetchedAt: b.now})
	if !slices.Equal(s.Members, good) || s.Failures != 0 || !s.LastSuccess.Equal(b.now) || b.realized != 1 {
		t.Fatalf("state %+v realized %d", s, b.realized)
	}
	if len(b.sent) != 1 || b.sent[0].topic != "alias/"+urlAlias || b.sent[0].durable {
		t.Fatalf("published %+v", b.sent)
	}
	b.sent = nil
	// The same list again changes nothing in the filter.
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true, Members: good})
	if b.realized != 1 || len(b.sent) != 0 {
		t.Fatalf("realized %d published %+v", b.realized, b.sent)
	}
	many := make([]string, aliasfeed.MaxMembers+1)
	for i := range many {
		many[i] = fmt.Sprintf("10.%d.%d.1", i/256, i%256)
	}
	for n, r := range []aliasfeed.Result{
		{OK: true, Members: []string{"192.0.2.1", "<html>"}},
		{OK: true, Members: many},
		{OK: false, Error: "status 503"},
	} {
		r.Request = b.ask(urlAlias)
		s = b.answer(urlAlias, r)
		if !slices.Equal(s.Members, good) || s.Failures != n+1 || s.LastError == "" || b.realized != 1 {
			t.Fatalf("failure %d: state %+v", n, s)
		}
		if n < 2 && !strings.HasPrefix(s.LastError, "result not valid") {
			t.Fatalf("failure %d: %q", n, s.LastError)
		}
		// The third in a row raises the notification.
		if got := b.notices(); n < 2 && len(got) != 0 || n == 2 && !slices.Equal(got, []string{"alias/" + urlAlias + " warning"}) {
			t.Fatalf("failure %d: notices %q", n, got)
		}
	}
	// A result for a request no longer waited for is dropped.
	if s = b.answer(urlAlias, aliasfeed.Result{Request: "old", OK: true}); !slices.Equal(s.Members, good) {
		t.Fatalf("state %+v", s)
	}
	// An empty list is a list: it replaces the last, and the success clears the notification.
	s = b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true})
	if s.Members == nil || len(s.Members) != 0 || s.Failures != 0 || s.LastError != "" || b.realized != 2 {
		t.Fatalf("state %+v realized %d", s, b.realized)
	}
	if got := b.notices(); !slices.Equal(got, []string{"alias/" + urlAlias + " "}) {
		t.Fatalf("notices %q", got)
	}
}

func TestHostNameResultsKeepEachName(t *testing.T) {
	b := newFeedBench(t)
	s := b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), OK: true, Names: map[string]aliasfeed.NameResult{
		"a.example": {Addresses: []string{"192.0.2.1", "2001:db8::1"}}, "b.example": {Addresses: []string{"192.0.2.2"}}}})
	if !slices.Equal(s.Members, []string{"192.0.2.1", "192.0.2.2", "2001:db8::1"}) || s.Failures != 0 {
		t.Fatalf("state %+v", s)
	}
	// A failed name keeps what it last resolved to; the other's answer is taken.
	s = b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), Names: map[string]aliasfeed.NameResult{
		"a.example": {Addresses: []string{"192.0.2.9"}}, "b.example": {Error: "NXDOMAIN"}}})
	if !slices.Equal(s.Members, []string{"192.0.2.2", "192.0.2.9"}) || s.Failures != 1 || s.LastError != "b.example: NXDOMAIN" ||
		s.Names[1].Error != "NXDOMAIN" || !slices.Equal(s.Names[1].Addresses, []string{"192.0.2.2"}) {
		t.Fatalf("state %+v", s)
	}
	// A network for a name is not valid, and replaces nothing.
	s = b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), OK: true, Names: map[string]aliasfeed.NameResult{
		"a.example": {Addresses: []string{"192.0.2.0/24"}}, "b.example": {Addresses: []string{"192.0.2.3"}}}})
	if !slices.Equal(s.Members, []string{"192.0.2.2", "192.0.2.9"}) || s.Failures != 2 || !strings.HasPrefix(s.LastError, "result not valid") {
		t.Fatalf("state %+v", s)
	}
}

func TestRemovedAliasesLoseTheirState(t *testing.T) {
	b := newFeedBench(t)
	for range aliasFailures {
		b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), Error: "timeout"})
	}
	b.notices()
	b.list = b.list[1:]
	b.f.schedule(t.Context())
	if _, err := os.Stat(aliasfeed.Path(b.f.stateDir, urlAlias)); !os.IsNotExist(err) {
		t.Fatalf("state kept: %v", err)
	}
	if got := b.notices(); !slices.Equal(got, []string{"alias/" + urlAlias + " "}) {
		t.Fatalf("notices %q", got)
	}
	if _, ok, err := b.f.refresh(t.Context(), urlAlias); ok || err != nil {
		t.Fatal("refreshed a removed alias")
	}
}

func TestAliasesAPI(t *testing.T) {
	b := newFeedBench(t)
	h := newHandlerWithAllServices(nil, http.NotFoundHandler(), nil, nil, nil, nil, nil, nil, nil, aliasesAPI{feeds: b.f})
	call := func(method, path string, capabilities ...authz.Capability) *httptest.ResponseRecorder {
		t.Helper()
		actor := requestActor{Actor: authz.Actor{Kind: authz.ActorAccount, ID: "00000000-0000-4000-8000-000000000001"},
			AccountID: "00000000-0000-4000-8000-000000000001", Source: "unix", Capabilities: capabilities}
		r := httptest.NewRequest(method, path, nil)
		r = r.WithContext(context.WithValue(r.Context(), authenticatedActorKey{}, actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	refresh := aliasesPath + "/" + urlAlias + "/refresh"
	if w := call(http.MethodPost, refresh, authz.ConfigurationRead); w.Code != http.StatusForbidden {
		t.Fatalf("without configuration.write: %d", w.Code)
	}
	w := call(http.MethodPost, refresh, authz.ConfigurationWrite)
	var body struct{ Request string }
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	if got := b.requests(); len(got) != 1 || got[0].ID != body.Request || got[0].Alias != urlAlias {
		t.Fatalf("requests %+v", got)
	}
	for _, path := range []string{aliasesPath + "/adr_none/refresh", aliasesPath + "//refresh", aliasesPath + "/" + urlAlias} {
		if w := call(http.MethodPost, path, authz.ConfigurationWrite); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), Names: map[string]aliasfeed.NameResult{
		"a.example": {Addresses: []string{"192.0.2.1"}}, "b.example": {Error: "NXDOMAIN"}}, FetchedAt: b.now})
	if w := call(http.MethodGet, aliasesPath+"/status", authz.StatusRead); w.Code != http.StatusForbidden {
		t.Fatalf("without configuration.read: %d", w.Code)
	}
	w = call(http.MethodGet, aliasesPath+"/status", authz.ConfigurationRead)
	var status struct{ Items []json.RawMessage }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil || len(status.Items) != 2 {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	at := b.now.Format(time.RFC3339)
	want := []string{
		`{"id":"` + urlAlias + `","name":"cloudflare","kind":"url","lastSuccess":null,"lastAttempt":"` + at + `","entries":0,"failures":0}`,
		`{"id":"` + hostAlias + `","name":"vpn","kind":"hostnames","lastSuccess":null,"lastAttempt":"` + at + `","entries":1,"lastError":"b.example: NXDOMAIN","failures":1,` +
			`"names":[{"name":"a.example","addresses":["192.0.2.1"],"resolvedAt":"` + at + `"},{"name":"b.example","addresses":[],"error":"NXDOMAIN","resolvedAt":null}]}`,
	}
	for i, item := range status.Items {
		if string(item) != want[i] {
			t.Errorf("item %d:\n got %s\nwant %s", i, item, want[i])
		}
	}
}

// A load that fails is tried again at the next result, changed or not.
func TestAliasLoadIsRetried(t *testing.T) {
	b := newFeedBench(t)
	b.f.realize = func(context.Context) error { b.realized++; return fmt.Errorf("lock held") }
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true, Members: []string{"192.0.2.1"}})
	b.f.realize = func(context.Context) error { b.realized++; return nil }
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: false, Error: "timeout"})
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: false, Error: "timeout"})
	if b.realized != 2 {
		t.Fatalf("realized %d times", b.realized)
	}
}

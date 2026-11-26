package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	t *testing.T
	f *aliasFeeds
	// list is the saved head's aliases, live the live generation's.
	list, live []config.DynamicAlias
	sent       []published
	now        time.Time
	realized   int
}

const urlAlias, hostAlias = "adr_00000001-0000-4000-8000-000000000001", "adr_00000002-0000-4000-8000-000000000002"

func newFeedBench(t *testing.T) *feedBench {
	b := &feedBench{t: t, now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC), list: []config.DynamicAlias{
		{ID: urlAlias, Name: "cloudflare", Kind: "url", URL: "https://www.cloudflare.com/ips-v4", RefreshSeconds: 3600},
		{ID: hostAlias, Name: "vpn", Kind: "hostnames", Names: []string{"a.example", "b.example"}, RefreshSeconds: 60},
	}}
	b.f = &aliasFeeds{life: t.Context(), stateDir: t.TempDir(), log: slog.New(slog.DiscardHandler), poke: make(chan struct{}, 1), applied: make(chan struct{}, 1),
		aliases: func(context.Context) ([]config.DynamicAlias, []config.DynamicAlias, error) {
			return slices.Clone(b.list), slices.Clone(b.live), nil
		},
		publish: func(topic string, payload []byte, durable bool) error {
			b.sent = append(b.sent, published{topic, payload, durable})
			return nil
		},
		realize: func(context.Context) error { b.realized++; return nil },
		now:     func() time.Time { return b.now }, refreshed: map[string]refresh{}, cleared: map[string]bool{}}
	// Applied: the live generation names what the saved head does.
	b.live = slices.Clone(b.list)
	return b
}

// src is the saved head's source of alias i.
func (b *feedBench) src(i int) source { return sourceOf(b.list[i]) }

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

// ask publishes one request for the saved head's source of alias id, past the refresh limit, and returns its ID.
func (b *feedBench) ask(id string) string {
	b.t.Helper()
	b.now = b.now.Add(refreshEvery)
	request, ok, err := b.f.refresh(b.t.Context(), id)
	if err != nil || !ok {
		b.t.Fatal(ok, err)
	}
	b.requests()
	return request
}

// answer delivers r for the saved head's source of alias id, unless r names a hash, and returns that source's state.
func (b *feedBench) answer(id string, r aliasfeed.Result) aliasfeed.State {
	b.t.Helper()
	if r.Hash == "" {
		i := slices.IndexFunc(b.list, func(a config.DynamicAlias) bool { return a.ID == id })
		r.Hash = b.src(i).hash
	}
	payload, err := bus.NewEvent(aliasfeed.ResultType, "andaru-outbound", r)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := b.f.handle(bus.Message{Seq: 1, Topic: aliasfeed.ResultTopic(id), Payload: payload}); err != nil {
		b.t.Fatal(err)
	}
	return b.f.load(source{id, r.Hash})
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
	// A changed URL is a new source, fetched at once, long before its interval.
	b.list[0].URL = "https://www.cloudflare.com/ips-v6"
	b.now = b.now.Add(time.Second)
	b.f.schedule(t.Context())
	if got = b.requests(); len(got) != 1 || got[0].Alias != urlAlias || got[0].URL != b.list[0].URL || got[0].Kind != "url" || got[0].Hash != b.src(0).hash {
		t.Fatalf("requests %+v", got)
	}
	// Unanswered, a source is not asked for again before its interval: requests do not pile up.
	for range 6 {
		b.now = b.now.Add(10 * time.Second)
		b.f.schedule(t.Context())
	}
	if got = b.requests(); len(got) != 1 || got[0].Alias != hostAlias {
		t.Fatalf("requests %+v", got)
	}
	if s := b.f.load(b.src(1)); len(s.Pending) != 3 {
		t.Fatalf("pending %q", s.Pending)
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
		{aliasfeed.State{LastAttempt: now.Add(-time.Minute)}, false, 9 * time.Minute},
		{aliasfeed.State{LastAttempt: now.Add(-time.Minute), Pending: []string{"r"}}, false, 9 * time.Minute},
		{aliasfeed.State{LastAttempt: now.Add(-10 * time.Minute)}, true, 0},
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
	// A failed name keeps what it last resolved to and shows its error; the other's answer is taken, and the alias,
	// with one name answered, has not failed.
	s = b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), Names: map[string]aliasfeed.NameResult{
		"a.example": {Addresses: []string{"192.0.2.9"}}, "b.example": {Error: "NXDOMAIN"}}})
	if !slices.Equal(s.Members, []string{"192.0.2.2", "192.0.2.9"}) || s.Failures != 0 || s.LastError != "" || !s.LastSuccess.Equal(b.now) ||
		s.Names[1].Error != "NXDOMAIN" || !slices.Equal(s.Names[1].Addresses, []string{"192.0.2.2"}) {
		t.Fatalf("state %+v", s)
	}
	// Every name failing fails the alias; a name answered with no addresses, and none an error, failed.
	s = b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), OK: true, Names: map[string]aliasfeed.NameResult{
		"a.example": {}, "b.example": {Error: "SERVFAIL"}}})
	if !slices.Equal(s.Members, []string{"192.0.2.2", "192.0.2.9"}) || s.Failures != 1 || s.LastError != "a.example: no addresses; b.example: SERVFAIL" ||
		s.Names[0].Error != "no addresses" {
		t.Fatalf("state %+v", s)
	}
	// A network for a name is not valid, and replaces nothing.
	s = b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), OK: true, Names: map[string]aliasfeed.NameResult{
		"a.example": {Addresses: []string{"192.0.2.0/24"}}, "b.example": {Addresses: []string{"192.0.2.3"}}}})
	if !slices.Equal(s.Members, []string{"192.0.2.2", "192.0.2.9"}) || s.Failures != 2 || !strings.HasPrefix(s.LastError, "result not valid") {
		t.Fatalf("state %+v", s)
	}
}

// A result is kept only for the request and the source it answers.
func TestAliasResultsMatchTheirRequest(t *testing.T) {
	b := newFeedBench(t)
	b.live = nil
	req := b.ask(urlAlias)
	old := b.src(0).hash
	// The saved URL changed, and no generation is live: the old source's answer is dropped.
	b.list[0].URL = "https://lists.example/v4"
	if s := b.answer(urlAlias, aliasfeed.Result{Request: req, Hash: old, OK: true, Members: []string{"192.0.2.1"}}); len(s.Members) != 0 || b.realized != 0 {
		t.Fatalf("state %+v", s)
	}
	// An answer for the right request but another hash is not this source's either.
	req = b.ask(urlAlias)
	if s := b.answer(urlAlias, aliasfeed.Result{Request: req, Hash: old, OK: true, Members: []string{"192.0.2.1"}}); len(s.Members) != 0 {
		t.Fatalf("state %+v", s)
	}
	if s := b.f.load(b.src(0)); len(s.Pending) != 1 {
		t.Fatalf("pending %q", s.Pending)
	}
	// The same answer twice is taken once.
	first := b.answer(urlAlias, aliasfeed.Result{Request: req, OK: true, Members: []string{"192.0.2.1"}})
	again := b.answer(urlAlias, aliasfeed.Result{Request: req, OK: false, Error: "late"})
	if !slices.Equal(first.Members, []string{"192.0.2.1"}) || again.Failures != 0 || again.LastError != "" {
		t.Fatalf("state %+v then %+v", first, again)
	}
	// The requests a source waits for are bounded: the oldest are pushed out, and their answers dropped.
	var ids []string
	for range maxPending + 2 {
		ids = append(ids, b.ask(urlAlias))
	}
	if s := b.f.load(b.src(0)); !slices.Equal(s.Pending, ids[2:]) {
		t.Fatalf("pending %q", s.Pending)
	}
	if s := b.answer(urlAlias, aliasfeed.Result{Request: ids[1], OK: true, Members: []string{"192.0.2.2"}}); !slices.Equal(s.Members, []string{"192.0.2.1"}) {
		t.Fatalf("state %+v", s)
	}
}

// A saved change never touches the live filter: its new source is fetched beside the live one, which keeps its list
// and its refresh until an apply replaces it.
func TestSavedChangesKeepTheLiveSource(t *testing.T) {
	b := newFeedBench(t)
	live := b.src(0)
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true, Members: []string{"192.0.2.1"}})
	if b.realized != 1 {
		t.Fatalf("realized %d", b.realized)
	}
	b.list[0].URL = "https://lists.example/v4"
	b.f.schedule(t.Context())
	got := b.requests()
	if len(got) != 2 || got[0].Hash == got[1].Hash {
		t.Fatalf("requests %+v", got)
	}
	newer := slices.IndexFunc(got, func(r aliasfeed.Request) bool { return r.Hash == b.src(0).hash })
	if newer < 0 || got[newer].URL != b.list[0].URL {
		t.Fatalf("the saved source not prefetched: %+v", got)
	}
	// Its list is kept, and loaded: the load fills the sets the live filter names, which may already be this
	// source's, by an apply gone live since.
	if s := b.answer(urlAlias, aliasfeed.Result{Request: got[newer].ID, OK: true, Members: []string{"198.51.100.0/24"}}); len(s.Members) != 1 || b.realized != 2 {
		t.Fatalf("state %+v realized %d", s, b.realized)
	}
	if s := b.f.load(live); !slices.Equal(s.Members, []string{"192.0.2.1"}) {
		t.Fatalf("the live source lost its list: %+v", s)
	}
	// The live source keeps refreshing on its interval.
	b.now = b.now.Add(time.Hour)
	b.f.schedule(t.Context())
	if got = b.requests(); !slices.ContainsFunc(got, func(r aliasfeed.Request) bool { return r.Hash == live.hash }) {
		t.Fatalf("requests %+v", got)
	}
	// Removed from the saved head, the live alias keeps its state, its refresh and its notification.
	b.list = b.list[1:]
	b.now = b.now.Add(time.Hour)
	b.f.schedule(t.Context())
	if got = b.requests(); !slices.ContainsFunc(got, func(r aliasfeed.Request) bool { return r.Hash == live.hash }) {
		t.Fatalf("requests %+v", got)
	}
	if _, err := os.Stat(aliasfeed.Path(b.f.stateDir, live.alias, live.hash)); err != nil || len(b.notices()) != 0 {
		t.Fatalf("live state: %v", err)
	}
}

// The state of a source no wanted generation names stays 30 days after its last attempt, for a rollback or the boot
// of a kept generation; the alias's notification is cleared once no wanted generation names it.
func TestUnwantedSourcesAreRemovedByAge(t *testing.T) {
	b := newFeedBench(t)
	for range aliasFailures {
		b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), Error: "timeout"})
	}
	b.notices()
	gone := b.src(0)
	b.list, b.live = b.list[1:], b.live[1:]
	b.f.schedule(t.Context())
	if _, err := os.Stat(aliasfeed.Path(b.f.stateDir, gone.alias, gone.hash)); err != nil {
		t.Fatalf("state removed at once: %v", err)
	}
	if got := b.notices(); !slices.Equal(got, []string{"alias/" + urlAlias + " "}) {
		t.Fatalf("notices %q", got)
	}
	b.f.schedule(t.Context())
	if got := b.notices(); len(got) != 0 {
		t.Fatalf("cleared again: %q", got)
	}
	if _, ok, err := b.f.refresh(t.Context(), urlAlias); ok || err != nil {
		t.Fatal("refreshed a removed alias")
	}
	// A state moved aside goes after as long, so the alias's directory can go.
	aside := aliasfeed.Path(b.f.stateDir, gone.alias, gone.hash) + aliasfeed.Corrupt + strconv.FormatInt(b.now.Unix(), 10)
	if err := os.WriteFile(aside, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.now = b.now.Add(keepUnwanted - time.Minute)
	b.f.schedule(t.Context())
	if _, err := os.Stat(aliasfeed.Path(b.f.stateDir, gone.alias, gone.hash)); err != nil {
		t.Fatalf("state removed early: %v", err)
	}
	if _, err := os.Stat(aside); err != nil {
		t.Fatalf("moved-aside state removed early: %v", err)
	}
	b.now = b.now.Add(2 * time.Minute)
	b.f.schedule(t.Context())
	if _, err := os.Stat(filepath.Join(aliasfeed.Dir(b.f.stateDir), gone.alias)); !os.IsNotExist(err) {
		t.Fatalf("state kept: %v", err)
	}
	if _, err := os.Stat(aliasfeed.Path(b.f.stateDir, hostAlias, b.src(0).hash)); err != nil {
		t.Fatalf("a wanted state removed: %v", err)
	}
}

// "Refresh now" asks for the saved source and the live one, at most once in refreshEvery per alias.
func TestAliasRefreshIsLimited(t *testing.T) {
	b := newFeedBench(t)
	b.list[0].URL = "https://lists.example/v4"
	first, _, err := b.f.refresh(t.Context(), urlAlias)
	if got := b.requests(); err != nil || len(got) != 2 || got[0].ID != first || got[0].Hash != b.src(0).hash || got[1].Hash != sourceOf(b.live[0]).hash {
		t.Fatalf("requests %+v %v", got, err)
	}
	b.now = b.now.Add(refreshEvery - time.Second)
	if again, _, err := b.f.refresh(t.Context(), urlAlias); again != first || err != nil || len(b.requests()) != 0 {
		t.Fatalf("refreshed again: %s %v", again, err)
	}
	if other, _, _ := b.f.refresh(t.Context(), hostAlias); other == first || len(b.requests()) != 1 {
		t.Fatal("the limit is not per alias")
	}
	b.now = b.now.Add(time.Second)
	if again, _, _ := b.f.refresh(t.Context(), urlAlias); again == first || len(b.requests()) != 2 {
		t.Fatal("not refreshed after the limit")
	}
}

// A request is recorded before it is published: one that cannot be recorded is not sent, and one not sent is no
// attempt.
func TestAliasRequestIsSavedBeforeItIsSent(t *testing.T) {
	b := newFeedBench(t)
	// A file where the alias's directory goes: its state cannot be written.
	if err := os.MkdirAll(aliasfeed.Dir(b.f.stateDir), 0o700); err != nil || os.WriteFile(filepath.Join(aliasfeed.Dir(b.f.stateDir), urlAlias), nil, 0o600) != nil {
		t.Fatal(err)
	}
	if _, _, err := b.f.refresh(t.Context(), urlAlias); err == nil || len(b.requests()) != 0 {
		t.Fatalf("sent unrecorded: %v", err)
	}
	if wait := b.f.schedule(t.Context()); wait != time.Minute || len(b.requests()) != 1 {
		t.Fatalf("wait %v", wait)
	}
	b.f.publish = func(string, []byte, bool) error { return fmt.Errorf("bus closed") }
	b.now = b.now.Add(time.Hour)
	if _, _, err := b.f.refresh(t.Context(), hostAlias); err == nil {
		t.Fatal("unsent refresh succeeded")
	}
	if s := b.f.load(b.src(1)); len(s.Pending) != 1 || !s.LastAttempt.Equal(b.now.Add(-time.Hour)) {
		t.Fatalf("an unsent request recorded: %+v", s)
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
	at, first := b.now.Format(time.RFC3339), b.now.Add(-refreshEvery).Format(time.RFC3339)
	want := []string{
		`{"id":"` + urlAlias + `","name":"cloudflare","kind":"url","lastSuccess":null,"lastAttempt":"` + first + `","entries":0,"failures":0}`,
		`{"id":"` + hostAlias + `","name":"vpn","kind":"hostnames","lastSuccess":"` + at + `","lastAttempt":"` + at + `","entries":1,"failures":0,` +
			`"names":[{"name":"a.example","addresses":["192.0.2.1"],"resolvedAt":"` + at + `"},{"name":"b.example","addresses":[],"error":"NXDOMAIN","resolvedAt":null}]}`,
	}
	for i, item := range status.Items {
		if string(item) != want[i] {
			t.Errorf("item %d:\n got %s\nwant %s", i, item, want[i])
		}
	}
	// The status only reads: a state that does not decode is shown empty, and left where it is.
	path := aliasfeed.Path(b.f.stateDir, hostAlias, b.src(1).hash)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodGet, aliasesPath+"/status", authz.ConfigurationRead); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"entries":0`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if aside, _ := filepath.Glob(path + aliasfeed.Corrupt + "*"); len(aside) != 0 {
		t.Fatalf("moved aside: %v", aside)
	}
	// A request that cannot be recorded is not sent, and the refresh is unavailable.
	b.now = b.now.Add(refreshEvery)
	dir := filepath.Join(aliasfeed.Dir(b.f.stateDir), urlAlias)
	if err := os.RemoveAll(dir); err != nil || os.WriteFile(dir, nil, 0o600) != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPost, refresh, authz.ConfigurationWrite); w.Code != http.StatusServiceUnavailable || len(b.requests()) != 0 {
		t.Fatalf("refresh unrecorded: %d %s", w.Code, w.Body)
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

// A result recorded while an apply activates, after its filter was filled and before it went live, is the saved
// head's only: the apply that follows loads the lists, so the new live sets do not stay empty.
func TestAnApplyLoadsTheLists(t *testing.T) {
	b := newFeedBench(t)
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true, Members: []string{"192.0.2.1"}})
	b.list[0].URL = "https://lists.example/v4"
	b.f.schedule(t.Context())
	b.requests()
	// Answered while the live generation still names the old URL.
	newer := []string{"198.51.100.0/24"}
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true, Members: newer})
	before := b.realized
	// It goes live; the same list again changes nothing, but the apply is owed a load.
	b.live = slices.Clone(b.list)
	b.f.apply()
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true, Members: newer})
	if b.realized != before+1 {
		t.Fatalf("realized %d, %d before the apply", b.realized, before)
	}
	// Without a result, the apply asks the run loop for the load.
	b.f.apply()
	<-b.f.applied
	b.f.fill()
	if b.realized != before+2 || b.f.owed.Load() {
		t.Fatalf("realized %d", b.realized)
	}
}

// A load stops with the engine, and stays owed.
func TestAliasLoadStopsWithTheEngine(t *testing.T) {
	b := newFeedBench(t)
	life, stop := context.WithCancel(t.Context())
	b.f.life = life
	stop()
	var got error
	b.f.realize = func(ctx context.Context) error { got = ctx.Err(); return got }
	b.f.owed.Store(true)
	b.f.fill()
	if got == nil || !b.f.owed.Load() {
		t.Fatalf("load ran with %v", got)
	}
}

// The notification is the alias's: raised while any wanted source of it keeps failing, cleared once none does, so a
// warning raised for an old URL goes once the fixed one is live.
func TestAliasNotificationFollowsTheAlias(t *testing.T) {
	b := newFeedBench(t)
	warn, clear := []string{"alias/" + urlAlias + " warning"}, []string{"alias/" + urlAlias + " "}
	for range aliasFailures {
		b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), Error: "status 404"})
	}
	if got := b.notices(); !slices.Equal(got, warn) {
		t.Fatalf("notices %q", got)
	}
	// The fixed URL, saved, answers; the live one still fails, and the warning stays.
	b.list[0].URL = "https://lists.example/v4"
	b.f.schedule(t.Context())
	b.requests()
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true, Members: []string{"192.0.2.1"}})
	if got := b.notices(); len(got) != 0 {
		t.Fatalf("cleared while the live source fails: %q", got)
	}
	// Applied, the old source is no longer wanted: the warning is cleared, once.
	b.live = slices.Clone(b.list)
	b.f.schedule(t.Context())
	b.f.schedule(t.Context())
	if got := b.notices(); !slices.Equal(got, clear) {
		t.Fatalf("notices %q", got)
	}
	// The new source fails in turn, then answers: raised, then cleared.
	for range aliasFailures {
		b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), Error: "timeout"})
	}
	b.answer(urlAlias, aliasfeed.Result{Request: b.ask(urlAlias), OK: true})
	if got := b.notices(); !slices.Equal(got, append(warn, clear...)) {
		t.Fatalf("notices %q", got)
	}
}

// A result whose source cannot be told, the store failing, is dropped rather than tried again: it would hold up every
// later result. The next schedule asks again.
func TestAliasResultIsDroppedWhenTheStoreFails(t *testing.T) {
	b := newFeedBench(t)
	req := b.ask(urlAlias)
	b.f.aliases = func(context.Context) ([]config.DynamicAlias, []config.DynamicAlias, error) {
		return nil, nil, fmt.Errorf("store fenced")
	}
	if s := b.answer(urlAlias, aliasfeed.Result{Request: req, OK: true, Members: []string{"192.0.2.1"}}); len(s.Members) != 0 || len(s.Pending) != 1 {
		t.Fatalf("state %+v", s)
	}
}

// Host names match whatever their case: in a result, in the kept state, and in the status.
func TestHostNamesMatchWhateverTheirCase(t *testing.T) {
	b := newFeedBench(t)
	b.list[1].Names = []string{"A.Example", "b.example"}
	b.live = slices.Clone(b.list)
	s := b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), OK: true, Names: map[string]aliasfeed.NameResult{
		"a.example": {Addresses: []string{"192.0.2.1"}}, "B.EXAMPLE": {Addresses: []string{"192.0.2.2"}}}})
	if !slices.Equal(s.Members, []string{"192.0.2.1", "192.0.2.2"}) || s.Failures != 0 {
		t.Fatalf("state %+v", s)
	}
	// Spelled otherwise, the names are the same source, and keep what they resolved to.
	b.list[1].Names = []string{"a.example", "B.example"}
	b.live = slices.Clone(b.list)
	s = b.answer(hostAlias, aliasfeed.Result{Request: b.ask(hostAlias), Names: map[string]aliasfeed.NameResult{
		"a.example": {Error: "NXDOMAIN"}, "b.example": {Addresses: []string{"192.0.2.3"}}}})
	if !slices.Equal(s.Members, []string{"192.0.2.1", "192.0.2.3"}) || s.Names[0].Name != "a.example" || s.Names[1].Name != "B.example" {
		t.Fatalf("state %+v", s)
	}
	// Spelled otherwise again, before any result: the status finds what each name resolved to.
	b.list[1].Names = []string{"A.EXAMPLE", "b.example"}
	w := httptest.NewRecorder()
	aliasesAPI{feeds: b.f}.status(w, httptest.NewRequest(http.MethodGet, aliasesPath+"/status", nil))
	if !strings.Contains(w.Body.String(), `{"name":"A.EXAMPLE","addresses":["192.0.2.1"],"error":"NXDOMAIN"`) {
		t.Fatalf("status %s", w.Body)
	}
}

// Once the saved source's request is out, a refresh stands even if the live source's cannot be sent.
func TestAliasRefreshStandsOnceTheSavedSourceIsAsked(t *testing.T) {
	b := newFeedBench(t)
	b.list[0].URL = "https://lists.example/v4"
	sent := 0
	b.f.publish = func(topic string, payload []byte, durable bool) error {
		if sent++; sent > 1 {
			return fmt.Errorf("bus closed")
		}
		b.sent = append(b.sent, published{topic, payload, durable})
		return nil
	}
	first, ok, err := b.f.refresh(t.Context(), urlAlias)
	if got := b.requests(); err != nil || !ok || len(got) != 1 || got[0].ID != first || got[0].Hash != b.src(0).hash {
		t.Fatalf("requests %+v %v", got, err)
	}
	if again, _, err := b.f.refresh(t.Context(), urlAlias); again != first || err != nil || sent != 2 {
		t.Fatalf("not limited: %s %v", again, err)
	}
}

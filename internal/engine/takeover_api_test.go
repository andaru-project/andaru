package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
)

const labMAC = "bc:24:11:89:01:10"

// takeoverBench is the lab VM as the proposal sees it.
type takeoverBench struct {
	root, state string
	head        string
	baseline    host.Baseline
	addresses   []host.AddressStatus
	drafted     []config.Changes
	reads       int
}

// netplanFixture locates the fixture beside this file: the root suite runs the test binary from the repository root.
func netplanFixture(t *testing.T, name string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	return filepath.Join(filepath.Dir(file), "../host/testdata/netplan", name)
}

func newTakeoverBench(t *testing.T, fixture string) *takeoverBench {
	return &takeoverBench{
		root: netplanFixture(t, fixture), state: t.TempDir(),
		head:     `{"schemaVersion":1,"links":[],"segments":[],"zones":[]}`,
		baseline: host.Baseline{Netplan: true, BootUnit: true},
		addresses: []host.AddressStatus{
			{Family: "ipv4", Address: "10.0.1.19", PrefixLength: 16, Scope: "global"},
			{Family: "ipv6", Address: "2a02:a466:79a7:beef:be24:11ff:fe89:110", PrefixLength: 64, Scope: "global"},
		},
	}
}

func (b *takeoverBench) handler() http.Handler {
	read := hostReader{
		Root: b.root,
		Links: func(context.Context) ([]host.LinkStatus, error) {
			b.reads++
			return []host.LinkStatus{{IfIndex: 2, Name: "eth0", LinkType: "ether", MTU: 1500, LinkAddress: labMAC, PermanentAddress: labMAC, Addresses: b.addresses}}, nil
		},
		Facts: func() (host.RealizerFacts, error) {
			return host.RealizerFacts{ForeignFiles: []string{"/run/systemd/network/10-netplan-eth0.link", "/run/systemd/network/10-netplan-eth0.network"}, ResolvedInCharge: true}, nil
		},
		Managed: func(context.Context) ([]host.Managed, error) {
			return []host.Managed{{Name: "eth0", NetworkFile: "/run/systemd/network/10-netplan-eth0.network", State: "configured"}}, nil
		},
		Baseline: func() (host.Baseline, error) { return b.baseline, nil },
		Route:    func(context.Context) (string, error) { return "eth0", nil },
	}
	head := func(context.Context, *config.Version) (config.RealizationInput, error) {
		b.reads++
		return config.RealizationInput{SavedVersion: "9", Value: json.RawMessage(b.head)}, nil
	}
	draft := func(_ context.Context, c config.Changes) (config.RealizationInput, error) {
		b.drafted = append(b.drafted, c)
		return config.RealizationInput{SavedVersion: "9", Value: json.RawMessage(draftValue)}, nil
	}
	live := newLiveHandler(http.NotFoundHandler(), newTakeoverHandler(head, draft, read, b.state))
	return newHandler(read.Links, newSavedHandler(nil, read.Links, read.Facts, live))
}

// draftValue is what the store makes of the static proposal.
const draftValue = `{"schemaVersion":1,
"links":[{"id":"lnk_00000001-0000-4000-8000-000000000001","name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"bc:24:11:89:01:10"}],
"segments":[{"id":"seg_00000001-0000-4000-8000-000000000001","name":"eth0","link":"lnk_00000001-0000-4000-8000-000000000001","zone":"zon_00000001-0000-4000-8000-000000000001",
"addressing":{"ipv4":{"mode":"static","addresses":["10.0.1.19/16"],"gateway":"10.0.0.1"},"ipv6":{"mode":"slaac"},"dns":{"servers":["172.16.53.53"]}}}],
"zones":[{"id":"zon_00000001-0000-4000-8000-000000000001","name":"wan"}]}`

type proposalReply struct {
	State        string            `json:"state"`
	SavedVersion string            `json:"savedVersion"`
	Operations   []json.RawMessage `json:"operations"`
	Adapters     []struct{ Name, PermanentMac, Summary string }
	Findings     []struct{ Severity, Code, Path, Message string }
}

func (b *takeoverBench) proposal(t *testing.T) proposalReply {
	t.Helper()
	w := httptest.NewRecorder()
	b.handler().ServeHTTP(w, rootRequest(http.MethodGet, "/api/v1/takeover/proposal"))
	var reply proposalReply
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if reply.Operations == nil || reply.Findings == nil || reply.Adapters == nil {
		t.Fatalf("lists must be arrays: %s", w.Body)
	}
	return reply
}

func sameJSON(t *testing.T, got []json.RawMessage, want string) {
	t.Helper()
	var a, b any
	raw, _ := json.Marshal(got)
	_ = json.Unmarshal(raw, &a)
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("operations = %s\nwant %s", raw, want)
	}
}

func TestProposalForTheLabBases(t *testing.T) {
	b := newTakeoverBench(t, "static")
	got := b.proposal(t)
	if got.State != "ready" || got.SavedVersion != "9" || len(got.Findings) != 0 {
		t.Fatalf("proposal = %+v", got)
	}
	sameJSON(t, got.Operations, `[
		{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},
		{"op":"create","collection":"links","localId":"link1","value":{"name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"bc:24:11:89:01:10"}},
		{"op":"create","collection":"segments","localId":"seg1","value":{"name":"eth0","link":"@link1","zone":"@wan","addressing":{
			"ipv4":{"mode":"static","addresses":["10.0.1.19/16"],"gateway":"10.0.0.1"},"ipv6":{"mode":"slaac"},
			"dns":{"servers":["172.16.53.53","fd00:53:53:53:53:53:53:53"],"searchDomains":["lab.example"]}}}}]`)
	if len(got.Adapters) != 1 || got.Adapters[0].Name != "eth0" || got.Adapters[0].PermanentMac != labMAC || !strings.Contains(got.Adapters[0].Summary, "static 10.0.1.19/16") || !strings.Contains(got.Adapters[0].Summary, "default route") {
		t.Fatalf("adapters = %+v", got.Adapters)
	}

	b = newTakeoverBench(t, "dhcp")
	b.addresses = b.addresses[:1]
	got = b.proposal(t)
	sameJSON(t, got.Operations, `[
		{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},
		{"op":"create","collection":"links","localId":"link1","value":{"name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"bc:24:11:89:01:10"}},
		{"op":"create","collection":"segments","localId":"seg1","value":{"name":"eth0","link":"@link1","zone":"@wan","addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"none"}}}}]`)
}

func TestProposalStates(t *testing.T) {
	b := newTakeoverBench(t, "static")
	b.head = `{"schemaVersion":1,"links":[{"id":"lnk_00000001-0000-4000-8000-000000000001","name":"x","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"02:00:00:00:00:01"}],"segments":[],"zones":[]}`
	if got := b.proposal(t); got.State != "saved-not-applied" || len(got.Operations) != 0 {
		t.Fatalf("proposal = %+v", got)
	}
	// A saved zone alone is a hand-made object too; the proposal's zones could collide with it.
	b.head = `{"schemaVersion":1,"links":[],"segments":[],"zones":[{"id":"zon_00000001-0000-4000-8000-000000000001","name":"wan"}]}`
	if got := b.proposal(t); got.State != "saved-not-applied" || len(got.Operations) != 0 {
		t.Fatalf("zones only: proposal = %+v", got)
	}

	for _, pointers := range []string{
		`{"confirmed":1,"lastKnownGood":null,"pending":null,"live":1,"takeover":true}`,
		`{"confirmed":null,"lastKnownGood":null,"pending":1,"live":1}`,
	} {
		b = newTakeoverBench(t, "static")
		if err := os.MkdirAll(filepath.Join(b.state, "generations"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(b.state, "generations", "pointers.json"), []byte(pointers), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := b.proposal(t); got.State != "taken-over" || len(got.Operations) != 0 {
			t.Fatalf("%s: proposal = %+v", pointers, got)
		}
	}

	b = newTakeoverBench(t, "static")
	b.baseline = host.Baseline{Ifupdown: true}
	got := b.proposal(t)
	if got.State != "unsupported" || len(got.Operations) != 0 || len(got.Findings) == 0 || !strings.Contains(got.Findings[0].Message, "later release") {
		t.Fatalf("ifupdown = %+v", got)
	}

	b = newTakeoverBench(t, "static")
	b.root = t.TempDir()
	dir := filepath.Join(b.root, "run/systemd/network")
	_ = os.MkdirAll(dir, 0o755)
	base, _ := os.ReadFile("../host/testdata/netplan/static/run/systemd/network/10-netplan-eth0.network")
	_ = os.WriteFile(filepath.Join(dir, "10-netplan-eth0.network"), base, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "10-netplan-br0.netdev"), []byte("[NetDev]\nName=br0\n"), 0o644)
	got = b.proposal(t)
	if got.State != "unsupported" || len(got.Operations) != 0 || got.Findings[0].Code != "TAKEOVER_UNSUPPORTED_BASELINE" || got.Findings[0].Severity != "error" || got.Findings[0].Path != "/run/systemd/network/10-netplan-br0.netdev" {
		t.Fatalf("netdev = %+v", got)
	}
}

func TestDraftPreview(t *testing.T) {
	b := newTakeoverBench(t, "static")
	body := `{"operations":[{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/preview/network/draft", strings.NewReader(body)).WithContext(rootRequest(http.MethodPost, "/").Context())
	b.handler().ServeHTTP(w, r)
	var reply struct {
		State, SavedVersion string
		Files               []struct{ Path, Content string }
		ApplyFindings       []struct{ Code string }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || reply.State != "draft" || reply.SavedVersion != "9" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(b.drafted) != 1 || len(b.drafted[0].Operations) != 1 {
		t.Fatalf("drafted = %+v", b.drafted)
	}
	if len(reply.Files) != 1 || !strings.Contains(reply.Files[0].Content, "DNS=172.16.53.53") || reply.ApplyFindings == nil {
		t.Fatalf("reply = %s", w.Body)
	}
	for _, f := range reply.ApplyFindings {
		if f.Code == "TAKEOVER_MISMATCH" {
			t.Fatalf("the draft is judged as a takeover: %s", w.Body)
		}
	}
	for method, want := range map[string]int{http.MethodGet: 405} {
		w := httptest.NewRecorder()
		b.handler().ServeHTTP(w, rootRequest(method, "/api/v1/preview/network/draft"))
		if w.Code != want {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
}

func TestTakeoverRoutesRequireRoot(t *testing.T) {
	b := newTakeoverBench(t, "static")
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/takeover/proposal"}, {http.MethodPost, "/api/v1/preview/network/draft"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		b.handler().ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 1000, verified: true})))
		assertAPIError(t, w, http.StatusForbidden, "FORBIDDEN")
	}
	if b.reads != 0 || len(b.drafted) != 0 {
		t.Fatalf("an unauthorized peer reached the host or store: %d reads", b.reads)
	}
}

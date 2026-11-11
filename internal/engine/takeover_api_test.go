package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/host/fixture"
)

const labMAC = "bc:24:11:89:01:10"

// takeoverBench is the lab VM as the proposal sees it.
type takeoverBench struct {
	root, state string
	head        string
	baseline    host.Baseline
	routes      []string
	addresses   []host.AddressStatus
	drafted     []config.Changes
	// The host's one NIC, what networkd binds and what a draft becomes; the netplan lab VM unless changed.
	nic, mac   string
	managed    []host.Managed
	facts      host.RealizerFacts
	draftValue string
	resolved   []string
	reads      int
}

func newTakeoverBench(t *testing.T, name string) *takeoverBench {
	return &takeoverBench{
		root: fixture.Root(t, name), state: t.TempDir(),
		head:     `{"schemaVersion":1,"links":[],"segments":[],"zones":[]}`,
		baseline: host.Baseline{Netplan: true, BootUnit: true},
		routes:   []string{"eth0"},
		nic:      "eth0", mac: labMAC, draftValue: draftValue,
		managed: []host.Managed{{Name: "eth0", NetworkFile: "/run/systemd/network/10-netplan-eth0.network", State: "configured"}},
		facts:   host.RealizerFacts{ForeignFiles: []string{"/run/systemd/network/10-netplan-eth0.link", "/run/systemd/network/10-netplan-eth0.network"}, ResolvedInCharge: true},
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
			return []host.LinkStatus{{IfIndex: 2, Name: b.nic, LinkType: "ether", MTU: 1500, LinkAddress: b.mac, PermanentAddress: b.mac, Addresses: b.addresses}}, nil
		},
		Facts:    func() (host.RealizerFacts, error) { return b.facts, nil },
		Managed:  func(context.Context) ([]host.Managed, error) { return b.managed, nil },
		Baseline: func() (host.Baseline, error) { return b.baseline, nil },
		Route:    func(context.Context) ([]string, error) { return b.routes, nil },
		Resolved: func(context.Context) ([]string, []string, error) { return b.resolved, []string{}, nil },
	}
	head := func(context.Context, *config.Version) (config.RealizationInput, error) {
		b.reads++
		return config.RealizationInput{SavedVersion: "9", Value: json.RawMessage(b.head)}, nil
	}
	draft := func(_ context.Context, c config.Changes) (config.RealizationInput, error) {
		b.drafted = append(b.drafted, c)
		return config.RealizationInput{SavedVersion: "9", Value: json.RawMessage(b.draftValue)}, nil
	}
	live := newLiveHandler(http.NotFoundHandler(), newTakeoverHandler(head, draft, read, b.state))
	return newHandler(read.Links, newSavedHandler(nil, read.Links, read.Facts, nil, live))
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
	Adapters     []struct {
		Name, PermanentMac, Summary string
		Addressed, Required         bool
	}
	Findings []struct{ Severity, Code, Path, Message string }
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
	if len(got.Adapters) != 1 || got.Adapters[0].Name != "eth0" || got.Adapters[0].PermanentMac != labMAC || !strings.Contains(got.Adapters[0].Summary, "static 10.0.1.19/16") || !strings.Contains(got.Adapters[0].Summary, "default route") || !got.Adapters[0].Addressed {
		t.Fatalf("adapters = %+v", got.Adapters)
	}

	b = newTakeoverBench(t, "dhcp")
	b.addresses = nil // unplugged: DHCP configured, no address now
	if got = b.proposal(t); len(got.Adapters) != 1 || got.Adapters[0].Addressed {
		t.Fatalf("unplugged adapter = %+v", got.Adapters)
	}
	b.addresses = []host.AddressStatus{{Family: "ipv4", Address: "10.0.1.19", PrefixLength: 16, Scope: "global"}}
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

	// Two default-route interfaces: which one is the uplink is not known.
	b = newTakeoverBench(t, "dhcp")
	b.routes = []string{"eth0", "eth1"}
	if got := b.proposal(t); got.State != "unsupported" || len(got.Operations) != 0 || !strings.Contains(got.Findings[0].Message, "eth0, eth1") {
		t.Fatalf("two default routes = %+v", got)
	}

	// netplan and ifupdown both configure the host: neither can be taken over alone.
	b = newTakeoverBench(t, "static")
	b.baseline = host.Baseline{Netplan: true, Ifupdown: true}
	got := b.proposal(t)
	if got.State != "unsupported" || len(got.Operations) != 0 || len(got.Findings) != 1 || got.Findings[0].Path != "/sbin/ifup" {
		t.Fatalf("netplan and ifupdown = %+v", got)
	}

	b = newTakeoverBench(t, "static")
	// The bench root is a private copy of the fixture; add netplan's bridge beside it.
	if err := os.WriteFile(filepath.Join(b.root, "run/systemd/network/10-netplan-br0.netdev"), []byte("[NetDev]\nName=br0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if len(reply.Files) != 2 || !strings.Contains(reply.Files[1].Content, "DNS=172.16.53.53") || reply.ApplyFindings == nil {
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

const debianMAC = "bc:24:11:38:71:37"

// newDebianBench is lab VM 117: the Debian installer's ifupdown and dhcpcd, systemd-resolved installed.
func newDebianBench(t *testing.T, name string) *takeoverBench {
	b := newTakeoverBench(t, name)
	b.baseline, b.routes, b.nic, b.mac = host.Baseline{Ifupdown: true, BootUnit: true}, []string{"ens18"}, "ens18", debianMAC
	b.managed, b.facts = []host.Managed{}, host.RealizerFacts{ForeignFiles: []string{}, ResolvedInCharge: true}
	b.addresses = b.addresses[:1]
	b.draftValue = `{"schemaVersion":1,
"links":[{"id":"lnk_00000001-0000-4000-8000-000000000001","name":"ens18","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"` + debianMAC + `"}],
"segments":[{"id":"seg_00000001-0000-4000-8000-000000000001","name":"ens18","link":"lnk_00000001-0000-4000-8000-000000000001","zone":"zon_00000001-0000-4000-8000-000000000001",
"addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"slaac"}}}],
"zones":[{"id":"zon_00000001-0000-4000-8000-000000000001","name":"wan"}]}`
	return b
}

type draftReply struct {
	Files         []struct{ Path, Content string }
	Findings      []struct{ Severity, Code, Message string }
	ApplyFindings []struct{ Code, Path string }
}

func (b *takeoverBench) postDraft(t *testing.T) draftReply {
	t.Helper()
	w := httptest.NewRecorder()
	r := rootRequest(http.MethodPost, "/api/v1/preview/network/draft")
	r.Body = io.NopCloser(strings.NewReader(`{"operations":[{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}}]}`))
	b.handler().ServeHTTP(w, r)
	var reply draftReply
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	return reply
}

func TestDraftShowsTakeoverLimits(t *testing.T) {
	got := newDebianBench(t, "ifupdown-dhcp").postDraft(t)
	if len(got.ApplyFindings) != 0 || len(got.Files) != 2 || !strings.Contains(got.Files[1].Content, "DUIDRawData=00:01:32:87:03:54:bc:24:11:38:71:37") {
		t.Fatalf("draft = %+v", got)
	}
	var limits []string
	for _, f := range got.Findings {
		if f.Code == "TAKEOVER_LIMIT" && f.Severity == "warning" {
			limits = append(limits, f.Message)
		}
	}
	if len(limits) != 2 || !strings.Contains(strings.Join(limits, " "), "SLAAC") || !strings.Contains(strings.Join(limits, " "), "if-up.d") {
		t.Fatalf("limits = %q", limits)
	}
	// The kernel's SLAAC address on a static host is EUI-64, as networkd's: nothing changes.
	b := newDebianBench(t, "ifupdown-static")
	b.addresses = append(b.addresses, host.AddressStatus{Family: "ipv6", Address: "2001:db8::be24:11ff:fe38:7137", PrefixLength: 64, Scope: "global"})
	b.draftValue = strings.Replace(b.draftValue, `"ipv4":{"mode":"dhcp"}`, `"ipv4":{"mode":"static","addresses":["10.0.1.13/16"],"gateway":"10.0.0.1"}`, 1)
	for _, f := range b.postDraft(t).Findings {
		if f.Code == "TAKEOVER_LIMIT" && strings.Contains(f.Message, "SLAAC") {
			t.Fatalf("SLAAC limit on a static host: %s", f.Message)
		}
	}
}

func TestIdentityComesFromTheCapturedBaseline(t *testing.T) {
	b := newDebianBench(t, "ifupdown-dhcp")
	captured := generation.IfupdownBaseline{Interfaces: []host.IfupdownInterface{{Detected: host.Detected{Name: "ens18", PermanentMAC: debianMAC},
		DHCP: true, Identity: &host.DHCPIdentity{DUID: []byte{0, 1, 0xaa, 0xbb, 0xcc}}}}}
	raw, _ := json.Marshal(map[string]any{"kind": "ifupdown", "files": []any{}, "ifupdown": captured})
	dir := filepath.Join(b.state, "generations", "baseline")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// After confirmation ifupdown is masked and dhcpcd's own DUID may have changed or gone.
	b.baseline.IfupdownMasked = true
	got := b.postDraft(t)
	if len(got.Files) != 2 || !strings.Contains(got.Files[1].Content, "DUIDRawData=aa:bb:cc\n") {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestProposalForIfupdown(t *testing.T) {
	got := newDebianBench(t, "ifupdown-dhcp").proposal(t)
	if got.State != "ready" || len(got.Adapters) != 1 || !got.Adapters[0].Required || !got.Adapters[0].Addressed {
		t.Fatalf("dhcp = %+v", got)
	}
	sameJSON(t, got.Operations, `[
		{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},
		{"op":"create","collection":"links","localId":"link1","value":{"name":"ens18","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"`+debianMAC+`"}},
		{"op":"create","collection":"segments","localId":"seg1","value":{"name":"ens18","link":"@link1","zone":"@wan","addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"slaac"}}}}]`)

	// A static install takes no DNS from anywhere: the servers in use now are proposed.
	b := newDebianBench(t, "ifupdown-static")
	b.resolved = []string{"192.0.2.53", "2001:db8::53"}
	sameJSON(t, b.proposal(t).Operations, `[
		{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},
		{"op":"create","collection":"links","localId":"link1","value":{"name":"ens18","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"`+debianMAC+`"}},
		{"op":"create","collection":"segments","localId":"seg1","value":{"name":"ens18","link":"@link1","zone":"@wan","addressing":{"ipv4":{"mode":"static","addresses":["10.0.1.13/16"],"gateway":"10.0.0.1"},"ipv6":{"mode":"none"},"dns":{"servers":["192.0.2.53","2001:db8::53"]}}}}]`)

	b = newDebianBench(t, "ifupdown-dhcp")
	b.facts.ResolvedInCharge = false
	if got := b.proposal(t); got.State != "unsupported" || len(got.Operations) != 0 || !slices.ContainsFunc(got.Findings, func(f struct{ Severity, Code, Path, Message string }) bool { return f.Code == "RESOLVED_NOT_IN_CHARGE" }) {
		t.Fatalf("resolved not in charge = %+v", got)
	}
}

// The kernel's own SLAAC address says nothing about DNS: router advertisements may carry no servers.
func TestProposalPrefillsDNSBesideKernelSLAAC(t *testing.T) {
	b := newDebianBench(t, "ifupdown-static")
	b.addresses = append(b.addresses, host.AddressStatus{Family: "ipv6", Address: "2001:db8::be24:11ff:fe38:7137", PrefixLength: 64, Scope: "global"})
	b.resolved = []string{"192.0.2.53"}
	raw, _ := json.Marshal(b.proposal(t).Operations)
	if !strings.Contains(string(raw), `"ipv6":{"mode":"slaac"}`) || !strings.Contains(string(raw), `"dns":{"servers":["192.0.2.53"]}`) {
		t.Fatalf("operations = %s", raw)
	}
}

package engine

import (
	"context"
	"encoding/json"
	"fmt"
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
			"dns":{"servers":["172.16.53.53","fd00:53:53:53:53:53:53:53"],"searchDomains":["lab.example"]}}}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"@seg1"}}]`)
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
		{"op":"create","collection":"segments","localId":"seg1","value":{"name":"eth0","link":"@link1","zone":"@wan","addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"none"}}}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"@seg1"}}]`)
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
	// Saved filter sections alone are a saved configuration: ready would activate saved rules or fail on expectedVersion 0.
	b.head = `{"schemaVersion":1,"links":[],"segments":[],"zones":[],"nat":{"outbound":{"mode":"none"}}}`
	if got := b.proposal(t); got.State != "saved-not-applied" || len(got.Operations) != 0 {
		t.Fatalf("nat only: proposal = %+v", got)
	}
	b.head = `{"schemaVersion":1,"links":[],"segments":[],"zones":[],"dns":{"mode":"recursive","segments":[]}}`
	if got := b.proposal(t); got.State != "saved-not-applied" || len(got.Operations) != 0 {
		t.Fatalf("dns only: proposal = %+v", got)
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
	Realizable    bool
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

func TestIdentityComesFromTheConfirmedCapture(t *testing.T) {
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
	// A failed takeover leaves its capture behind while ifupdown still runs the host: what dhcpcd presents now wins.
	if got := b.postDraft(t); len(got.Files) != 2 || !strings.Contains(got.Files[1].Content, "DUIDRawData=00:01:32:87:03:54:bc:24:11:38:71:37\n") {
		t.Fatalf("stale capture rendered: %+v", got.Files)
	}
	// The saved-version preview judges the capture the same way.
	read := hostReader{Root: b.root, Baseline: func() (host.Baseline, error) { return b.baseline, nil },
		Route: func(context.Context) ([]string, error) { return b.routes, nil }}
	ids, err := previewIdentities(read, filepath.Join(b.state, "generations"))(t.Context(), []host.LinkStatus{{IfIndex: 2, Name: b.nic, LinkType: "ether", MTU: 1500, LinkAddress: b.mac, PermanentAddress: b.mac, Addresses: b.addresses}})
	if err != nil {
		t.Fatal(err)
	}
	if live, ok := ids[debianMAC]; !ok || fmt.Sprintf("% x", live.DUID) != "00 01 00 01 32 87 03 54 bc 24 11 38 71 37" {
		t.Fatalf("preview did not use dhcpcd's live identity: %+v", ids)
	}
	// After confirmation ifupdown is masked and dhcpcd's own DUID may have changed or gone.
	b.baseline.IfupdownMasked = true
	if err := os.WriteFile(filepath.Join(b.state, "generations", "pointers.json"), []byte(`{"confirmed":null,"lastKnownGood":null,"pending":null,"live":null,"takeover":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
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
		{"op":"create","collection":"segments","localId":"seg1","value":{"name":"ens18","link":"@link1","zone":"@wan","addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"slaac"}}}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"@seg1"}}]`)

	// A static install takes no DNS from anywhere: the servers in use now are proposed.
	b := newDebianBench(t, "ifupdown-static")
	b.resolved = []string{"192.0.2.53", "2001:db8::53"}
	sameJSON(t, b.proposal(t).Operations, `[
		{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},
		{"op":"create","collection":"links","localId":"link1","value":{"name":"ens18","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"`+debianMAC+`"}},
		{"op":"create","collection":"segments","localId":"seg1","value":{"name":"ens18","link":"@link1","zone":"@wan","addressing":{"ipv4":{"mode":"static","addresses":["10.0.1.13/16"],"gateway":"10.0.0.1"},"ipv6":{"mode":"none"},"dns":{"servers":["192.0.2.53","2001:db8::53"]}}}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"@seg1"}}]`)

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

// filterDraft is the static lab proposal with a starter rule, outbound NAT and management on eth0.
const filterDraft = `{"schemaVersion":1,
"links":[{"id":"lnk_00000001-0000-4000-8000-000000000001","name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"bc:24:11:89:01:10"}],
"segments":[{"id":"seg_00000001-0000-4000-8000-000000000001","name":"eth0","link":"lnk_00000001-0000-4000-8000-000000000001","zone":"zon_00000001-0000-4000-8000-000000000001",
"addressing":{"ipv4":{"mode":"static","addresses":["10.0.1.19/16"],"gateway":"10.0.0.1"},"ipv6":{"mode":"slaac"},"dns":{"servers":["172.16.53.53"]}}}],
"zones":[{"id":"zon_00000001-0000-4000-8000-000000000001","name":"wan"}],
"objects":{"addresses":[],"services":[{"id":"svc_00000001-0000-4000-8000-000000000001","name":"ping","protocol":"icmp","types":["echo-request"]}]},
"policy":{"input":[{"id":"rul_00000001-0000-4000-8000-000000000001","name":"ping","enabled":true,"action":"accept","from":["zon_00000001-0000-4000-8000-000000000001"],"to":[],"services":["svc_00000001-0000-4000-8000-000000000001"],"log":false}],"forward":[]},
"nat":{"outbound":{"mode":"automatic"}},
"management":{"segment":"seg_00000001-0000-4000-8000-000000000001"}}`

func TestFilterGates(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(b *takeoverBench)
		code string
	}{
		"ufw.service enabled": {func(b *takeoverBench) { b.facts.FilterUnits = []string{"ufw.service"} }, "FOREIGN_FILTER"},
		// Only a takeover's confirmation masks nftables.service; any other apply leaves it to flush Andaru's filter.
		"nftables.service enabled without a takeover": {func(b *takeoverBench) {
			b.facts.FilterUnits = []string{"nftables.service"}
			if err := os.MkdirAll(filepath.Join(b.state, "generations"), 0o700); err != nil {
				panic(err)
			}
			if err := os.WriteFile(filepath.Join(b.state, "generations", "pointers.json"), []byte(`{"confirmed":1,"lastKnownGood":null,"pending":null,"live":null,"takeover":true}`), 0o600); err != nil {
				panic(err)
			}
		}, "FOREIGN_FILTER"},
		"nft missing": {func(b *takeoverBench) { b.facts.NftTool = false }, "TOOL_MISSING"},
		"no management": {func(b *takeoverBench) {
			b.draftValue = strings.Replace(b.draftValue, `,
"management":{"segment":"seg_00000001-0000-4000-8000-000000000001"}`, "", 1)
		}, "MANAGEMENT_REQUIRED"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newTakeoverBench(t, "static")
			b.draftValue, b.facts.NftTool = filterDraft, true
			tc.edit(b)
			got := b.postDraft(t)
			if !slices.ContainsFunc(got.ApplyFindings, func(f struct{ Code, Path string }) bool { return f.Code == tc.code }) {
				t.Fatalf("apply findings %+v", got.ApplyFindings)
			}
		})
	}
}

// Confirming a takeover masks nftables.service, so an enabled one does not refuse the takeover; the review says so.
func TestTakeoverMasksNftables(t *testing.T) {
	b := newTakeoverBench(t, "static")
	b.draftValue, b.facts.NftTool = filterDraft, true
	b.facts.FilterUnits = []string{"nftables.service"}
	got := b.postDraft(t)
	if len(got.ApplyFindings) != 0 {
		t.Fatalf("apply findings %+v", got.ApplyFindings)
	}
	if !slices.ContainsFunc(got.Findings, func(f struct{ Severity, Code, Message string }) bool {
		return f.Severity == "warning" && f.Code == "FOREIGN_FILTER" && strings.Contains(f.Message, "masks it")
	}) {
		t.Fatalf("findings %+v", got.Findings)
	}
}

// On a host with nothing else in the way, the takeover's draft stays realizable with nftables.service enabled.
func TestTakeoverWithNftablesIsRealizable(t *testing.T) {
	b := newDebianBench(t, "ifupdown-dhcp")
	b.facts.NftTool, b.facts.FilterUnits = true, []string{"nftables.service"}
	b.draftValue = strings.TrimSuffix(b.draftValue, "}") + `,
"management":{"segment":"seg_00000001-0000-4000-8000-000000000001"}}`
	got := b.postDraft(t)
	if len(got.ApplyFindings) != 0 || !got.Realizable {
		t.Fatalf("realizable %v, apply findings %+v, findings %+v", got.Realizable, got.ApplyFindings, got.Findings)
	}
}

func TestDraftShowsRules(t *testing.T) {
	b := newTakeoverBench(t, "static")
	b.draftValue, b.facts.NftTool = filterDraft, true
	w := httptest.NewRecorder()
	r := rootRequest(http.MethodPost, "/api/v1/preview/network/draft")
	r.Body = io.NopCloser(strings.NewReader(`{"operations":[{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}}]}`))
	b.handler().ServeHTTP(w, r)
	var got struct {
		Rules []struct {
			Chain, Name, Action string
			Position            int
			From, To, Services  []string
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Rules) != 1 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	row := got.Rules[0]
	if row.Chain != "input" || row.Position != 1 || row.Name != "ping" || row.Action != "accept" || !slices.Equal(row.From, []string{"wan"}) || !slices.Equal(row.Services, []string{"ping"}) || row.To == nil {
		t.Fatalf("row %+v", row)
	}
}

// twoNICs is the lab VM with a second NIC: eth0 the static uplink, eth1 a DHCP-less LAN port.
func twoNICs() []host.Detected {
	return []host.Detected{
		{Name: "eth0", PermanentMAC: labMAC, MTU: 1500, DefaultRoute: true, IPv4: host.Addressing{Mode: "static", Addresses: []string{"10.0.1.19/16"}, Gateway: "10.0.0.1"}, IPv6: host.Addressing{Mode: "slaac"}},
		{Name: "eth1", PermanentMAC: "bc:24:11:89:01:11", MTU: 1500, IPv4: host.Addressing{Mode: "static", Addresses: []string{"192.168.40.1/24"}}, IPv6: host.Addressing{Mode: "none"}},
	}
}

func TestProposalStarterRules(t *testing.T) {
	ops0, dhcp0 := proposalOperations(twoNICs(), "eth0", host.RealizerFacts{})
	raw, _ := json.Marshal(ops0)
	var ops []json.RawMessage
	_ = json.Unmarshal(raw, &ops)
	sameJSON(t, ops, `[
		{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},
		{"op":"create","collection":"zones","localId":"lan","value":{"name":"lan"}},
		{"op":"create","collection":"links","localId":"link1","value":{"name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"bc:24:11:89:01:10"}},
		{"op":"create","collection":"links","localId":"link2","value":{"name":"eth1","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"bc:24:11:89:01:11"}},
		{"op":"create","collection":"segments","localId":"seg1","value":{"name":"eth0","link":"@link1","zone":"@wan","addressing":{"ipv4":{"mode":"static","addresses":["10.0.1.19/16"],"gateway":"10.0.0.1"},"ipv6":{"mode":"slaac"}}}},
		{"op":"create","collection":"segments","localId":"seg2","value":{"name":"eth1","link":"@link2","zone":"@lan","addressing":{"ipv4":{"mode":"static","addresses":["192.168.40.1/24"]},"ipv6":{"mode":"none"}}}},
		{"op":"create","collection":"services","localId":"ping","value":{"name":"ping","protocol":"icmp","types":["echo-request"]}},
		{"op":"create","collection":"services","localId":"ping6","value":{"name":"ping6","protocol":"icmpv6","types":["echo-request"]}},
		{"op":"create","collection":"services","localId":"dnsUdp","value":{"name":"dns-udp","protocol":"udp","ports":["53"]}},
		{"op":"create","collection":"services","localId":"dnsTcp","value":{"name":"dns-tcp","protocol":"tcp","ports":["53"]}},
		{"op":"create","collection":"services","localId":"dns","value":{"name":"dns","protocol":"group","members":["@dnsUdp","@dnsTcp"]}},
		{"op":"create","collection":"policy.input","localId":"lanToFirewall","value":{"name":"lan to firewall: ping and DNS","enabled":true,"action":"accept","from":["@lan"],"to":[],"services":["@ping","@ping6","@dns"],"log":false}},
		{"op":"create","collection":"policy.forward","localId":"lanOut","value":{"name":"lan to any","enabled":true,"action":"accept","from":["@lan"],"to":[],"services":[],"log":false}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"@seg1"}}]`)

	// Without Kea and Unbound installed the takeover proposes no DNS service and no DHCP server.
	if dhcp0 != nil || strings.Contains(string(raw), `"collection":"dns"`) {
		t.Fatalf("services proposed without their daemons: %s %v", raw, dhcp0)
	}
	// From the serial console: the LAN segment is the management one.
	ops0, _ = proposalOperations(twoNICs(), "", host.RealizerFacts{})
	raw, _ = json.Marshal(ops0)
	if !strings.Contains(string(raw), `"value":{"segment":"@seg2"}`) {
		t.Fatalf("management without a session: %s", raw)
	}
}

// With both daemons installed the LAN gets the DNS service, which takes over the host's servers, and a DHCP offer.
func TestProposalServesTheLAN(t *testing.T) {
	nics := twoNICs()
	nics[0].DNS, nics[0].Domains = []string{"172.16.53.53"}, []string{"lab.example"}
	ops, dhcp := proposalOperations(nics, "eth0", host.RealizerFacts{KeaTool: true, UnboundTool: true})
	raw, _ := json.Marshal(ops)
	if !strings.Contains(string(raw), `{"op":"replace","collection":"dns","expectedVersion":"0","value":{"mode":"forward","searchDomains":["lab.example"],"segments":["@seg2"],"upstreams":[{"address":"172.16.53.53","protocol":"do53"}]}}`) || strings.Contains(string(raw), `"servers"`) {
		t.Fatalf("dns: %s", raw)
	}
	offer, _ := json.Marshal(dhcp)
	if string(offer) != `[{"op":"create","collection":"dhcp","localId":"dhcp2","value":{"name":"eth1","ranges":[{"first":"192.168.40.128","last":"192.168.40.254"}],"segment":"@seg2"}}]` {
		t.Fatalf("dhcp offer %s", offer)
	}
	// A host that learned its servers from DHCP gets a recursive resolver: those servers change.
	nics[0].DNS = nil
	ops, _ = proposalOperations(nics, "eth0", host.RealizerFacts{KeaTool: true, UnboundTool: true})
	raw, _ = json.Marshal(ops)
	if !strings.Contains(string(raw), `"value":{"mode":"recursive","searchDomains":["lab.example"],"segments":["@seg2"]}`) {
		t.Fatalf("dns: %s", raw)
	}
}

// A /30 LAN has no room for a DHCP range, but the DNS service needs only its static address.
func TestProposalServesDNSOnASmallLAN(t *testing.T) {
	nics := twoNICs()
	nics[1].IPv4.Addresses = []string{"192.168.40.1/30"}
	ops, dhcp := proposalOperations(nics, "eth0", host.RealizerFacts{KeaTool: true, UnboundTool: true})
	raw, _ := json.Marshal(ops)
	if !strings.Contains(string(raw), `"collection":"dns","expectedVersion":"0","value":{"mode":"recursive","segments":["@seg2"]}`) {
		t.Fatalf("dns: %s", raw)
	}
	if len(dhcp) != 0 {
		t.Fatalf("dhcp offer on a /30: %+v", dhcp)
	}
}

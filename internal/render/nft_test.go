package render

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/aliasfeed"
)

func nftFile(t *testing.T, r Result) (File, bool) {
	t.Helper()
	for _, f := range r.Files {
		if f.Path == nftPath {
			return f, true
		}
	}
	return File{}, false
}

// skipMissingTool skips a test whose tool is absent, except where CI installed every tool on purpose and set
// ANDARU_TEST_REQUIRE_TOOLS=1: there a skip would hide a broken check.
func skipMissingTool(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("ANDARU_TEST_REQUIRE_TOOLS") == "1" {
		t.Fatal(why)
	}
	t.Skip(why)
}

// Every rendered filter must be a script nft accepts; checking needs root and nft, which CI's root suites have.
func TestNftScriptIsValid(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("nft -c needs root")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		skipMissingTool(t, "nft not installed")
	}
	names, _ := fs.Glob(testdata, "testdata/filter-*.links.json")
	if len(names) == 0 {
		t.Fatal("no filter fixtures")
	}
	for _, path := range names {
		name := strings.TrimSuffix(filepath.Base(path), ".links.json")
		f, ok := nftFile(t, render(t, fixture(t, name)))
		if !ok {
			t.Fatalf("%s rendered no filter", name)
		}
		script := filepath.Join(t.TempDir(), "andaru.nft")
		if err := os.WriteFile(script, []byte(f.Content), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("nft", "-c", "-f", script).CombinedOutput(); err != nil {
			t.Fatalf("%s: nft refused the script: %s\n%s", name, out, f.Content)
		}
	}
}

// Without a management segment the filter is off: the files are exactly today's and nothing forwards.
func TestNoFilterWithoutManagement(t *testing.T) {
	plain := render(t, fixture(t, "ethernet-uplink"))
	in := change(t, fixture(t, "filter-two-zones"), func(d map[string]any) {
		delete(d, "management")
		delete(d, "rules")
		delete(d, "nat")
		delete(d, "aliases")
	})
	got := render(t, in)
	if golden(got) != golden(plain) {
		t.Fatalf("output changed without a filter:\n%s", golden(got))
	}
	for _, f := range got.Files {
		if strings.Contains(f.Content, "Forwarding=") {
			t.Fatalf("%s forwards without a filter", f.Path)
		}
	}
	// Rules without a management segment are refused, not silently dropped.
	refused := render(t, change(t, fixture(t, "filter-two-zones"), func(d map[string]any) { delete(d, "management") }))
	if refused.Realizable || !hasFinding(refused, "error", "MANAGEMENT_REQUIRED", "/management") {
		t.Fatalf("findings %+v", refused.Findings)
	}
}

// After a takeover the kernel name changes (eth0 becomes ens18); the rules follow the link, not the old name.
func TestManagementFollowsTheLinkName(t *testing.T) {
	in := fixture(t, "filter-two-zones")
	for i := range in.Links {
		in.Links[i].Name = strings.Replace(in.Links[i].Name, "ens", "enp0s", 1)
	}
	f, _ := nftFile(t, render(t, in))
	if !strings.Contains(f.Content, `iifname "enp0s19" tcp dport 22 accept`) {
		t.Fatalf("management rule does not use the current name:\n%s", f.Content)
	}
}

func TestFilterFindings(t *testing.T) {
	in := fixture(t, "filter-two-zones")
	in.Facts.NftTool = false
	if r := render(t, in); !hasFinding(r, "error", "TOOL_MISSING", "") || r.Realizable {
		t.Fatalf("missing nft: %+v", r.Findings)
	}
	in = fixture(t, "filter-two-zones")
	in.Facts.FilterUnits = []string{"nftables.service"}
	if r := render(t, in); !hasFinding(r, "error", "FOREIGN_FILTER", "nftables.service") {
		t.Fatalf("foreign filter: %+v", r.Findings)
	}
	// Forwarding is switched on together with the filter, on every segment's file.
	for _, f := range render(t, fixture(t, "filter-two-zones")).Files {
		if strings.HasSuffix(f.Path, ".network") && f.Path != UnclassifiedPath && !strings.Contains(f.Content, "IPv4Forwarding=yes\nIPv6Forwarding=yes\n") {
			t.Fatalf("%s does not forward:\n%s", f.Path, f.Content)
		}
	}
}

// A PPPoE session that is not rendered has no interface whose handshakes need clamping.
func TestNoClampForAnUnusableSession(t *testing.T) {
	clamps := func(in Input) int {
		f, _ := nftFile(t, render(t, in))
		return strings.Count(f.Content, "maxseg size set")
	}
	if n := clamps(fixture(t, "filter-pppoe")); n != 4 {
		t.Fatalf("%d clamps for one session, want 4", n)
	}
	in := change(t, fixture(t, "filter-pppoe"), func(d map[string]any) { item(d, "links", 0)["enabled"] = false })
	if n := clamps(in); n != 0 {
		t.Fatalf("%d clamps for a session whose parent is disabled", n)
	}
}

// An alias fed from a URL or from host names declares both family sets, empty, with its source's hash in their
// comment, and a rule naming it renders a line for each family, whatever its fetched list will hold.
func TestDynamicAliasRendersBothEmptySets(t *testing.T) {
	const id, set, rule = "adr_00000009-0000-4000-8000-000000000009", "adr_00000009_0000_4000_8000_000000000009", "rul_00000009-0000-4000-8000-000000000009"
	hashes := map[string]string{"url": aliasfeed.Hash("url", "https://www.cloudflare.com/ips-v4", nil),
		"hostnames": aliasfeed.Hash("hostnames", "", []string{"VPN.example.com", "a.example"})}
	for kind, fields := range map[string]map[string]any{
		"url":       {"url": "https://www.cloudflare.com/ips-v4"},
		"hostnames": {"names": []any{"a.example", "vpn.example.com"}},
	} {
		t.Run(kind, func(t *testing.T) {
			in := change(t, fixture(t, "filter-objects"), func(d map[string]any) {
				alias := map[string]any{"id": id, "name": "dynamic", "kind": kind, "refreshSeconds": 300}
				for k, v := range fields {
					alias[k] = v
				}
				aliases := d["aliases"].(map[string]any)
				aliases["addresses"] = append(aliases["addresses"].([]any), alias)
				d["rules"] = append(d["rules"].([]any), map[string]any{"id": rule, "name": "from dynamic", "enabled": true, "action": "accept",
					"from": map[string]any{"zones": []any{}, "addresses": []any{id}}, "to": map[string]any{"zones": []any{}, "addresses": []any{}}, "ports": []any{}, "log": false})
			})
			f, ok := nftFile(t, render(t, in))
			if !ok {
				t.Fatal("no filter rendered")
			}
			lines(t, f.Content,
				"\tset "+set+"_4 { type ipv4_addr; flags interval; auto-merge; comment \"feed:"+hashes[kind]+"\"; }",
				"\tset "+set+"_6 { type ipv6_addr; flags interval; auto-merge; comment \"feed:"+hashes[kind]+"\"; }",
				"\t\tip saddr @"+set+"_4 accept comment \""+rule+"\"",
				"\t\tip6 saddr @"+set+"_6 accept comment \""+rule+"\"")
		})
	}
}

func TestInlineLiteralsRender(t *testing.T) {
	const (
		ruleAllow = "rul_00000011-0000-4000-8000-000000000011"
		ruleRej   = "rul_00000012-0000-4000-8000-000000000012"
		addrGrp   = "adr_00000013-0000-4000-8000-000000000013"
		portGrp   = "svc_00000014-0000-4000-8000-000000000014"
	)
	in := change(t, fixture(t, "nat-port-forwards"), func(d map[string]any) {
		d["management"].(map[string]any)["allowedSources"] = []any{"10.0.0.1", "10.0.0.0/24", "10.0.1.1-10.0.1.5", "2001:db8::1"}
		aliases := d["aliases"].(map[string]any)
		aliases["addresses"] = append(aliases["addresses"].([]any), map[string]any{
			"id": addrGrp, "name": "lit-group", "kind": "group", "members": []any{"192.0.2.5", "2001:db8::/64"},
		})
		aliases["ports"] = append(aliases["ports"].([]any), map[string]any{
			"id": portGrp, "name": "lit-ports", "protocol": "group", "members": []any{"tcp/443", "icmp/echo-request"},
		})
		d["rules"] = append(d["rules"].([]any),
			map[string]any{
				"id": ruleAllow, "name": "lit-allow", "enabled": true, "action": "accept",
				"from":  map[string]any{"zones": []any{}, "addresses": []any{"10.0.0.1", "10.0.0.10-10.0.0.20"}},
				"to":    map[string]any{"zones": []any{}, "addresses": []any{"198.51.100.0/24"}, "not": true},
				"ports": []any{"udp/53,5353", portGrp},
				"log":   false,
			},
			map[string]any{
				"id": ruleRej, "name": "lit-reject", "enabled": true, "action": "reject",
				"from":  map[string]any{"zones": []any{}, "addresses": []any{"2001:db8::1"}},
				"to":    map[string]any{"zones": []any{}, "addresses": []any{}},
				"ports": []any{"tcp/8000-8080", "icmpv6"},
				"log":   false,
			},
		)
		pfs := d["nat"].(map[string]any)["portForwards"].([]any)
		pf0 := pfs[0].(map[string]any)
		pf0["from"] = []any{"198.51.100.7"}
		pf0["to"] = map[string]any{"address": "203.0.113.5"}
		d["nat"].(map[string]any)["oneToOne"] = []any{
			map[string]any{
				"id": "oto_00000015-0000-4000-8000-000000000015", "name": "lit-o2o", "enabled": true,
				"zone": "zon_00000001-0000-4000-8000-000000000001", "external": "203.0.113.50", "internal": "192.0.2.50",
				"destination": []any{"198.51.100.9"},
			},
		}
	})
	f, ok := nftFile(t, render(t, in))
	if !ok {
		t.Fatal("no filter rendered")
	}
	lines(t, f.Content,
		"\tset mgmt_4 { type ipv4_addr; flags interval; auto-merge; elements = { 10.0.0.1, 10.0.0.0/24, 10.0.1.1-10.0.1.5 }; }",
		"\tset mgmt_6 { type ipv6_addr; flags interval; auto-merge; elements = { 2001:db8::1 }; }",
		"\tset adr_00000013_0000_4000_8000_000000000013_4 { type ipv4_addr; flags interval; auto-merge; elements = { 192.0.2.5 }; }",
		"\tset adr_00000013_0000_4000_8000_000000000013_6 { type ipv6_addr; flags interval; auto-merge; elements = { 2001:db8::/64 }; }",
		"\t\tip saddr 10.0.0.1 ip daddr != 198.51.100.0/24 udp dport { 53, 5353 } accept comment \""+ruleAllow+"\"",
		"\t\tip saddr 10.0.0.10-10.0.0.20 ip daddr != 198.51.100.0/24 tcp dport { 443 } accept comment \""+ruleAllow+"\"",
		"\t\tip saddr 10.0.0.1 ip daddr != 198.51.100.0/24 icmp type { echo-request } accept comment \""+ruleAllow+"\"",
		"\t\tip6 saddr 2001:db8::1 tcp dport { 8000-8080 } reject with tcp reset comment \""+ruleRej+"\"",
		"\t\tip6 saddr 2001:db8::1 meta l4proto ipv6-icmp reject comment \""+ruleRej+"\"",
	)
	if !strings.Contains(f.Content, "ip saddr 198.51.100.7 ip daddr 203.0.113.5") {
		t.Fatalf("port forward literal match missing:\n%s", f.Content)
	}
	if !strings.Contains(f.Content, "ip saddr 198.51.100.9 dnat ip to ip daddr map") || !strings.Contains(f.Content, "ip daddr 198.51.100.9 snat ip to ip saddr map") {
		t.Fatalf("1:1 NAT literal destination match missing:\n%s", f.Content)
	}
}

// Port 80 is redirected only where an enabled HTTP-01 order needs it: a DNS-01 or disabled order adds nothing.
func TestACMEHTTP01Redirect(t *testing.T) {
	with := func(edit func(orders []any)) string {
		f, _ := nftFile(t, render(t, change(t, fixture(t, "filter-acme-http01"), func(d map[string]any) {
			edit(d["acme"].(map[string]any)["orders"].([]any))
		})))
		return f.Content
	}
	if got := with(func([]any) {}); !strings.Contains(got, "set acme_http01_active { type ifname; }") || !strings.Contains(got, "tcp dport 80") {
		t.Fatalf("enabled HTTP-01 order rendered no redirect:\n%s", got)
	}
	for name, edit := range map[string]func([]any){
		"disabled": func(o []any) { o[0].(map[string]any)["enabled"] = false },
		"dns-01 only": func(o []any) {
			o[0].(map[string]any)["challenge"] = map[string]any{"type": "dns-01", "dns01": map[string]any{"provider": "cloudflare"}}
		},
	} {
		if got := with(edit); strings.Contains(got, "acme_http01_active") || strings.Contains(got, "dport 80") || strings.Contains(got, "8480") {
			t.Fatalf("%s: port 80 rule rendered:\n%s", name, got)
		}
	}
}

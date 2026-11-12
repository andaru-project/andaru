package render

import (
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

const (
	natWAN     = "zon_00000001_0000_4000_8000_000000000001"
	natBlocked = "adr_00000007_0000_4000_8000_000000000007_4"
	pfwWeb     = "pfw_00000001-0000-4000-8000-000000000001"
	pfwSkip    = "pfw_00000002-0000-4000-8000-000000000002"
	pfwRange   = "pfw_00000003-0000-4000-8000-000000000003"
	pfwSSH     = "pfw_00000004-0000-4000-8000-000000000004"
	pfwOff     = "pfw_00000005-0000-4000-8000-000000000005"
)

// table is the rendered nft script.
func table(t *testing.T, result Result) string {
	t.Helper()
	return fileContent(t, result, nftPath)
}

// chainOf is one chain's body.
func chainOf(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, "\tchain "+name+" {\n")
	if start < 0 {
		t.Fatalf("no chain %s in:\n%s", name, script)
	}
	end := strings.Index(script[start:], "\n\t}\n")
	return script[start : start+end+1]
}

// inOrder fails unless every line appears in body, in this order.
func inOrder(t *testing.T, body string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		i := strings.Index(body[at:], "\t\t"+w+"\n")
		if i < 0 {
			t.Fatalf("missing or out of order %q in:\n%s", w, body)
		}
		at += i + len(w)
	}
}

func TestPortForwardRendering(t *testing.T) {
	script := table(t, render(t, fixture(t, "nat-port-forwards")))
	inOrder(t, chainOf(t, script, "prerouting"),
		"type nat hook prerouting priority dstnat;",
		`iifname @`+natWAN+` meta nfproto ipv4 fib daddr . iif type local tcp dport 8080 log prefix "andaru `+pfwWeb+` " dnat ip to 192.0.2.10:80 comment "`+pfwWeb+`"`,
		`iifname @`+natWAN+` ip saddr @`+natBlocked+` meta nfproto ipv4 fib daddr . iif type local tcp dport 1000-1001 accept comment "`+pfwSkip+`"`,
		`iifname @`+natWAN+` meta nfproto ipv4 fib daddr . iif type local tcp dport 1000-1001 dnat ip addr . port to tcp dport map { 1000 : 192.0.2.10 . 2000, 1001 : 192.0.2.10 . 2001 } comment "`+pfwRange+`"`,
		`iifname @`+natWAN+` meta nfproto ipv4 fib daddr . iif type local tcp dport 2222 redirect to :22 comment "`+pfwSSH+`"`,
	)
	inOrder(t, chainOf(t, script, "forward"),
		"ct state invalid drop",
		`iifname @`+natWAN+` ct status dnat ct original proto-dst 8080 ip daddr 192.0.2.10 tcp dport 80 accept comment "`+pfwWeb+`"`,
		`iifname @`+natWAN+` ct status dnat ct original proto-dst 1000-1001 ip daddr 192.0.2.10 tcp dport 2000-2001 accept comment "`+pfwRange+`"`,
	)
	inOrder(t, chainOf(t, script, "input"),
		"iif lo accept",
		`iifname @`+natWAN+` ct status dnat ct original proto-dst 2222 meta nfproto ipv4 tcp dport 22 accept comment "`+pfwSSH+`"`,
	)
	if strings.Contains(script, pfwOff) {
		t.Fatalf("a disabled forward rendered:\n%s", script)
	}
	if n := strings.Count(script, pfwSkip); n != 1 {
		t.Fatalf("the exclusion rendered %d lines; it has no associated rule", n)
	}
}

// The associated rule comes before the policy, so a later rule cannot shadow the forward.
func TestAssociatedRuleBeforePolicy(t *testing.T) {
	forward := chainOf(t, table(t, render(t, fixture(t, "nat-port-forwards"))), "forward")
	policy := strings.Index(forward, "comment \"rul_")
	if at := strings.Index(forward, pfwWeb); at < 0 || policy < 0 || at > policy {
		t.Fatalf("policy before the associated rule:\n%s", forward)
	}
}

func natChange(t *testing.T, edit func(forwards []any)) Input {
	t.Helper()
	return change(t, fixture(t, "nat-port-forwards"), func(d map[string]any) {
		edit(d["nat"].(map[string]any)["portForwards"].([]any))
	})
}

func TestForwardWithoutFilterRule(t *testing.T) {
	script := table(t, render(t, natChange(t, func(f []any) { f[0].(map[string]any)["filterRule"] = "none" })))
	if !strings.Contains(chainOf(t, script, "prerouting"), pfwWeb) || strings.Contains(chainOf(t, script, "forward"), pfwWeb) {
		t.Fatalf("filterRule none:\n%s", script)
	}
}

func TestNATNeedsManagement(t *testing.T) {
	in := change(t, fixture(t, "nat-port-forwards"), func(d map[string]any) {
		delete(d, "management")
		d["nat"].(map[string]any)["outbound"] = map[string]any{"mode": "none"}
		d["policy"] = map[string]any{"input": []any{}, "forward": []any{}}
	})
	result := render(t, in)
	if result.Realizable || !strings.Contains(golden(result), "MANAGEMENT_REQUIRED") {
		t.Fatalf("realizable without a filter: %+v", result.Findings)
	}
}

func TestTCPUDPForward(t *testing.T) {
	script := table(t, render(t, natChange(t, func(f []any) {
		f[0].(map[string]any)["protocol"] = "tcp-udp"
		f[0].(map[string]any)["ports"] = "53"
		delete(f[0].(map[string]any), "targetPort")
	})))
	inOrder(t, chainOf(t, script, "prerouting"),
		`iifname @`+natWAN+` meta nfproto ipv4 fib daddr . iif type local meta l4proto { tcp, udp } th dport 53 log prefix "andaru `+pfwWeb+` " dnat ip to 192.0.2.10 comment "`+pfwWeb+`"`)
	inOrder(t, chainOf(t, script, "forward"),
		`iifname @`+natWAN+` ct status dnat ct original proto-dst 53 ip daddr 192.0.2.10 meta l4proto { tcp, udp } th dport 53 accept comment "`+pfwWeb+`"`)
}

func TestAddressTo(t *testing.T) {
	script := table(t, render(t, natChange(t, func(f []any) {
		f[0].(map[string]any)["to"] = map[string]any{"address": "adr_00000007-0000-4000-8000-000000000007"}
	})))
	inOrder(t, chainOf(t, script, "prerouting"),
		`iifname @`+natWAN+` ip daddr @`+natBlocked+` tcp dport 8080 log prefix "andaru `+pfwWeb+` " dnat ip to 192.0.2.10:80 comment "`+pfwWeb+`"`)
	inOrder(t, chainOf(t, script, "forward"),
		`iifname @`+natWAN+` ct status dnat ct original ip daddr @`+natBlocked+` ct original proto-dst 8080 ip daddr 192.0.2.10 tcp dport 80 accept comment "`+pfwWeb+`"`)
}

// A target that is one of the firewall's own addresses is local delivery: its rule goes in input.
func TestForwardToAFirewallAddress(t *testing.T) {
	script := table(t, render(t, natChange(t, func(f []any) {
		f[0].(map[string]any)["target"] = map[string]any{"address": "192.0.2.1"}
	})))
	if strings.Contains(chainOf(t, script, "forward"), pfwWeb) || !strings.Contains(chainOf(t, script, "input"), pfwWeb) {
		t.Fatalf("firewall address target:\n%s", script)
	}
}

const (
	otoServer  = "oto_00000002-0000-4000-8000-000000000002"
	otoBlock   = "oto_00000003-0000-4000-8000-000000000003"
	natPartner = "adr_00000007_0000_4000_8000_000000000007_4"
)

func TestOneToOneRendering(t *testing.T) {
	script := table(t, render(t, fixture(t, "nat-one-to-one")))
	inOrder(t, chainOf(t, script, "prerouting"),
		`iifname @`+natWAN+` meta nfproto ipv4 fib daddr . iif type local tcp dport 8080 dnat ip to 192.0.2.10:80 comment "`+pfwWeb+`"`,
		`iifname @internal fib daddr type local ip daddr != @internal_4 tcp dport 8080 dnat ip to 192.0.2.10:80 comment "`+pfwWeb+`"`,
		`iifname @`+natWAN+` dnat ip to ip daddr map { 10.0.1.29 : 192.0.2.30 } comment "`+otoServer+`"`,
		`iifname @internal dnat ip to ip daddr map { 10.0.1.29 : 192.0.2.30 } comment "`+otoServer+`"`,
		// The exclusion of 192.0.2.31 carves its external counterpart out of the mapping.
		`iifname @`+natWAN+` ip saddr @`+natPartner+` ip daddr != { 203.0.113.31 } dnat ip prefix to ip daddr map { 203.0.113.16/28 : 192.0.2.16/28 } comment "`+otoBlock+`"`,
	)
	inOrder(t, chainOf(t, script, "postrouting"),
		`oifname @`+natWAN+` snat ip to ip saddr map { 192.0.2.30 : 10.0.1.29 } comment "`+otoServer+`"`,
		`oifname @`+natWAN+` ip daddr @`+natPartner+` ip saddr != { 192.0.2.31 } snat ip prefix to ip saddr map { 192.0.2.16/28 : 203.0.113.16/28 } comment "`+otoBlock+`"`,
		`oifname "ens19" ct status dnat ip saddr 192.0.2.0/24 masquerade comment "reflection"`,
		// Excluded and unmapped hosts still leave through the outbound masquerade, after every 1:1 line.
		"fib saddr type != local oifname @uplinks meta nfproto ipv4 masquerade",
	)
	if !strings.Contains(script, `set internal { type ifname; elements = { "ens19" }; }`) || !strings.Contains(script, `set internal_4 { type ipv4_addr; elements = { 192.0.2.1 }; }`) {
		t.Fatalf("reflection sets:\n%s", script)
	}
}

// Without reflection there is no hairpin masquerade and no reflection set.
func TestNoReflectionNoHairpin(t *testing.T) {
	script := table(t, render(t, change(t, fixture(t, "nat-one-to-one"), func(d map[string]any) {
		nat := d["nat"].(map[string]any)
		for _, list := range []string{"portForwards", "oneToOne"} {
			for _, e := range nat[list].([]any) {
				delete(e.(map[string]any), "reflection")
			}
		}
	})))
	if strings.Contains(script, "reflection") || strings.Contains(script, "@internal") {
		t.Fatalf("reflection rendered without being asked for:\n%s", script)
	}
}

func findingAt(result Result, code, path string) bool {
	for _, f := range result.Findings {
		if f.Code == code && f.Path == path {
			return true
		}
	}
	return false
}

func TestProxyARPWarning(t *testing.T) {
	external := func(addr string) Result {
		return render(t, change(t, fixture(t, "nat-one-to-one"), func(d map[string]any) {
			d["nat"].(map[string]any)["oneToOne"].([]any)[1].(map[string]any)["external"] = addr
		}))
	}
	if r := external("10.0.1.50"); !findingAt(r, "PROXY_ARP_REQUIRED", "/nat/oneToOne/1/external") || !r.Realizable {
		t.Fatalf("10.0.1.50: %+v", r.Findings)
	}
	if r := external("10.0.1.29"); findingAt(r, "PROXY_ARP_REQUIRED", "/nat/oneToOne/1/external") {
		t.Fatalf("an address the WAN holds needs no proxy ARP: %+v", r.Findings)
	}
}

// A WAN address learned by DHCP inside a 1:1 range would hand the firewall's own traffic to the inside host.
func TestOneToOneLearnedAddress(t *testing.T) {
	in := fixture(t, "nat-one-to-one")
	for i := range in.Links {
		if in.Links[i].Name == "ens18" {
			// 10.0.1.29 is the declared extra WAN address the server's 1:1 uses: holding it is the point, not a conflict.
			in.Links[i].Addresses = append(in.Links[i].Addresses, host.AddressStatus{Family: "ipv4", Address: "203.0.113.17", PrefixLength: 28, Scope: "global"},
				host.AddressStatus{Family: "ipv4", Address: "10.0.1.29", PrefixLength: 16, Scope: "global"})
		}
	}
	r := render(t, in)
	if r.Realizable || !findingAt(r, "ONE_TO_ONE_OWN_ADDRESS", "/nat/oneToOne/2/external") || findingAt(r, "ONE_TO_ONE_OWN_ADDRESS", "/nat/oneToOne/1/external") {
		t.Fatalf("findings %+v", r.Findings)
	}
}

// A range kept on the same ports needs no port change: every port goes to itself, not to the first.
func TestSelfRangeWithoutShift(t *testing.T) {
	script := table(t, render(t, natChange(t, func(f []any) {
		f[3].(map[string]any)["ports"] = "1000-1001"
		f[3].(map[string]any)["targetPort"] = 1000
	})))
	inOrder(t, chainOf(t, script, "prerouting"),
		`iifname @`+natWAN+` meta nfproto ipv4 fib daddr . iif type local tcp dport 1000-1001 redirect comment "`+pfwSSH+`"`)
}

// Reflection keeps the forward's source match, as pfSense's reflection rdr does.
func TestReflectionKeepsTheSource(t *testing.T) {
	script := table(t, render(t, change(t, fixture(t, "nat-one-to-one"), func(d map[string]any) {
		d["nat"].(map[string]any)["portForwards"].([]any)[0].(map[string]any)["from"] = []any{"adr_00000007-0000-4000-8000-000000000007"}
	})))
	inOrder(t, chainOf(t, script, "prerouting"),
		`iifname @internal ip saddr @`+natPartner+` fib daddr type local ip daddr != @internal_4 tcp dport 8080 dnat ip to 192.0.2.10:80 comment "`+pfwWeb+`"`)
}

// An exclusion carves its hosts out of later entries of its own zone only.
func TestOneToOneExclusionStaysInItsZone(t *testing.T) {
	script := table(t, render(t, change(t, fixture(t, "nat-one-to-one"), func(d map[string]any) {
		d["nat"].(map[string]any)["oneToOne"].([]any)[0].(map[string]any)["zone"] = "zon_00000002-0000-4000-8000-000000000002"
	})))
	if strings.Contains(script, "192.0.2.31") {
		t.Fatalf("an exclusion in the LAN zone carved hosts out of WAN entries:\n%s", script)
	}
}

// A self forward's associated rule must not admit a connection a 1:1 entry translated to the same host and port.
func TestSelfRuleExcludesOneToOneTraffic(t *testing.T) {
	forward := chainOf(t, table(t, render(t, fixture(t, "nat-one-to-one"))), "forward")
	inOrder(t, forward,
		`iifname @`+natWAN+` ct status dnat ct original ip daddr != @one_to_one_4 ct original proto-dst 8080 ip daddr 192.0.2.10 tcp dport 80 accept comment "`+pfwWeb+`"`)
}

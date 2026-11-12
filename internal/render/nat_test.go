package render

import (
	"strings"
	"testing"
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
		`iifname @`+natWAN+` fib daddr . iif type local tcp dport 8080 log prefix "andaru `+pfwWeb+` " dnat ip to 192.0.2.10:80 comment "`+pfwWeb+`"`,
		`iifname @`+natWAN+` ip saddr @`+natBlocked+` fib daddr . iif type local tcp dport 1000-1001 accept comment "`+pfwSkip+`"`,
		`iifname @`+natWAN+` fib daddr . iif type local tcp dport 1000-1001 dnat ip addr . port to tcp dport map { 1000 : 192.0.2.10 . 2000, 1001 : 192.0.2.10 . 2001 } comment "`+pfwRange+`"`,
		`iifname @`+natWAN+` fib daddr . iif type local tcp dport 2222 redirect to :22 comment "`+pfwSSH+`"`,
	)
	inOrder(t, chainOf(t, script, "forward"),
		"ct state invalid drop",
		`iifname @`+natWAN+` ct status dnat ct original proto-dst 8080 ip daddr 192.0.2.10 tcp dport 80 accept comment "`+pfwWeb+`"`,
		`iifname @`+natWAN+` ct status dnat ct original proto-dst 1000-1001 ip daddr 192.0.2.10 tcp dport 2000-2001 accept comment "`+pfwRange+`"`,
	)
	inOrder(t, chainOf(t, script, "input"),
		"iif lo accept",
		`iifname @`+natWAN+` ct status dnat ct original proto-dst 2222 tcp dport 22 accept comment "`+pfwSSH+`"`,
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
		`iifname @`+natWAN+` fib daddr . iif type local meta l4proto { tcp, udp } th dport 53 log prefix "andaru `+pfwWeb+` " dnat ip to 192.0.2.10 comment "`+pfwWeb+`"`)
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

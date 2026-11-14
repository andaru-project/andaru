package render

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/delegation"
)

// A delegated segment's subnet waits for the prefix: the template holds the suffixes and positions, Kea's
// file only what does not change.
func TestDHCPv6DelegatedRendering(t *testing.T) {
	result := render(t, fixture(t, "dhcp6-delegated"))
	tpl := fileContent(t, result, kea6TemplatePath)
	contains(t, tpl, `"uplink": "ens18"`, `"subnetId": "1"`, `"interface": "ens19"`, `"first": "::1000"`, `"last": "::1fff"`,
		`"address": "::10"`, `"prefixId": "1"`, `"hostname": "router"`, `"length": 60`, `"delegatedLength": 64`,
		`"valid-lifetime": 7200`, `"preferred-lifetime": 4500`, `"name": "dns-servers"`, `"data": "fd00:a5d:1::1"`)
	kea := fileContent(t, result, kea6Path)
	contains(t, kea, `"interfaces": [`, `"ens19"`, `"subnet6": []`)
	if strings.Contains(kea, "::1000") {
		t.Fatalf("a delegated subnet in Kea's file:\n%s", kea)
	}
	contains(t, fileContent(t, result, nftPath), `iifname "ens19" ip6 saddr fe80::/10 udp sport 546 udp dport 547 accept`)
	if strings.Contains(networkFile(t, result, "ens19"), "[Route]") {
		t.Fatal("a delegated pool's route is root's, not networkd's")
	}
	if !result.Realizable {
		t.Fatalf("findings %+v", result.Findings)
	}
}

// What does not fit the prefix actually delegated is reported; what fits is not, whatever its length.
func TestDelegatedPrefixTooShort(t *testing.T) {
	held := func(p string) delegation.State {
		h := &delegation.Held{Prefix: netip.MustParsePrefix(p), Preferred: time.Now().Add(time.Hour), Valid: time.Now().Add(2 * time.Hour)}
		return delegation.State{WANs: map[string]delegation.WAN{"ens18": {Requested: 48, Current: h}}}
	}
	in := fixture(t, "dhcp6-delegated")
	in.Facts.Delegated = held("2001:db8:aa00::/56")
	if r := render(t, in); findingAt(r, "DELEGATED_PREFIX_TOO_SHORT", "/segments/1/addressing/ipv6/subnetId") || findingAt(r, "DELEGATED_PREFIX_TOO_SHORT", "/dhcp6/0/prefixDelegation") {
		t.Fatalf("subnet 1 and pool 8/60 fit a /56: %+v", r.Findings)
	}
	in = change(t, in, func(d map[string]any) {
		item(d, "segments", 1)["addressing"].(map[string]any)["ipv6"].(map[string]any)["subnetId"] = "100"
		d["dhcp6"].([]any)[0].(map[string]any)["prefixDelegation"] = map[string]any{"subnetId": "10", "length": 60, "delegatedLength": 64}
	})
	r := render(t, in)
	if !findingAt(r, "DELEGATED_PREFIX_TOO_SHORT", "/segments/1/addressing/ipv6/subnetId") || !findingAt(r, "DELEGATED_PREFIX_TOO_SHORT", "/dhcp6/0/prefixDelegation") || !r.Realizable {
		t.Fatalf("findings %+v", r.Findings)
	}
	if !strings.Contains(r.Findings[slicesIndex(r, "DELEGATED_PREFIX_TOO_SHORT")].Message, "2001:db8:aa00::/56") {
		t.Fatalf("the message does not name the prefix: %+v", r.Findings)
	}
	// A pool that fits is still not served when its own segment does not: it is named too.
	pool8 := change(t, in, func(d map[string]any) {
		d["dhcp6"].([]any)[0].(map[string]any)["prefixDelegation"] = map[string]any{"subnetId": "8", "length": 60, "delegatedLength": 64}
	})
	r = render(t, pool8)
	if !findingAt(r, "DELEGATED_PREFIX_TOO_SHORT", "/dhcp6/0/prefixDelegation") {
		t.Fatalf("the pool of a segment that does not fit went unnamed: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Path == "/dhcp6/0/prefixDelegation" && !strings.Contains(f.Message, "its segment's subnet ID") {
			t.Fatalf("the message blames the pool: %s", f.Message)
		}
	}
	in.Facts.Delegated = delegation.State{}
	if r := render(t, in); findingAt(r, "DELEGATED_PREFIX_TOO_SHORT", "/segments/1/addressing/ipv6/subnetId") {
		t.Fatal("judged without a prefix")
	}
}

func slicesIndex(r Result, code string) int {
	for i, f := range r.Findings {
		if f.Code == code {
			return i
		}
	}
	return -1
}

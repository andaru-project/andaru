package render

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/delegation"
	"github.com/andaru-project/andaru/internal/host"
)

func TestVirtualAddressesRendering(t *testing.T) {
	result := render(t, fixture(t, "virtual"))
	lines(t, "\n"+fileContent(t, result, networkDir+"andaru-vip.netdev"), "[NetDev]", "Name=andaru-vip", "Kind=dummy")
	lines(t, "\n"+fileContent(t, result, networkDir+"andaru-vip.network"), "[Match]", "Name=andaru-vip", "RequiredForOnline=no",
		"LinkLocalAddressing=no", "Address=172.16.53.53/32", "Address=fd00:53:53:53:53:53:53:53/128")
	unbound := fileContent(t, result, "/run/andaru-unbound/andaru.conf")
	for _, want := range []string{"\tinterface: 172.16.53.53\n", "\tinterface-action: 172.16.53.53 allow\n", "\tinterface: fd00:53:53:53:53:53:53:53\n", "\tinterface-action: fd00:53:53:53:53:53:53:53 allow\n"} {
		if !strings.Contains(unbound, want) {
			t.Fatalf("missing %q in:\n%s", want, unbound)
		}
	}
	nft := fileContent(t, result, nftPath)
	contains(t, nft,
		// IPv4 to the virtual address: dnat, and the rule in the input chain.
		`meta nfproto ipv4 fib daddr . iif type local udp dport 53 ct mark set ct mark & 0xffff0000 | 0x00000001 dnat ip to 172.16.53.53`,
		`ct mark & 0x0000ffff == 0x00000001 meta nfproto ipv4 udp dport 53 accept`,
		// IPv6 to the virtual address and to self.
		`meta nfproto ipv6 fib daddr . iif type local meta l4proto { tcp, udp } th dport 53 ct mark set ct mark & 0xffff0000 | 0x00000002 dnat ip6 to fd00:53:53:53:53:53:53:53`,
		`ct mark & 0x0000ffff == 0x00000002 meta nfproto ipv6 meta l4proto { tcp, udp } th dport 53 accept`,
		`meta nfproto ipv6 fib daddr . iif type local tcp dport 2222 ct mark set ct mark & 0xffff0000 | 0x00000003 redirect to :22`,
		`ct mark & 0x0000ffff == 0x00000003 meta nfproto ipv6 tcp dport 22 accept`,
	)
	// Reflection leaves the virtual addresses to their services.
	if i := strings.Index(nft, "set internal_4 {"); i < 0 || !strings.Contains(nft[i:i+200], "172.16.53.53") {
		t.Fatalf("internal_4 lacks the virtual address:\n%s", nft)
	}
	if strings.Contains(nft, "ip daddr 172.16.53.53 udp dport 53 accept") {
		t.Fatal("a forward to the virtual address took a forward-chain rule")
	}
	if !result.Realizable {
		t.Fatalf("findings %+v", result.Findings)
	}
}

// The other IPv6 forward forms: a port to a virtual address, a shifted range, a negated source, a destination object.
func TestIPv6ForwardForms(t *testing.T) {
	in := change(t, fixture(t, "virtual"), func(d map[string]any) {
		z := d["nat"].(map[string]any)["portForwards"].([]any)[0].(map[string]any)["ingressZones"]
		d["aliases"].(map[string]any)["addresses"] = append(d["aliases"].(map[string]any)["addresses"].([]any),
			map[string]any{"id": "adr_00000009-0000-4000-8000-000000000009", "name": "v6peer", "kind": "network", "prefix": "2001:db8:9::/48"})
		six := func(id, ports string, fields map[string]any) map[string]any {
			f := map[string]any{"id": id, "name": id[:12], "family": "ipv6", "enabled": true, "ingressZones": z, "protocol": "tcp", "from": []any{}, "to": map[string]any{"self": true}, "filterRule": "auto", "ports": ports, "target": map[string]any{"address": "fd00:53:53:53:53:53:53:53"}}
			for k, v := range fields {
				f[k] = v
			}
			return f
		}
		d["nat"].(map[string]any)["portForwards"] = append(d["nat"].(map[string]any)["portForwards"].([]any),
			six("pfw_00000005-0000-4000-8000-000000000005", "8053", map[string]any{"targetPort": 53}),
			six("pfw_00000006-0000-4000-8000-000000000006", "9000-9001", map[string]any{"targetPort": 7000}),
			six("pfw_00000007-0000-4000-8000-000000000007", "9100", map[string]any{"from": []any{"adr_00000009-0000-4000-8000-000000000009"}, "fromNegated": true}),
			six("pfw_00000008-0000-4000-8000-000000000008", "9200", map[string]any{"to": map[string]any{"address": "adr_00000009-0000-4000-8000-000000000009"}}))
	})
	nft := fileContent(t, render(t, in), nftPath)
	contains(t, nft,
		`dnat ip6 to [fd00:53:53:53:53:53:53:53]:53`,
		`dnat ip6 addr . port to tcp dport map { 9000 : fd00:53:53:53:53:53:53:53 . 7000, 9001 : fd00:53:53:53:53:53:53:53 . 7001 }`,
		`ip6 saddr != @adr_00000009_0000_4000_8000_000000000009_6`,
		`ip6 daddr @adr_00000009_0000_4000_8000_000000000009_6 tcp dport 9200`)
}

// Reflection on an address-object destination never takes a virtual address in it either.
func TestReflectionObjectSkipsVirtualAddresses(t *testing.T) {
	in := change(t, fixture(t, "virtual"), func(d map[string]any) {
		d["aliases"].(map[string]any)["addresses"] = append(d["aliases"].(map[string]any)["addresses"].([]any),
			map[string]any{"id": "adr_0000000a-0000-4000-8000-00000000000a", "name": "public", "kind": "network", "prefix": "172.16.53.0/24"})
		f := d["nat"].(map[string]any)["portForwards"].([]any)[3].(map[string]any)
		f["to"] = map[string]any{"address": "adr_0000000a-0000-4000-8000-00000000000a"}
	})
	nft := fileContent(t, render(t, in), nftPath)
	contains(t, nft, `iifname @internal ip daddr @adr_0000000a_0000_4000_8000_00000000000a_4 ip daddr != @virtual_4 tcp dport 8080`)
	if i := strings.Index(nft, "set virtual_4 {"); i < 0 || !strings.Contains(nft[i:i+200], "172.16.53.53") {
		t.Fatalf("no virtual_4 set:\n%s", nft)
	}
}

// A held block on a subnet the firewall learned, or under a prefix delegated to it, is warned about at the block;
// a single held address and a block clear of what was learned are not.
func TestHeldAddressOnLearnedSubnet(t *testing.T) {
	const four, six = "/virtualAddresses/2/address", "/virtualAddresses/3/address"
	in := fixture(t, "routed-block")
	if r := render(t, in); findingAt(r, "HELD_ADDRESS_ON_LEARNED_SUBNET", four) || findingAt(r, "HELD_ADDRESS_ON_LEARNED_SUBNET", six) {
		t.Fatalf("nothing learned, yet %+v", r.Findings)
	}
	in.Links[0].Addresses = []host.AddressStatus{
		{Family: "ipv4", Address: "203.0.113.2", PrefixLength: 28, Scope: "global"},
		{Family: "ipv4", Address: "172.16.53.1", PrefixLength: 24, Scope: "global"},
		{Family: "ipv6", Address: "fe80::1", PrefixLength: 64, Scope: "link"},
	}
	in.Facts.Delegated = delegation.State{WANs: map[string]delegation.WAN{"ens18": {Requested: 48,
		Current: &delegation.Held{Prefix: netip.MustParsePrefix("2001:db8::/32"), Preferred: time.Now().Add(time.Hour), Valid: time.Now().Add(2 * time.Hour)}}}}
	r := render(t, in)
	if !findingAt(r, "HELD_ADDRESS_ON_LEARNED_SUBNET", four) || !findingAt(r, "HELD_ADDRESS_ON_LEARNED_SUBNET", six) || !r.Realizable {
		t.Fatalf("findings %+v", r.Findings)
	}
	// A single held address on a learned subnet takes that one host's traffic: warned too, in its own words.
	if !slices.ContainsFunc(r.Findings, func(f Finding) bool {
		return f.Code == "HELD_ADDRESS_ON_LEARNED_SUBNET" && f.Path == "/virtualAddresses/0/address" && strings.HasPrefix(f.Message, "This address is on ")
	}) {
		t.Fatalf("no warning for a single held address: %+v", r.Findings)
	}
	in.Links[0].Addresses = []host.AddressStatus{{Family: "ipv4", Address: "203.0.113.10", PrefixLength: 29, Scope: "global"}}
	in.Facts.Delegated = delegation.State{}
	if r := render(t, in); findingAt(r, "HELD_ADDRESS_ON_LEARNED_SUBNET", four) {
		t.Fatalf("a neighbouring subnet warned: %+v", r.Findings)
	}
}

// A proxy NDP entry is on no andaru-vip: with no other virtual address there is no dummy device.
func TestProxyNDPIsNotOnTheDummy(t *testing.T) {
	r := render(t, change(t, fixture(t, "proxy-ndp"), func(d map[string]any) {
		d["virtualAddresses"] = d["virtualAddresses"].([]any)[1:]
	}))
	if slices.ContainsFunc(r.Files, func(f File) bool { return strings.Contains(f.Path, "andaru-vip") }) {
		t.Fatalf("andaru-vip rendered:\n%s", golden(r))
	}
	if !strings.Contains(fileEnding(t, r, "ens18.network").Content, "IPv6ProxyNDPAddress=2001:db8:1::50\n") {
		t.Fatalf("no proxy entry:\n%s", golden(r))
	}
}

// A proxy NDP address on no subnet its segment's link has, declared or learned, is warned about; one on a subnet the
// link learned is not, and a subnet another link learned does not count.
func TestProxyNDPOffSubnet(t *testing.T) {
	const one, two = "/virtualAddresses/1/address", "/virtualAddresses/2/address"
	in := fixture(t, "proxy-ndp")
	if r := render(t, in); findingAt(r, "PROXY_NDP_OFF_SUBNET", one) || findingAt(r, "PROXY_NDP_OFF_SUBNET", two) {
		t.Fatalf("on the static subnet, yet %+v", r.Findings)
	}
	in = change(t, in, func(d map[string]any) {
		item(d, "segments", 0)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "dhcp6", "requestPrefixLength": 56}
	})
	// Nothing learned yet, or a DHCPv6 address alone, a /128: the uplink's prefix is unknown, so nothing is said.
	if r := render(t, in); findingAt(r, "PROXY_NDP_OFF_SUBNET", one) || !r.Realizable {
		t.Fatalf("nothing learned, yet %+v", r.Findings)
	}
	in.Links[0].Addresses = []host.AddressStatus{
		{Family: "ipv6", Address: "fe80::1", PrefixLength: 64, Scope: "link"},
		{Family: "ipv6", Address: "fd00:1::2", PrefixLength: 64, Scope: "global"}, // a ULA tells nothing of the global prefix
		{Family: "ipv6", Address: "2001:db8:1::abcd", PrefixLength: 128, Scope: "global"},
	}
	if r := render(t, in); findingAt(r, "PROXY_NDP_OFF_SUBNET", one) {
		t.Fatalf("a DHCPv6 /128 alone warned: %+v", r.Findings)
	}
	// Given to the firewall itself, by DHCPv6: the host cannot have it.
	in.Links[0].Addresses = []host.AddressStatus{{Family: "ipv6", Address: "2001:db8:1::50", PrefixLength: 128, Scope: "global"}}
	if r := render(t, in); !findingAt(r, "PROXY_NDP_ADDRESS_HELD", one) || findingAt(r, "PROXY_NDP_ADDRESS_HELD", two) {
		t.Fatalf("the uplink's own address not warned, or another warned: %+v", r.Findings)
	}
	// The uplink on another prefix: the address is off it, whatever the LAN learned.
	in.Links[0].Addresses = []host.AddressStatus{{Family: "ipv6", Address: "2001:db8:9::2", PrefixLength: 64, Scope: "global"}}
	in.Links[1].Addresses = []host.AddressStatus{{Family: "ipv6", Address: "2001:db8:1::9", PrefixLength: 64, Scope: "global"}}
	if r := render(t, in); !findingAt(r, "PROXY_NDP_OFF_SUBNET", one) || !findingAt(r, "PROXY_NDP_OFF_SUBNET", two) {
		t.Fatalf("off the uplink's prefix, or the LAN's subnet counted: %+v", r.Findings)
	}
	in.Links[0].Addresses = []host.AddressStatus{
		{Family: "ipv6", Address: "fe80::1", PrefixLength: 64, Scope: "link"},
		{Family: "ipv6", Address: "2001:db8:1::2", PrefixLength: 64, Scope: "global"},
	}
	// Nor is it a held address on a learned subnet: the firewall does not hold it.
	if r := render(t, in); findingAt(r, "PROXY_NDP_OFF_SUBNET", one) || findingAt(r, "PROXY_NDP_OFF_SUBNET", two) ||
		findingAt(r, "HELD_ADDRESS_ON_LEARNED_SUBNET", one) || findingAt(r, "HELD_ADDRESS_ON_LEARNED_SUBNET", two) {
		t.Fatalf("on the learned subnet, yet %+v", r.Findings)
	}
}

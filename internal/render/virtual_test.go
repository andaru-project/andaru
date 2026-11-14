package render

import (
	"strings"
	"testing"
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
		d["objects"].(map[string]any)["addresses"] = append(d["objects"].(map[string]any)["addresses"].([]any),
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
		d["objects"].(map[string]any)["addresses"] = append(d["objects"].(map[string]any)["addresses"].([]any),
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

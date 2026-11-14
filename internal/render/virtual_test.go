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

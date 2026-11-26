package generation

import (
	"slices"
	"testing"
)

// networkd keeps a delegated address after its file stops asking for one (#45): Andaru removes it, and only there.
func TestStaleDelegatedAddressIsRemoved(t *testing.T) {
	delegated := "[Match]\nName=eth1\n\n[Network]\nIPv6AcceptRA=no\nDHCPPrefixDelegation=yes\n"
	none := "[Match]\nName=eth1\n\n[Network]\nLinkLocalAddressing=no\nIPv6AcceptRA=no\n"
	for _, s := range []struct {
		name string
		eth1 string
		want []string
	}{
		{"no longer delegated", none, nil},
		{"still delegated", delegated, []string{"fd00:a5d::1/64"}},
	} {
		b := newBench(t)
		b.net.devices["eth1"] = true
		b.live(map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-eth1.network": delegated})
		b.net.dynamic = map[string][]string{"eth0": {"fd00:1::1/64"}, "eth1": {"fd00:a5d::1/64"}}
		if err := b.applyConfirmed(map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-eth1.network": s.eth1}); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if got := b.net.dynamic["eth1"]; !slices.Equal(got, s.want) {
			t.Errorf("%s: eth1 has %q, want %q", s.name, got, s.want)
		}
		// eth0's file does not refuse router advertisements, so its address may be networkd's own.
		if got := b.net.dynamic["eth0"]; !slices.Equal(got, []string{"fd00:1::1/64"}) {
			t.Errorf("%s: eth0 has %q", s.name, got)
		}
	}
}

func TestTakesDynamicIPv6(t *testing.T) {
	for data, want := range map[string]bool{
		"DHCP=yes": true, "DHCP=ipv6": true, "IPv6AcceptRA=yes": true, "DHCPPrefixDelegation=yes": true, "Type=ppp": true,
		"DHCP=ipv4": true, "Address=fd00::1/64": true, "IPv6AcceptRA=no": false, "DHCP=ipv4\nIPv6AcceptRA=no": false,
	} {
		if got := takesDynamicIPv6([]byte("[Network]\n" + data + "\n")); got != want {
			t.Errorf("takesDynamicIPv6(%q) = %v, want %v", data, got, want)
		}
	}
}

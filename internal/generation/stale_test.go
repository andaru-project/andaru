package generation

import (
	"slices"
	"strings"
	"testing"
)

const (
	lanDelegated = "[Match]\nName=eth1\n\n[Network]\nIPv6AcceptRA=no\nDHCPPrefixDelegation=yes\n"
	lanNone      = "[Match]\nName=eth1\n\n[Network]\nLinkLocalAddressing=no\nIPv6AcceptRA=no\n"
)

func staleBench(t *testing.T, eth1 string) *bench {
	b := newBench(t)
	b.net.devices["eth1"] = true
	b.live(map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-eth1.network": eth1})
	b.net.dynamic = map[string][]string{"eth0": {"fd00:1::1/64"}, "eth1": {"fd00:a5d::1/60"}}
	return b
}

// networkd keeps a delegated address after its file stops asking for one (#45): Andaru removes it and its route from
// every link whose file takes no dynamic IPv6.
func TestStaleDelegatedAddressIsRemoved(t *testing.T) {
	for _, s := range []struct {
		name, before, after string
		gone                bool
	}{
		{"no longer delegated", lanDelegated, lanNone, true},
		{"still delegated", lanDelegated, lanDelegated, false},
		{"took none before either", lanNone, lanNone, true},
		// A link that takes advertisements may hold networkd's own SLAAC address.
		{"takes DHCPv6 without advertisements", lanDelegated, "[Match]\nName=eth1\n\n[Network]\nIPv6AcceptRA=no\nDHCP=ipv6\n", false},
		{"takes advertisements", lanNone, "[Match]\nName=eth1\n\n[Network]\nIPv6AcceptRA=yes\n", false},
	} {
		b := staleBench(t, s.before)
		if err := b.applyConfirmed(map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-eth1.network": s.after}); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		var want []string
		if !s.gone {
			want = []string{"fd00:a5d::1/60"}
		}
		if got := b.net.dynamic["eth1"]; !slices.Equal(got, want) {
			t.Errorf("%s: eth1 has %q, want %q", s.name, got, want)
		}
		if got, want := b.net.staleRoutes, map[bool][]string{true: {"eth1 fd00:a5d::/60"}}[s.gone]; !slices.Equal(got, want) {
			t.Errorf("%s: routes removed %q, want %q", s.name, got, want)
		}
		if got := b.net.dynamic["eth0"]; !slices.Equal(got, []string{"fd00:1::1/64"}) {
			t.Errorf("%s: eth0 has %q", s.name, got)
		}
	}
}

// The case #45 was found in: the rollback restores a file that takes no IPv6.
func TestRollbackRemovesTheDelegatedAddress(t *testing.T) {
	b := staleBench(t, lanNone)
	b.r.VerifyWait = 20 * b.r.Poll
	id := b.gen(map[string]string{
		"50-andaru-eth0.network": uplinkDHCP, "50-andaru-eth1.network": lanDelegated,
		"50-andaru-eth9.network": "[Match]\nName=eth9\n", // never bound, so the apply fails
	})
	if err := b.r.Activate(t.Context(), id); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("activate = %v, want a failure", err)
	}
	if got := b.net.dynamic["eth1"]; len(got) != 0 || !slices.Equal(b.net.staleRoutes, []string{"eth1 fd00:a5d::/60"}) {
		t.Fatalf("after the rollback eth1 has %q, routes removed %q", got, b.net.staleRoutes)
	}
}

func TestTakesDynamicIPv6(t *testing.T) {
	for data, want := range map[string]bool{
		"IPv6AcceptRA=yes": true, "DHCP=ipv4": true, "IPv6AcceptRA=no\nDHCPPrefixDelegation=yes": true,
		"IPv6AcceptRA=no\nType=ppp": true, "IPv6AcceptRA=no\nDHCP=ipv6": true, "IPv6AcceptRA=no\nDHCP=yes": true, "IPv6AcceptRA=no": false, "DHCP=ipv4\nIPv6AcceptRA=no": false,
	} {
		if got := takesDynamicIPv6([]byte("[Network]\n" + data + "\n")); got != want {
			t.Errorf("takesDynamicIPv6(%q) = %v, want %v", data, got, want)
		}
	}
}

// The prefix route of an address the new file still declares stays, and so does that address; one prefix is removed
// only once.
func TestStaleRouteKeepsADeclaredPrefix(t *testing.T) {
	b := staleBench(t, lanDelegated)
	b.net.dynamic["eth1"] = []string{"fd00:a5d::1/64", "fd00:a5d::2/64", "fd00:b::1/64", "fd00:c::1/128", "fd00:d::/60", "fd00:e::1/64"}
	// fd00:d:0:1::1/64 sits inside the stale /60 without being it; fd00:e::1/64 is declared as it is.
	static := "[Match]\nName=eth1\n\n[Network]\nLinkLocalAddressing=no\nIPv6AcceptRA=no\nAddress=fd00:b::9/64\nAddress=fd00:d:0:1::1/64\nAddress=fd00:e::1/64\n"
	if err := b.applyConfirmed(map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-eth1.network": static}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"eth1 fd00:a5d::/64", "eth1 fd00:d::/60"}; !slices.Equal(b.net.staleRoutes, want) {
		t.Fatalf("routes removed %q, want %q", b.net.staleRoutes, want)
	}
	if want := []string{"fd00:e::1/64"}; !slices.Equal(b.net.dynamic["eth1"], want) {
		t.Fatalf("eth1 keeps %q, want %q", b.net.dynamic["eth1"], want)
	}
}

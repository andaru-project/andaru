package delegation

import (
	"net/netip"
	"testing"
)

func TestPlace(t *testing.T) {
	for _, tc := range []struct {
		base   string
		id     uint64
		length int
		want   string // empty: does not fit
	}{
		{"2001:db8::/48", 0x1, 64, "2001:db8:0:1::/64"},
		{"2001:db8::/56", 0x8, 60, "2001:db8:0:80::/60"},
		{"2001:db8::/56", 0x100, 64, ""},
		{"2001:db8::/56", 0, 56, "2001:db8::/56"},
		{"2001:db8::/56", 1, 56, ""},
		{"2001:db8::/56", 0, 48, ""},
		{"2001:db8::/56", 0, 65, ""},
	} {
		got, ok := Place(netip.MustParsePrefix(tc.base), tc.id, tc.length)
		if tc.want == "" {
			if ok {
				t.Errorf("Place(%s, %x, %d) = %s, want no fit", tc.base, tc.id, tc.length, got)
			}
			continue
		}
		if !ok || got != netip.MustParsePrefix(tc.want) {
			t.Errorf("Place(%s, %x, %d) = %s %v, want %s", tc.base, tc.id, tc.length, got, ok, tc.want)
		}
	}
}

func TestJoin(t *testing.T) {
	if got := Join(netip.MustParsePrefix("2001:db8:0:1::/64"), netip.MustParseAddr("::1000")); got != netip.MustParseAddr("2001:db8:0:1::1000") {
		t.Fatalf("Join = %s", got)
	}
}

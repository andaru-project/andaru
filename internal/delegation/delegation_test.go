package delegation

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
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

const template = `{"subnets":[{"id":7,"interface":"ens19","uplink":"ens18","subnetId":"1",
	"ranges":[{"first":"::1000","last":"::1fff"}],
	"reservations":[{"duid":"00:01:00:01:aa:bb:cc:dd:ee:ff","address":"::10","prefixId":"1","hostname":"router"}],
	"pool":{"subnetId":"8","length":60,"delegatedLength":64},
	"valid-lifetime":7200,"preferred-lifetime":4500,"option-data":[],"user-context":{"andaru":{"domain":"home.arpa","register":true}}}]}`

func TestFillTemplate(t *testing.T) {
	subnets, skipped, err := FillTemplate([]byte(template), long("2001:db8:aa00::/48"), now)
	if err != nil || len(skipped) != 0 || len(subnets) != 1 {
		t.Fatalf("%v %v %v", subnets, skipped, err)
	}
	got, _ := json.Marshal(subnets[0])
	for _, want := range []string{`"id":7`, `"subnet":"2001:db8:aa00:1::/64"`, `"interface":"ens19"`, `"pool":"2001:db8:aa00:1::1000 - 2001:db8:aa00:1::1fff"`,
		`"delegated-len":64,"prefix":"2001:db8:aa00:80::","prefix-len":60`, `"ip-addresses":["2001:db8:aa00:1::10"]`, `"prefixes":["2001:db8:aa00:81::/64"]`,
		`"hostname":"router"`, `"valid-lifetime":7200`, `"option-data":[]`, `"user-context"`} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(string(got), "uplink") || strings.Contains(string(got), "subnetId") {
		t.Fatalf("template fields left in Kea's subnet: %s", got)
	}
	// A /56 has no room for subnet 100: the subnet is left out and named.
	tpl := strings.Replace(template, `"subnetId":"1",`, `"subnetId":"100",`, 1)
	subnets, skipped, err = FillTemplate([]byte(tpl), long("2001:db8:aa00::/56"), now)
	if err != nil || len(subnets) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0], "ens19") {
		t.Fatalf("%v %v %v", subnets, skipped, err)
	}
	// No prefix for the uplink: nothing to serve, nothing reported.
	if subnets, skipped, err = FillTemplate([]byte(template), nil, now); err != nil || len(subnets) != 0 || len(skipped) != 0 {
		t.Fatalf("%v %v %v", subnets, skipped, err)
	}
	// A pool that does not fit leaves the subnet served without it.
	subnets, skipped, _ = FillTemplate([]byte(strings.Replace(template, `"subnetId":"8"`, `"subnetId":"10"`, 1)), long("2001:db8:aa00::/56"), now)
	if got, _ := json.Marshal(subnets); len(subnets) != 1 || strings.Contains(string(got), "pd-pools") || strings.Contains(string(got), "prefixes") || len(skipped) != 1 {
		t.Fatalf("%s %v", got, skipped)
	}
}

var now = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

// long is a prefix held far longer than any lease.
func long(p string) map[string]Held {
	return map[string]Held{"ens18": {Prefix: netip.MustParsePrefix(p), Preferred: now.Add(240 * time.Hour), Valid: now.Add(480 * time.Hour)}}
}

// A lease never outlives the WAN's delegation: its lifetimes are cut to what remains of the prefix, in steps of
// ten minutes so Kea is not reconfigured every minute.
func TestFillTemplateBoundsLifetimes(t *testing.T) {
	for _, tc := range []struct {
		preferred, valid time.Duration
		want             string
	}{
		{240 * time.Hour, 480 * time.Hour, `"preferred-lifetime":4500,"reservations"`},
		{2900 * time.Second, 4000 * time.Second, `"preferred-lifetime":2400,`},
		{0, 300 * time.Second, `"preferred-lifetime":0,`},
	} {
		held := map[string]Held{"ens18": {Prefix: netip.MustParsePrefix("2001:db8:aa00::/48"), Preferred: now.Add(tc.preferred), Valid: now.Add(tc.valid)}}
		subnets, _, err := FillTemplate([]byte(template), held, now)
		got, _ := json.Marshal(subnets)
		if err != nil || !strings.Contains(string(got), tc.want) {
			t.Fatalf("%v/%v: missing %s in %s", tc.preferred, tc.valid, tc.want, got)
		}
		wantValid := map[time.Duration]string{480 * time.Hour: `"valid-lifetime":7200`, 4000 * time.Second: `"valid-lifetime":3600`, 300 * time.Second: `"valid-lifetime":300`}[tc.valid]
		if !strings.Contains(string(got), wantValid) {
			t.Fatalf("%v: missing %s in %s", tc.valid, wantValid, got)
		}
	}
}

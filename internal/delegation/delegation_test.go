package delegation

import (
	"encoding/json"
	"net/netip"
	"strings"
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

const template = `{"subnets":[{"id":7,"interface":"ens19","uplink":"ens18","subnetId":"1",
	"ranges":[{"first":"::1000","last":"::1fff"}],
	"reservations":[{"duid":"00:01:00:01:aa:bb:cc:dd:ee:ff","address":"::10","prefixId":"1","hostname":"router"}],
	"pool":{"subnetId":"8","length":60,"delegatedLength":64},
	"valid-lifetime":7200,"preferred-lifetime":4500,"option-data":[],"user-context":{"andaru":{"domain":"home.arpa","register":true}}}]}`

func TestFillTemplate(t *testing.T) {
	subnets, skipped := FillTemplate(parse(t, template), held("2001:db8:aa00::/48"))
	if len(skipped) != 0 || len(subnets) != 1 {
		t.Fatalf("%v %v", subnets, skipped)
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
	// A /56 has no room for subnet 100: the subnet is left out and named, and its pool (8/60 fits) with it.
	tpl := strings.Replace(template, `"subnetId":"1",`, `"subnetId":"100",`, 1)
	subnets, skipped = FillTemplate(parse(t, tpl), held("2001:db8:aa00::/56"))
	if len(subnets) != 0 || len(skipped) != 2 || !strings.Contains(skipped[0], "subnet of ens19") || !strings.Contains(skipped[1], "delegation pool of ens19") {
		t.Fatalf("%v %v", subnets, skipped)
	}
	// No prefix for the uplink: nothing to serve, nothing reported.
	if subnets, skipped = FillTemplate(parse(t, template), nil); len(subnets) != 0 || len(skipped) != 0 {
		t.Fatalf("%v %v", subnets, skipped)
	}
	// A pool that does not fit leaves the subnet served without it.
	subnets, skipped = FillTemplate(parse(t, strings.Replace(template, `"subnetId":"8"`, `"subnetId":"10"`, 1)), held("2001:db8:aa00::/56"))
	if got, _ := json.Marshal(subnets); len(subnets) != 1 || strings.Contains(string(got), "pd-pools") || strings.Contains(string(got), "prefixes") || len(skipped) != 1 {
		t.Fatalf("%s %v", got, skipped)
	}
}

// held is ens18 holding prefix p.
func held(p string) map[string]Held {
	return map[string]Held{"ens18": {Prefix: netip.MustParsePrefix(p)}}
}

func parse(t *testing.T, raw string) Template {
	t.Helper()
	var tpl Template
	if err := json.Unmarshal([]byte(raw), &tpl); err != nil {
		t.Fatal(err)
	}
	return tpl
}

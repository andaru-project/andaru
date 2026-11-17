package config

import (
	"strings"
	"testing"
)

func TestManagementLANClosure(t *testing.T) {
	vlan := 6
	d := declaration{
		Management: &management{Segment: "seg-lan", AllowedSources: []string{"addr-admin"}},
		Zones:      []zone{{identity: identity{ID: "zone-lan", Name: "lan"}}},
		Links: []link{
			{identity: identity{ID: "link-nic"}, Kind: "ethernet", Enabled: true},
			{identity: identity{ID: "link-vlan"}, Kind: "vlan", Enabled: true, Parent: "link-nic", VLANID: &vlan},
		},
		Segments: []segment{{
			identity: identity{ID: "seg-lan", Name: "lan"},
			Link:     "link-vlan", Zone: "zone-lan",
			Addressing: addressing{IPv4: ipv4{Mode: "static", Addresses: []string{"192.0.2.1/24"}}, IPv6: ipv6{Mode: "none"}},
		}},
	}
	raw, err := managementLAN(d, map[string]string{"WAN_PASSWORD": "TEST_ONLY_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`"kind":"lan-closure"`, `"segment":"seg-lan"`, `"addr-admin"`, `"link-vlan"`, `"link-nic"`, `"zone-lan"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "TEST_ONLY_SECRET") || strings.Contains(body, "WAN_PASSWORD") {
		t.Fatalf("unbound secret leaked: %s", body)
	}
}

func TestUplinkManagementRefused(t *testing.T) {
	base := func() declaration {
		return declaration{
			Management: &management{Segment: "seg"},
			Links:      []link{{identity: identity{ID: "link"}, Kind: "ethernet", Enabled: true}},
			Segments: []segment{{
				identity: identity{ID: "seg"}, Link: "link",
				Addressing: addressing{IPv4: ipv4{Mode: "static", Addresses: []string{"192.0.2.1/24"}}, IPv6: ipv6{Mode: "none"}},
			}},
		}
	}
	cases := []declaration{base(), base(), base()}
	cases[0].Segments[0].Addressing.IPv4.Gateway = "192.0.2.254"
	cases[1].Segments[0].Addressing.IPv4.Mode = "dhcp"
	cases[2].Links[0].Kind = "pppoe"
	for _, d := range cases {
		raw, err := managementLAN(d, nil)
		if err == nil || !strings.Contains(err.Error(), "UPLINK_MANAGEMENT") || raw != nil {
			t.Fatalf("got %q %v", raw, err)
		}
	}
}

func TestNoManagementRefused(t *testing.T) {
	raw, err := managementLAN(declaration{}, nil)
	if err == nil || !strings.Contains(err.Error(), "NO_MANAGEMENT") || raw != nil {
		t.Fatalf("got %q %v", raw, err)
	}
}

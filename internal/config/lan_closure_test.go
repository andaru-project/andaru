package config

import (
	"encoding/json"
	"slices"
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

// keptLAN records the closure of the policy fixture with the delegated LAN as management, reached from a group.
func keptLAN(t *testing.T) []byte {
	t.Helper()
	d := policyFixture(t)
	d["management"] = map[string]any{"segment": item(segmentsOf(d), 1)["id"], "allowedSources": []any{adrGroup}}
	decl, err := decodeMap(t, d)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := managementLAN(decl, map[string]string{"WAN_PASSWORD": "TEST_ONLY_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestKeptRecoveryDeclaration(t *testing.T) {
	body := []byte(`{"declaration":{"schemaVersion":1,"links":[],"segments":[],"zones":[]},"secrets":{"WAN_PASSWORD":"TEST_ONLY_SECRET"}}`)
	docs, err := KeptDeclaration(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(docs.Configuration) != `{"schemaVersion":1,"links":[],"segments":[],"zones":[]}` {
		t.Fatalf("configuration = %s", docs.Configuration)
	}
	if !strings.Contains(string(docs.ConfigurationSecrets), `"WAN_PASSWORD":{"kind":"password","value":"TEST_ONLY_SECRET"}`) {
		t.Fatalf("secrets = %s", docs.ConfigurationSecrets)
	}
	if err = ValidateBackupDocuments(docs); err != nil {
		t.Fatal(err)
	}
}

func TestKeptLANDeclaration(t *testing.T) {
	docs, err := KeptDeclaration(keptLAN(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateBackupDocuments(docs); err != nil {
		t.Fatalf("%v: %s", err, docs.Configuration)
	}
	var d declaration
	if err = json.Unmarshal(docs.Configuration, &d); err != nil {
		t.Fatal(err)
	}
	if d.Management == nil || len(d.Segments) != 1 || d.Segments[0].Name != "lan" || len(d.Links) != 3 || len(d.Zones) != 1 || len(d.Objects.Addresses) != 2 {
		t.Fatalf("declaration = %s", docs.Configuration)
	}
	if d.DNS != nil || len(d.DHCP) != 0 || len(d.DHCP6) != 0 || len(d.NAT.PortForwards) != 0 || len(d.Policy.Input) != 0 || len(d.Policy.Forward) != 0 {
		t.Fatalf("services or policy came along: %s", docs.Configuration)
	}
	if d.Segments[0].Addressing.IPv6.Mode != "none" {
		t.Fatalf("IPv6 delegated from the uplink came along: %+v", d.Segments[0].Addressing.IPv6)
	}
	if strings.Contains(string(docs.ConfigurationSecrets), "TEST_ONLY_SECRET") || strings.Contains(string(docs.Configuration), "wan") {
		t.Fatalf("the uplink came along: %s %s", docs.Configuration, docs.ConfigurationSecrets)
	}
}

func TestKeptLANRefusesUplink(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(keptLAN(t), &doc); err != nil {
		t.Fatal(err)
	}
	seg := doc["segments"].([]any)[0].(map[string]any)
	seg["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "dhcp"}
	body, _ := json.Marshal(doc)
	if _, err := KeptDeclaration(body); err == nil || !strings.Contains(err.Error(), "UPLINK_MANAGEMENT") {
		t.Fatalf("uplink closure: %v", err)
	}
}

// A copy recorded before groups carried their members cannot be repaired without widening who may reach management,
// so it is refused, and disaster boot moves on to the next copy.
func TestKeptLANRefusesGroupWithoutMembers(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(keptLAN(t), &doc); err != nil {
		t.Fatal(err)
	}
	// Before groups carried their members, only the group itself was recorded.
	doc["addresses"] = slices.DeleteFunc(doc["addresses"].([]any), func(a any) bool { return a.(map[string]any)["id"] != adrGroup })
	body, _ := json.Marshal(doc)
	if _, err := KeptDeclaration(body); err == nil {
		t.Fatal("a group with no members left validated")
	} else if !strings.Contains(err.Error(), "VALIDATION") {
		t.Fatalf("older copy: %v", err)
	}
}

func TestKeptLANRefusesNoAddress(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(keptLAN(t), &doc); err != nil {
		t.Fatal(err)
	}
	// Its only address was the delegated IPv6, which a copy cannot keep.
	seg := doc["segments"].([]any)[0].(map[string]any)
	seg["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "none"}
	body, _ := json.Marshal(doc)
	if _, err := KeptDeclaration(body); err == nil || !strings.Contains(err.Error(), "NO_ADDRESS") {
		t.Fatalf("addressless management LAN: %v", err)
	}
}

package config

import (
	"encoding/json"
	"slices"
	"testing"
)

const legacyZone = "zon_00000001-0000-4000-8000-000000000001"

// legacyDocument is a configuration as schema 1 saved it: two rule lists, sides that mix zones and addresses,
// and objects where the aliases are now.
func legacyDocument(t *testing.T) []byte {
	t.Helper()
	rule := func(id, name string, from, to []any) map[string]any {
		return map[string]any{"id": id, "name": name, "enabled": true, "action": "accept", "from": from, "to": to, "services": []any{svcDNS}, "log": false}
	}
	raw, err := canonical(map[string]any{
		"schemaVersion": 1, "links": []any{}, "segments": []any{},
		"zones": []any{map[string]any{"id": legacyZone, "name": "lan"}},
		"objects": map[string]any{
			"addresses": []any{map[string]any{"id": adrHost, "name": "nas", "kind": "host", "address": "192.0.2.10"}},
			"services":  []any{map[string]any{"id": svcDNS, "name": "dns", "protocol": "udp", "ports": []any{"53"}}},
		},
		"policy": map[string]any{
			// "From lan or from the nas" to the firewall, and "from lan or the nas, to the nas or to lan" through it.
			"input":   []any{rule(rulA, "dns", []any{adrHost, legacyZone}, []any{})},
			"forward": []any{rule(rulB, "dns", []any{legacyZone, adrHost}, []any{adrHost, legacyZone}), rule(rulC, "any", []any{}, []any{})},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLegacyDocumentReadsAsTheCurrentForm(t *testing.T) {
	raw := legacyDocument(t)
	d, stored, origin, err := decodeSaved(raw)
	if err != nil || stored != 1 || d.SchemaVersion != schemaVersion {
		t.Fatalf("stored=%d schema=%d: %v", stored, d.SchemaVersion, err)
	}
	if len(d.Aliases.Addresses) != 1 || d.Aliases.Addresses[0].ID != adrHost || len(d.Aliases.Ports) != 1 || d.Aliases.Ports[0].ID != svcDNS {
		t.Fatalf("aliases: %+v", d.Aliases)
	}
	type row struct {
		id                          string
		fromZones, fromAddresses    []string
		toZones, toAddresses, ports []string
	}
	var got []row
	for _, r := range d.Rules {
		got = append(got, row{r.ID, r.From.Zones, r.From.Addresses, r.To.Zones, r.To.Addresses, r.Ports})
	}
	none, zone, host, firewall, dns := []string{}, []string{legacyZone}, []string{adrHost}, []string{firewallZone}, []string{svcDNS}
	want := []row{
		// The input rules first, to the firewall; a side that mixed zones and addresses is one rule per kind.
		{rulA, zone, none, firewall, none, dns},
		{derivedRuleID(rulA, 1), none, host, firewall, none, dns},
		{rulB, zone, none, zone, none, dns},
		{derivedRuleID(rulB, 1), zone, none, none, host, dns},
		{derivedRuleID(rulB, 2), none, host, zone, none, dns},
		{derivedRuleID(rulB, 3), none, host, none, host, dns},
		{rulC, none, none, none, none, dns},
	}
	if len(got) != len(want) {
		t.Fatalf("rules: %+v", got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.id != w.id || !slices.Equal(g.fromZones, w.fromZones) || !slices.Equal(g.fromAddresses, w.fromAddresses) || !slices.Equal(g.toZones, w.toZones) || !slices.Equal(g.toAddresses, w.toAddresses) || !slices.Equal(g.ports, w.ports) {
			t.Fatalf("rule %d: got %+v, want %+v", i, g, w)
		}
	}
	if len(origin) != 4 || origin[derivedRuleID(rulA, 1)] != rulA || origin[derivedRuleID(rulB, 1)] != rulB || origin[derivedRuleID(rulB, 3)] != rulB {
		t.Fatalf("origin: %v", origin)
	}
	// The same document reads the same way each time, IDs included: the store upgrades it on every load.
	again, _, _, err := decodeSaved(raw)
	a, _ := canonical(d)
	b, _ := canonical(again)
	if err != nil || string(a) != string(b) {
		t.Fatalf("a second read differs: %v\n%s\n%s", err, a, b)
	}
	// What it reads as is itself a valid document of the current form.
	if _, was, _, err := decodeSaved(a); err != nil || was != schemaVersion {
		t.Fatalf("the upgraded document does not load: %d %v", was, err)
	}
}

func TestLegacyDocumentIsStillChecked(t *testing.T) {
	for name, edit := range map[string]func(d map[string]any){
		"the current form's field in a legacy document": func(d map[string]any) { d["rules"] = []any{} },
		"a rule naming a missing address": func(d map[string]any) {
			d["policy"].(map[string]any)["input"].([]any)[0].(map[string]any)["from"] = []any{"adr_00000009-0000-4000-8000-000000000009"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var d map[string]any
			if err := json.Unmarshal(legacyDocument(t), &d); err != nil {
				t.Fatal(err)
			}
			edit(d)
			raw, _ := json.Marshal(d)
			if _, _, _, err := decodeSaved(raw); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// A zone on a legacy side stood for both families, so a rule could pair "lan or an IPv4 host" with what only
// IPv6 reaches. Split, its host half can never match: it is left out, as the engine before rendered nothing for it.
func TestLegacyRuleWhoseSplitHalfNeverMatchesStillLoads(t *testing.T) {
	for name, edit := range map[string]func(objects, policy map[string]any){
		"an IPv6 destination": func(objects, policy map[string]any) {
			objects["addresses"] = append(objects["addresses"].([]any), map[string]any{"id": adrNet6, "name": "v6net", "kind": "network", "prefix": "2001:db8::/64"})
			rule := policy["forward"].([]any)[0].(map[string]any)
			rule["from"], rule["to"] = []any{adrHost, legacyZone}, []any{adrNet6}
		},
		"an ICMPv6 service": func(objects, policy map[string]any) {
			objects["services"] = append(objects["services"].([]any), map[string]any{"id": svcGroup, "name": "ping6", "protocol": "icmpv6", "types": []any{"echo-request"}})
			policy["forward"].([]any)[0].(map[string]any)["from"] = []any{adrHost, legacyZone}
			policy["forward"].([]any)[0].(map[string]any)["to"] = []any{}
			policy["forward"].([]any)[0].(map[string]any)["services"] = []any{svcGroup}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(legacyDocument(t), &m); err != nil {
				t.Fatal(err)
			}
			edit(m["objects"].(map[string]any), m["policy"].(map[string]any))
			raw, _ := json.Marshal(m)
			d, _, origin, err := decodeSaved(raw)
			if err != nil {
				t.Fatalf("a document the engine before saved does not load: %v", err)
			}
			var ids []string
			for _, r := range d.Rules {
				ids = append(ids, r.ID)
			}
			// rulB's zone half stays and its host half is gone; rulA is split as before.
			if want := []string{rulA, derivedRuleID(rulA, 1), rulB, rulC}; !slices.Equal(ids, want) {
				t.Fatalf("rules %v, want %v", ids, want)
			}
			if _, kept := origin[derivedRuleID(rulB, 1)]; kept || len(origin) != 1 {
				t.Fatalf("origin still names the rule left out: %v", origin)
			}
		})
	}
	// A rule that can never match and was not split is refused, as any document's is.
	var m map[string]any
	_ = json.Unmarshal(legacyDocument(t), &m)
	objects := m["objects"].(map[string]any)
	objects["addresses"] = append(objects["addresses"].([]any), map[string]any{"id": adrNet6, "name": "v6net", "kind": "network", "prefix": "2001:db8::/64"})
	rule := m["policy"].(map[string]any)["forward"].([]any)[1].(map[string]any)
	rule["from"], rule["to"] = []any{adrHost}, []any{adrNet6}
	raw, _ := json.Marshal(m)
	if _, _, _, err := decodeSaved(raw); err == nil {
		t.Fatal("a rule that never matched in the form before was accepted")
	}
}

// TestLegacyFormsLoad holds documents of the later schemas a release wrote. A release moved a configuration to
// schema 2 once a restore left a link's password unresolved, and to schema 3 once it held roles; either stayed
// after that, so schema 3 also comes with no roles. Each is the schema 1 document with those additions, and loads
// as the current form with its rules upgraded and what made it that schema intact.
func TestLegacyFormsLoad(t *testing.T) {
	const eth, ppp, role = "lnk_00000001-0000-4000-8000-000000000001", "lnk_00000002-0000-4000-8000-000000000002", "rol_00000001-0000-4000-8000-000000000001"
	links := []any{
		map[string]any{"id": eth, "name": "wan0", "kind": "ethernet", "mtu": 1500, "enabled": true, "permanentMac": "02:00:00:00:00:01"},
		map[string]any{"id": ppp, "name": "wan", "kind": "pppoe", "mtu": 1492, "enabled": true, "parent": eth, "username": "user", "password": map[string]any{"secret": "WAN_PASSWORD", "unresolved": true}},
	}
	roles := []any{map[string]any{"id": role, "name": "Operator", "capabilities": []any{"configuration.write", "status.read"}}}
	for _, tc := range []struct {
		schema int
		roles  []any
	}{{2, nil}, {3, roles}, {3, []any{}}} {
		var doc map[string]any
		if err := json.Unmarshal(legacyDocument(t), &doc); err != nil {
			t.Fatal(err)
		}
		doc["schemaVersion"], doc["links"] = tc.schema, links
		if tc.roles != nil {
			doc["roles"] = tc.roles
		}
		raw, err := canonical(doc)
		if err != nil {
			t.Fatal(err)
		}
		d, stored, _, err := decodeSaved(raw)
		if err != nil || stored != tc.schema || d.SchemaVersion != schemaVersion {
			t.Fatalf("schema %d: stored=%d schema=%d: %v", tc.schema, stored, d.SchemaVersion, err)
		}
		if len(d.Rules) != 7 || len(d.Aliases.Addresses) != 1 {
			t.Fatalf("schema %d: rules %d, aliases %+v", tc.schema, len(d.Rules), d.Aliases)
		}
		if len(d.Links) != 2 || d.Links[1].Password == nil || !d.Links[1].Password.Unresolved {
			t.Fatalf("schema %d: links: %+v", tc.schema, d.Links)
		}
		if len(d.Roles) != len(tc.roles) || len(tc.roles) == 1 && (d.Roles[0].ID != role || len(d.Roles[0].Capabilities) != 2) {
			t.Fatalf("schema %d: roles: %+v", tc.schema, d.Roles)
		}
	}
}

// A schema 3 forward negating no sources matched every source; it still loads as it was saved.
func TestLegacyEmptyNegatedSourceLoads(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(legacyDocument(t), &doc); err != nil {
		t.Fatal(err)
	}
	doc["schemaVersion"], doc["roles"] = 3, []any{}
	doc["nat"] = map[string]any{"outbound": map[string]any{"mode": "none"}, "portForwards": []any{map[string]any{
		"id": "pfw_00000001-0000-4000-8000-000000000001", "name": "web", "enabled": true, "ingressZones": []any{legacyZone},
		"protocol": "tcp", "from": []any{}, "fromNegated": true, "to": map[string]any{"self": true}, "ports": "8080",
		"target": map[string]any{"address": "192.0.2.10"}}}}
	raw, err := canonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	d, _, _, err := decodeSaved(raw)
	if err != nil || len(d.NAT.PortForwards) != 1 || !d.NAT.PortForwards[0].FromNegated {
		t.Fatalf("forwards %+v: %#v", d.NAT.PortForwards, err)
	}
}

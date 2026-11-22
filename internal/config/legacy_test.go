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
			// "From lan or from the nas" to the firewall, and "from lan to the nas or to lan" through it.
			"input":   []any{rule(rulA, "dns", []any{adrHost, legacyZone}, []any{})},
			"forward": []any{rule(rulB, "dns", []any{legacyZone}, []any{adrHost, legacyZone}), rule("rul_00000003-0000-4000-8000-000000000003", "any", []any{}, []any{})},
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
		{"rul_00000003-0000-4000-8000-000000000003", none, none, none, none, dns},
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
	if len(origin) != 2 || origin[derivedRuleID(rulA, 1)] != rulA || origin[derivedRuleID(rulB, 1)] != rulB {
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

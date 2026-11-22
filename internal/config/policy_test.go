package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const (
	adrHost  = "adr_00000001-0000-4000-8000-000000000001"
	adrNet6  = "adr_00000002-0000-4000-8000-000000000002"
	adrGroup = "adr_00000003-0000-4000-8000-000000000003"
	svcDNS   = "svc_00000001-0000-4000-8000-000000000001"
	svcGroup = "svc_00000002-0000-4000-8000-000000000002"
	rulA     = "rul_00000001-0000-4000-8000-000000000001"
	rulB     = "rul_00000002-0000-4000-8000-000000000002"
	rulC     = "rul_00000003-0000-4000-8000-000000000003"
)

// side is one side of a rule: its zones and, within them, its addresses.
func side(zones []any, addresses ...any) map[string]any {
	if zones == nil {
		zones = []any{}
	}
	if addresses == nil {
		addresses = []any{}
	}
	return map[string]any{"zones": zones, "addresses": addresses}
}

// policyFixture is the network fixture plus aliases, rules, outbound NAT and management.
func policyFixture(t *testing.T) map[string]any {
	t.Helper()
	d := fixtureMap(t)
	zone := item(d["zones"].([]any), 0)["id"].(string)
	seg := item(segmentsOf(d), 0)["id"].(string)
	d["aliases"] = map[string]any{
		"addresses": []any{
			// Saved out of ID order: the store sorts objects, never rules.
			map[string]any{"id": adrGroup, "name": "servers", "kind": "group", "members": []any{adrHost}},
			map[string]any{"id": adrHost, "name": "nas", "kind": "host", "address": "192.0.2.10"},
			map[string]any{"id": adrNet6, "name": "v6net", "kind": "network", "prefix": "2001:db8::/64"},
		},
		"ports": []any{
			map[string]any{"id": svcDNS, "name": "dns", "protocol": "udp", "ports": []any{"53"}},
			map[string]any{"id": svcGroup, "name": "web", "protocol": "group", "members": []any{svcDNS}},
		},
	}
	d["rules"] = []any{
		map[string]any{"id": rulB, "name": "b", "enabled": true, "action": "accept", "from": side([]any{zone}), "to": side(nil, adrGroup), "ports": []any{svcGroup}, "log": false},
		map[string]any{"id": rulA, "name": "a", "enabled": false, "action": "drop", "from": side(nil), "to": side(nil), "ports": []any{}, "log": true},
	}
	d["nat"] = map[string]any{"outbound": map[string]any{"mode": "automatic"}}
	d["management"] = map[string]any{"segment": seg, "allowedSources": []any{adrHost}}
	return d
}

func TestCanonicalKeepsPolicyOrder(t *testing.T) {
	d, err := decodeMap(t, policyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.Rules[0].ID != rulB || d.Rules[1].ID != rulA {
		t.Fatalf("rule order changed: %s, %s", d.Rules[0].ID, d.Rules[1].ID)
	}
	if d.Aliases.Addresses[0].ID != adrHost || d.Aliases.Addresses[2].ID != adrGroup {
		t.Fatal("addresses not sorted by id")
	}
	// An empty filter section reads back as absent, so an untouched declaration keeps its form.
	plain, err := decodeMap(t, fixtureMap(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := canonical(plain)
	for _, key := range []string{`"aliases"`, `"rules"`, `"nat"`, `"management"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("%s appeared in a declaration without a filter: %s", key, raw)
		}
	}
}

func TestPolicyValidation(t *testing.T) {
	type edit func(d map[string]any)
	addr := func(d map[string]any, i int) map[string]any {
		return item(d["aliases"].(map[string]any)["addresses"].([]any), i)
	}
	svc := func(d map[string]any, i int) map[string]any {
		return item(d["aliases"].(map[string]any)["ports"].([]any), i)
	}
	fwd := func(d map[string]any, i int) map[string]any {
		return item(d["rules"].([]any), i)
	}
	zoneOf := func(d map[string]any) any { return item(d["zones"].([]any), 0)["id"] }
	for name, tc := range map[string]struct {
		edit edit
		path string
	}{
		"host not an address":    {func(d map[string]any) { addr(d, 1)["address"] = "nas.example" }, "/aliases/addresses/1/address"},
		"network with host bits": {func(d map[string]any) { addr(d, 2)["prefix"] = "2001:db8::1/64" }, "/aliases/addresses/2/prefix"},
		"range backwards": {func(d map[string]any) {
			addr(d, 1)["kind"] = "range"
			delete(addr(d, 1), "address")
			addr(d, 1)["first"] = "192.0.2.9"
			addr(d, 1)["last"] = "192.0.2.1"
		}, "/aliases/addresses/1/last"},
		"range across families": {func(d map[string]any) {
			addr(d, 1)["kind"] = "range"
			delete(addr(d, 1), "address")
			addr(d, 1)["first"] = "192.0.2.1"
			addr(d, 1)["last"] = "2001:db8::1"
		}, "/aliases/addresses/1/last"},
		"group of a group":          {func(d map[string]any) { addr(d, 0)["members"] = []any{adrGroup} }, "/aliases/addresses/0/members/0"},
		"empty group":               {func(d map[string]any) { addr(d, 0)["members"] = []any{} }, "/aliases/addresses/0/members"},
		"group of a missing member": {func(d map[string]any) { addr(d, 0)["members"] = []any{"adr_00000009-0000-4000-8000-000000000009"} }, "/aliases/addresses/0/members/0"},
		"port zero":                 {func(d map[string]any) { svc(d, 0)["ports"] = []any{"0"} }, "/aliases/ports/0/ports/0"},
		"port range backwards":      {func(d map[string]any) { svc(d, 0)["ports"] = []any{"90-80"} }, "/aliases/ports/0/ports/0"},
		"ports on icmp":             {func(d map[string]any) { svc(d, 0)["protocol"] = "icmp" }, "/aliases/ports/0/ports"},
		"unknown icmp type": {func(d map[string]any) {
			svc(d, 0)["protocol"] = "icmp"
			delete(svc(d, 0), "ports")
			svc(d, 0)["types"] = []any{"router-advertisement"}
		}, "/aliases/ports/0/types/0"},
		"tcp without ports":      {func(d map[string]any) { delete(svc(d, 0), "ports") }, "/aliases/ports/0/ports"},
		"rule from a port alias": {func(d map[string]any) { fwd(d, 0)["from"] = side(nil, svcDNS) }, "/rules/0/from/addresses/0"},
		"rule zone an address":   {func(d map[string]any) { fwd(d, 0)["from"] = side([]any{adrHost}) }, "/rules/0/from/zones/0"},
		"rule from a missing zone": {func(d map[string]any) {
			fwd(d, 0)["from"] = side([]any{"zon_00000009-0000-4000-8000-000000000009"})
		}, "/rules/0/from/zones/0"},
		"rule port a zone":  {func(d map[string]any) { fwd(d, 0)["ports"] = []any{zoneOf(d)} }, "/rules/0/ports/0"},
		"from the firewall": {func(d map[string]any) { fwd(d, 0)["from"] = side([]any{firewallZone}) }, "/rules/0/from/zones/0"},
		"the firewall beside a zone": {func(d map[string]any) {
			fwd(d, 0)["to"] = side([]any{firewallZone, zoneOf(d)})
		}, "/rules/0/to/zones"},
		"not without addresses": {func(d map[string]any) {
			fwd(d, 0)["from"] = map[string]any{"zones": []any{zoneOf(d)}, "addresses": []any{}, "not": true}
		}, "/rules/0/from/not"},
		"too many left out": {func(d map[string]any) {
			left := make([]any, maxLeftOut+1)
			for i := range left {
				left[i] = adrHost
			}
			fwd(d, 0)["from"] = map[string]any{"zones": []any{}, "addresses": left, "not": true}
		}, "/rules/0/from/addresses"},
		"too many left out on both sides": {func(d map[string]any) {
			left := slices.Repeat([]any{adrHost}, maxLeftOut/2+1)
			fwd(d, 0)["from"] = map[string]any{"zones": []any{}, "addresses": left, "not": true}
			fwd(d, 0)["to"] = map[string]any{"zones": []any{}, "addresses": left, "not": true}
		}, "/rules/0/to/addresses"},
		"a side that is a list": {func(d map[string]any) { fwd(d, 0)["from"] = []any{zoneOf(d)} }, "/rules/0/from"},
		"families never meet":   {func(d map[string]any) { fwd(d, 0)["from"] = side(nil, adrHost); fwd(d, 0)["to"] = side(nil, adrNet6) }, "/rules/0/to/addresses"},
		"port alias family never meets": {func(d map[string]any) {
			svc(d, 0)["protocol"] = "icmp"
			delete(svc(d, 0), "ports")
			svc(d, 0)["types"] = []any{"echo-request"}
			fwd(d, 0)["from"], fwd(d, 0)["to"], fwd(d, 0)["ports"] = side(nil, adrNet6), side(nil), []any{svcDNS}
		}, "/rules/0/ports"},
		"unknown action": {func(d map[string]any) { fwd(d, 0)["action"] = "allow" }, "/rules/0/action"},
		"management on nothing": {func(d map[string]any) {
			d["management"].(map[string]any)["segment"] = "seg_00000009-0000-4000-8000-000000000009"
		}, "/management/segment"},
		"management source a zone": {func(d map[string]any) {
			d["management"].(map[string]any)["allowedSources"] = []any{item(d["zones"].([]any), 0)["id"]}
		}, "/management/allowedSources/0"},
		"unknown NAT mode": {func(d map[string]any) { d["nat"].(map[string]any)["outbound"].(map[string]any)["mode"] = "hybrid" }, "/nat/outbound/mode"},
		"rule expanding past the line cap": {func(d map[string]any) {
			rep := func(id string, n int) []any {
				out := make([]any, n)
				for i := range out {
					out[i] = id
				}
				return out
			}
			fwd(d, 0)["from"], fwd(d, 0)["to"], fwd(d, 0)["ports"] = side(nil, rep(adrHost, 17)...), side(nil, rep(adrHost, 16)...), rep(svcDNS, 16)
		}, "/rules/0"},
		// 64 x 64 combinations, each rendered once per family: 8192 lines.
		"rule expanding past the line cap by family": {func(d map[string]any) {
			from, to := make([]any, 64), make([]any, 64)
			for i := range from {
				from[i], to[i] = zoneOf(d), zoneOf(d)
			}
			fwd(d, 0)["from"], fwd(d, 0)["to"], fwd(d, 0)["ports"] = side(from), side(to), []any{}
		}, "/rules/0"},
	} {
		t.Run(name, func(t *testing.T) {
			d := policyFixture(t)
			tc.edit(d)
			_, err := decodeMap(t, d)
			var v validationError
			raw, _ := json.Marshal(err)
			if e, ok := err.(validationError); ok {
				v = e
			}
			for _, f := range v {
				if f.Path == tc.path {
					return
				}
			}
			t.Fatalf("want an error at %s, got %v %s", tc.path, err, raw)
		})
	}
}

// A rule is judged against its group's members as they are now, not as they were when the rule was saved.
func TestRuleRevalidatedWhenGroupChanges(t *testing.T) {
	d := policyFixture(t)
	fwd := item(d["rules"].([]any), 0)
	fwd["from"], fwd["to"] = side(nil, adrNet6), side(nil, adrGroup)
	// servers holds an IPv4 host; v6net is IPv6: the rule can never match.
	if _, err := decodeMap(t, d); err == nil {
		t.Fatal("a rule whose families never meet was accepted")
	}
	item(d["aliases"].(map[string]any)["addresses"].([]any), 0)["members"] = []any{adrNet6}
	if _, err := decodeMap(t, d); err != nil {
		t.Fatalf("after the group changed family: %v", err)
	}
}

// A zone says where traffic arrives, not which family it is: only the addresses of the two sides must meet.
func TestZoneDoesNotGiveASideAFamily(t *testing.T) {
	d := policyFixture(t)
	r := item(d["rules"].([]any), 0)
	zone := item(d["zones"].([]any), 0)["id"]
	r["from"], r["to"] = side([]any{zone}), side(nil, adrNet6)
	if _, err := decodeMap(t, d); err != nil {
		t.Fatalf("a zone with every address refused beside an IPv6 destination: %v", err)
	}
	r["from"] = side([]any{zone}, adrHost)
	if _, err := decodeMap(t, d); err == nil {
		t.Fatal("an IPv4 host in a zone was accepted towards an IPv6 network")
	}
	// "Not these" leaves every other address of either family.
	r["from"] = map[string]any{"zones": []any{zone}, "addresses": []any{adrHost}, "not": true}
	if _, err := decodeMap(t, d); err != nil {
		t.Fatalf("every address but an IPv4 host refused towards an IPv6 network: %v", err)
	}
}

// A side that leaves addresses out is one filter line whatever it names, so it does not count towards the cap.
func TestLeftOutSideIsOneLine(t *testing.T) {
	d := policyFixture(t)
	r := item(d["rules"].([]any), 0)
	left, to := make([]any, maxLeftOut), make([]any, 300)
	for i := range left {
		left[i] = adrHost
	}
	for i := range to {
		to[i] = adrHost
	}
	r["from"], r["to"], r["ports"] = map[string]any{"zones": []any{}, "addresses": left, "not": true}, side(nil, to...), []any{}
	if _, err := decodeMap(t, d); err != nil {
		t.Fatal(err)
	}
}

// Rules need not have names of their own: two lists became one, and a rule's place tells it apart.
func TestRulesMayShareAName(t *testing.T) {
	d := policyFixture(t)
	item(d["rules"].([]any), 1)["name"] = "b"
	if _, err := decodeMap(t, d); err != nil {
		t.Fatal(err)
	}
	item(d["rules"].([]any), 1)["id"] = rulB
	if _, err := decodeMap(t, d); err == nil {
		t.Fatal("two rules with one ID were accepted")
	}
}

// A rule to the firewall may name which of the firewall's addresses, and the sides are kept as sets.
func TestRuleToTheFirewall(t *testing.T) {
	d := policyFixture(t)
	zone := item(d["zones"].([]any), 0)["id"]
	r := item(d["rules"].([]any), 0)
	r["from"], r["to"] = side([]any{zone}, adrGroup, adrHost), side([]any{firewallZone}, adrHost)
	got, err := decodeMap(t, d)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Rules[0].toFirewall() || got.Rules[1].toFirewall() {
		t.Fatalf("destinations: %+v", got.Rules)
	}
	if got.Rules[0].From.Addresses[0] != adrHost {
		t.Fatalf("a side's addresses are not sorted: %v", got.Rules[0].From.Addresses)
	}
}

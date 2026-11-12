package config

import (
	"encoding/json"
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
)

// policyFixture is the network fixture plus filter objects, rules, outbound NAT and management.
func policyFixture(t *testing.T) map[string]any {
	t.Helper()
	d := fixtureMap(t)
	zone := item(d["zones"].([]any), 0)["id"].(string)
	seg := item(segmentsOf(d), 0)["id"].(string)
	d["objects"] = map[string]any{
		"addresses": []any{
			// Saved out of ID order: the store sorts objects, never rules.
			map[string]any{"id": adrGroup, "name": "servers", "kind": "group", "members": []any{adrHost}},
			map[string]any{"id": adrHost, "name": "nas", "kind": "host", "address": "192.0.2.10"},
			map[string]any{"id": adrNet6, "name": "v6net", "kind": "network", "prefix": "2001:db8::/64"},
		},
		"services": []any{
			map[string]any{"id": svcDNS, "name": "dns", "protocol": "udp", "ports": []any{"53"}},
			map[string]any{"id": svcGroup, "name": "web", "protocol": "group", "members": []any{svcDNS}},
		},
	}
	d["policy"] = map[string]any{
		"input": []any{},
		"forward": []any{
			map[string]any{"id": rulB, "name": "b", "enabled": true, "action": "accept", "from": []any{zone}, "to": []any{adrGroup}, "services": []any{svcGroup}, "log": false},
			map[string]any{"id": rulA, "name": "a", "enabled": false, "action": "drop", "from": []any{}, "to": []any{}, "services": []any{}, "log": true},
		},
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
	if d.Policy.Forward[0].ID != rulB || d.Policy.Forward[1].ID != rulA {
		t.Fatalf("rule order changed: %s, %s", d.Policy.Forward[0].ID, d.Policy.Forward[1].ID)
	}
	if d.Objects.Addresses[0].ID != adrHost || d.Objects.Addresses[2].ID != adrGroup {
		t.Fatal("addresses not sorted by id")
	}
	// An empty filter section reads back as absent, so an untouched declaration keeps its form.
	plain, err := decodeMap(t, fixtureMap(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := canonical(plain)
	for _, key := range []string{`"objects"`, `"policy"`, `"nat"`, `"management"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("%s appeared in a declaration without a filter: %s", key, raw)
		}
	}
}

func TestPolicyValidation(t *testing.T) {
	type edit func(d map[string]any)
	addr := func(d map[string]any, i int) map[string]any {
		return item(d["objects"].(map[string]any)["addresses"].([]any), i)
	}
	svc := func(d map[string]any, i int) map[string]any {
		return item(d["objects"].(map[string]any)["services"].([]any), i)
	}
	fwd := func(d map[string]any, i int) map[string]any {
		return item(d["policy"].(map[string]any)["forward"].([]any), i)
	}
	for name, tc := range map[string]struct {
		edit edit
		path string
	}{
		"host not an address":    {func(d map[string]any) { addr(d, 1)["address"] = "nas.example" }, "/objects/addresses/1/address"},
		"network with host bits": {func(d map[string]any) { addr(d, 2)["prefix"] = "2001:db8::1/64" }, "/objects/addresses/2/prefix"},
		"range backwards": {func(d map[string]any) {
			addr(d, 1)["kind"] = "range"
			delete(addr(d, 1), "address")
			addr(d, 1)["first"] = "192.0.2.9"
			addr(d, 1)["last"] = "192.0.2.1"
		}, "/objects/addresses/1/last"},
		"range across families": {func(d map[string]any) {
			addr(d, 1)["kind"] = "range"
			delete(addr(d, 1), "address")
			addr(d, 1)["first"] = "192.0.2.1"
			addr(d, 1)["last"] = "2001:db8::1"
		}, "/objects/addresses/1/last"},
		"group of a group":          {func(d map[string]any) { addr(d, 0)["members"] = []any{adrGroup} }, "/objects/addresses/0/members/0"},
		"empty group":               {func(d map[string]any) { addr(d, 0)["members"] = []any{} }, "/objects/addresses/0/members"},
		"group of a missing member": {func(d map[string]any) { addr(d, 0)["members"] = []any{"adr_00000009-0000-4000-8000-000000000009"} }, "/objects/addresses/0/members/0"},
		"port zero":                 {func(d map[string]any) { svc(d, 0)["ports"] = []any{"0"} }, "/objects/services/0/ports/0"},
		"port range backwards":      {func(d map[string]any) { svc(d, 0)["ports"] = []any{"90-80"} }, "/objects/services/0/ports/0"},
		"ports on icmp":             {func(d map[string]any) { svc(d, 0)["protocol"] = "icmp" }, "/objects/services/0/ports"},
		"unknown icmp type": {func(d map[string]any) {
			svc(d, 0)["protocol"] = "icmp"
			delete(svc(d, 0), "ports")
			svc(d, 0)["types"] = []any{"router-advertisement"}
		}, "/objects/services/0/types/0"},
		"tcp without ports":      {func(d map[string]any) { delete(svc(d, 0), "ports") }, "/objects/services/0/ports"},
		"rule from a service":    {func(d map[string]any) { fwd(d, 0)["from"] = []any{svcDNS} }, "/policy/forward/0/from/0"},
		"rule service is a zone": {func(d map[string]any) { fwd(d, 0)["services"] = []any{item(d["zones"].([]any), 0)["id"]} }, "/policy/forward/0/services/0"},
		"input rule with a to": {func(d map[string]any) {
			d["policy"].(map[string]any)["input"] = []any{map[string]any{"id": "rul_00000003-0000-4000-8000-000000000003", "name": "c", "enabled": true, "action": "accept", "from": []any{}, "to": []any{adrHost}, "services": []any{}, "log": false}}
		}, "/policy/input/0/to"},
		"families never meet": {func(d map[string]any) { fwd(d, 0)["from"] = []any{adrHost}; fwd(d, 0)["to"] = []any{adrNet6} }, "/policy/forward/0/to"},
		"service family never meets": {func(d map[string]any) {
			svc(d, 0)["protocol"] = "icmp"
			delete(svc(d, 0), "ports")
			svc(d, 0)["types"] = []any{"echo-request"}
			fwd(d, 0)["from"], fwd(d, 0)["to"], fwd(d, 0)["services"] = []any{adrNet6}, []any{}, []any{svcDNS}
		}, "/policy/forward/0/services"},
		"duplicate rule name": {func(d map[string]any) { fwd(d, 1)["name"] = "b" }, "/policy/forward/1/name"},
		"unknown action":      {func(d map[string]any) { fwd(d, 0)["action"] = "allow" }, "/policy/forward/0/action"},
		"management on nothing": {func(d map[string]any) {
			d["management"].(map[string]any)["segment"] = "seg_00000009-0000-4000-8000-000000000009"
		}, "/management/segment"},
		"management source a zone": {func(d map[string]any) {
			d["management"].(map[string]any)["allowedSources"] = []any{item(d["zones"].([]any), 0)["id"]}
		}, "/management/allowedSources/0"},
		"unknown NAT mode": {func(d map[string]any) { d["nat"].(map[string]any)["outbound"].(map[string]any)["mode"] = "hybrid" }, "/nat/outbound/mode"},
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
	fwd := item(d["policy"].(map[string]any)["forward"].([]any), 0)
	fwd["from"], fwd["to"] = []any{adrNet6}, []any{adrGroup}
	// servers holds an IPv4 host; v6net is IPv6: the rule can never match.
	if _, err := decodeMap(t, d); err == nil {
		t.Fatal("a rule whose families never meet was accepted")
	}
	item(d["objects"].(map[string]any)["addresses"].([]any), 0)["members"] = []any{adrNet6}
	if _, err := decodeMap(t, d); err != nil {
		t.Fatalf("after the group changed family: %v", err)
	}
}

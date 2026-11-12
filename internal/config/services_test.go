package config

import (
	"encoding/json"
	"strings"
	"testing"
)

const dhpLAN = "dhp_00000001-0000-4000-8000-000000000001"

// servicesFixture serves the fixture's static LAN segment (192.0.2.7/24) with DHCP and DNS.
func servicesFixture(t *testing.T) map[string]any {
	t.Helper()
	d := fixtureMap(t)
	lan := item(segmentsOf(d), 1)["id"].(string)
	d["dhcp"] = []any{map[string]any{
		"id": dhpLAN, "name": "lan", "segment": lan,
		"ranges":       []any{map[string]any{"first": "192.0.2.100", "last": "192.0.2.199"}},
		"staticLeases": []any{map[string]any{"mac": "02:00:00:00:00:10", "address": "192.0.2.10", "hostname": "nas"}},
	}}
	d["dns"] = map[string]any{"mode": "recursive", "segments": []any{lan}}
	return d
}

func TestServiceValidation(t *testing.T) {
	entry := func(d map[string]any) map[string]any { return item(d["dhcp"].([]any), 0) }
	dns := func(d map[string]any) map[string]any { return d["dns"].(map[string]any) }
	wan := func(d map[string]any) string { return item(segmentsOf(d), 0)["id"].(string) }
	lease := func(d map[string]any) map[string]any { return item(entry(d)["staticLeases"].([]any), 0) }
	for name, tc := range map[string]struct {
		edit func(d map[string]any)
		path string
	}{
		"unknown segment":        {func(d map[string]any) { entry(d)["segment"] = "seg_00000009-0000-4000-8000-000000000009" }, "/dhcp/0/segment"},
		"segment without static": {func(d map[string]any) { entry(d)["segment"] = wan(d) }, "/dhcp/0/segment"},
		"second entry, same segment": {func(d map[string]any) {
			second := map[string]any{"id": "dhp_00000002-0000-4000-8000-000000000002", "name": "lan2", "segment": entry(d)["segment"],
				"ranges": []any{map[string]any{"first": "192.0.2.50", "last": "192.0.2.60"}}}
			d["dhcp"] = append(d["dhcp"].([]any), second)
		}, "/dhcp/1/segment"},
		"range outside the subnet": {func(d map[string]any) { item(entry(d)["ranges"].([]any), 0)["last"] = "192.0.3.10" }, "/dhcp/0/ranges/0"},
		"range holds own address":  {func(d map[string]any) { item(entry(d)["ranges"].([]any), 0)["first"] = "192.0.2.5" }, "/dhcp/0/ranges/0"},
		"range backwards":          {func(d map[string]any) { item(entry(d)["ranges"].([]any), 0)["first"] = "192.0.2.200" }, "/dhcp/0/ranges/0"},
		"ranges overlap": {func(d map[string]any) {
			entry(d)["ranges"] = append(entry(d)["ranges"].([]any), map[string]any{"first": "192.0.2.150", "last": "192.0.2.160"})
		}, "/dhcp/0/ranges/1"},
		"lease time too short":      {func(d map[string]any) { entry(d)["leaseTime"] = 299 }, "/dhcp/0/leaseTime"},
		"lease time too long":       {func(d map[string]any) { entry(d)["leaseTime"] = 604801 }, "/dhcp/0/leaseTime"},
		"static lease outside":      {func(d map[string]any) { lease(d)["address"] = "198.51.100.1" }, "/dhcp/0/staticLeases/0/address"},
		"static lease on own":       {func(d map[string]any) { lease(d)["address"] = "192.0.2.7" }, "/dhcp/0/staticLeases/0/address"},
		"static lease hostname dot": {func(d map[string]any) { lease(d)["hostname"] = "a.b" }, "/dhcp/0/staticLeases/0/hostname"},
		"duplicate MAC": {func(d map[string]any) {
			entry(d)["staticLeases"] = append(entry(d)["staticLeases"].([]any), map[string]any{"mac": "02:00:00:00:00:10", "address": "192.0.2.11"})
		}, "/dhcp/0/staticLeases/1/mac"},
		"duplicate address": {func(d map[string]any) {
			entry(d)["staticLeases"] = append(entry(d)["staticLeases"].([]any), map[string]any{"mac": "02:00:00:00:00:11", "address": "192.0.2.10"})
		}, "/dhcp/0/staticLeases/1/address"},
		"forward without upstreams": {func(d map[string]any) { dns(d)["mode"] = "forward" }, "/dns/upstreams"},
		"recursive with upstreams": {func(d map[string]any) {
			dns(d)["upstreams"] = []any{map[string]any{"address": "192.0.2.53", "protocol": "do53"}}
		}, "/dns/upstreams"},
		"dot without tlsName": {func(d map[string]any) {
			dns(d)["mode"] = "forward"
			dns(d)["upstreams"] = []any{map[string]any{"address": "192.0.2.53", "protocol": "dot"}}
		}, "/dns/upstreams/0/tlsName"},
		"mixed protocols": {func(d map[string]any) {
			dns(d)["mode"] = "forward"
			dns(d)["upstreams"] = []any{map[string]any{"address": "192.0.2.53", "protocol": "do53"}, map[string]any{"address": "192.0.2.54", "protocol": "dot", "tlsName": "dns.example"}}
		}, "/dns/upstreams"},
		"doh reserved": {func(d map[string]any) {
			dns(d)["mode"] = "forward"
			dns(d)["upstreams"] = []any{map[string]any{"address": "192.0.2.53", "protocol": "doh"}}
		}, "/dns/upstreams/0/protocol"},
		"upstream not an address": {func(d map[string]any) {
			dns(d)["mode"] = "forward"
			dns(d)["upstreams"] = []any{map[string]any{"address": "dns.example", "protocol": "do53"}}
		}, "/dns/upstreams/0/address"},
		"dns on a segment without static": {func(d map[string]any) { dns(d)["segments"] = []any{wan(d)} }, "/dns/segments/0"},
		"bad local domain":                {func(d map[string]any) { dns(d)["localDomain"] = "bad..name" }, "/dns/localDomain"},
		"host DNS beside the service": {func(d map[string]any) {
			item(segmentsOf(d), 1)["addressing"].(map[string]any)["dns"] = map[string]any{"servers": []any{"192.0.2.53"}}
		}, "/segments/1/addressing/dns"},
	} {
		t.Run(name, func(t *testing.T) {
			d := servicesFixture(t)
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

func TestServiceDefaults(t *testing.T) {
	d := servicesFixture(t)
	d["dns"].(map[string]any)["segments"] = []any{}
	got, err := decodeMap(t, d)
	if err != nil {
		t.Fatal(err)
	}
	normalizeDeclaration(&got)
	if got.DHCP[0].LeaseTime != 7200 || got.DNS.LocalDomain != "home.arpa" || got.DNS.RegisterLeases == nil || !*got.DNS.RegisterLeases {
		t.Fatalf("defaults %+v %+v", got.DHCP[0], got.DNS)
	}
}

func TestServiceStoreVersions(t *testing.T) {
	s := seededState(t)
	lan := s.Config.Segments[1].ID
	if v := versionOf(&s, listKey("dns")).Version; v != "0" {
		t.Fatalf("absent dns at version %q", v)
	}
	p := mustRun(t, &s, `{"operations":[
		{"op":"create","collection":"dhcp","localId":"lan","value":{"name":"lan","segment":"`+lan+`","ranges":[{"first":"192.0.2.100","last":"192.0.2.199"}]}},
		{"op":"replace","collection":"dns","expectedVersion":"0","value":{"mode":"recursive","segments":["`+lan+`"]}}]}`)
	id := p.Created["lan"]
	if !strings.HasPrefix(id, "dhp_") || s.Config.DHCP[0].LeaseTime != 7200 || s.Config.DNS == nil || versionOf(&s, listKey("dns")).Version != s.Version {
		t.Fatalf("created %q, %+v, dns %+v", id, s.Config.DHCP, s.Config.DNS)
	}
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"dhcp","id":"`+id+`","expectedVersion":"`+string(s.Version)+`","value":{"name":"lan","segment":"`+lan+`","ranges":[{"first":"192.0.2.150","last":"192.0.2.199"}],"leaseTime":3600}}]}`)
	if s.Config.DHCP[0].LeaseTime != 3600 {
		t.Fatalf("replace lost: %+v", s.Config.DHCP[0])
	}
	// A served segment cannot go while DHCP or DNS names it.
	for _, batch := range []string{
		`{"operations":[{"op":"delete","collection":"segments","id":"` + lan + `","expectedVersion":"7"}]}`,
	} {
		if _, e := run(t, &s, batch); e == nil {
			t.Fatal("deleted a served segment")
		}
	}
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"dhcp","id":"`+id+`","expectedVersion":"`+string(s.Version)+`"},{"op":"delete","collection":"dns","expectedVersion":"`+string(versionOf(&s, listKey("dns")).Version)+`"}]}`)
	if len(s.Config.DHCP) != 0 || s.Config.DNS != nil {
		t.Fatalf("not deleted: %+v %+v", s.Config.DHCP, s.Config.DNS)
	}
}

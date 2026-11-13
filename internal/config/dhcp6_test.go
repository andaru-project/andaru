package config

import (
	"strings"
	"testing"
)

const dh6LAN = "dh6_00000001-0000-4000-8000-000000000001"

// dhcp6Fixture is the services fixture with the LAN on a static unique local /64, served by DHCPv6.
func dhcp6Fixture(t *testing.T, fields map[string]any) map[string]any {
	t.Helper()
	d := servicesFixture(t)
	item(segmentsOf(d), 1)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "static", "addresses": []any{"fd00:1::1/64"}}
	e := map[string]any{"id": dh6LAN, "name": "lan6", "segment": item(segmentsOf(d), 1)["id"],
		"ranges":           []any{map[string]any{"first": "fd00:1::1000", "last": "fd00:1::1fff"}},
		"staticLeases":     []any{map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:ff", "address": "fd00:1::10", "prefix": "fd00:beef:1:100::/56", "hostname": "nas"}},
		"prefixDelegation": map[string]any{"prefix": "fd00:beef:1::/48", "delegatedLength": 56}}
	for k, v := range fields {
		if v == nil {
			delete(e, k)
		} else {
			e[k] = v
		}
	}
	d["dhcp6"] = []any{e}
	return d
}

func TestDHCPv6Validation(t *testing.T) {
	const p = "/dhcp6/0"
	got := accepted(t, dhcp6Fixture(t, nil))
	if len(got.DHCP6) != 1 || got.DHCP6[0].PrefixDelegation == nil {
		t.Fatalf("decoded %+v", got.DHCP6)
	}
	accepted(t, dhcp6Fixture(t, map[string]any{"staticLeases": nil, "prefixDelegation": nil}))
	// The DHCPv4 fixture's static lease is named "nas" too: one device, both families.
	lease := func(fields map[string]any) map[string]any {
		l := map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:ff", "address": "fd00:1::10"}
		for k, v := range fields {
			if v == nil {
				delete(l, k)
			} else {
				l[k] = v
			}
		}
		return l
	}
	for name, tc := range map[string]struct {
		fields map[string]any
		path   string
	}{
		"unknown segment":       {map[string]any{"segment": "seg_00000009-0000-4000-8000-000000000009"}, p + "/segment"},
		"range outside":         {map[string]any{"ranges": []any{map[string]any{"first": "fd00:2::1", "last": "fd00:2::9"}}}, p + "/ranges/0"},
		"range holds own":       {map[string]any{"ranges": []any{map[string]any{"first": "fd00:1::", "last": "fd00:1::9"}}}, p + "/ranges/0"},
		"short lease":           {map[string]any{"leaseTime": 299}, p + "/leaseTime"},
		"IPv4 DNS server":       {map[string]any{"dnsServers": []any{"192.0.2.53"}}, p + "/dnsServers/0"},
		"bad duid":              {map[string]any{"staticLeases": []any{lease(map[string]any{"duid": "00:04"})}}, p + "/staticLeases/0/duid"},
		"duplicate duid":        {map[string]any{"staticLeases": []any{lease(nil), lease(map[string]any{"address": "fd00:1::11"})}}, p + "/staticLeases/1/duid"},
		"nothing leased":        {map[string]any{"staticLeases": []any{lease(map[string]any{"address": nil})}}, p + "/staticLeases/0"},
		"prefix outside pool":   {map[string]any{"staticLeases": []any{lease(map[string]any{"prefix": "fd00:dead::/56"})}}, p + "/staticLeases/0/prefix"},
		"prefix wrong length":   {map[string]any{"staticLeases": []any{lease(map[string]any{"prefix": "fd00:beef:1:100::/60"})}}, p + "/staticLeases/0/prefix"},
		"address in range":      {map[string]any{"staticLeases": []any{lease(map[string]any{"address": "fd00:1::1001"})}}, p + "/staticLeases/0/address"},
		"duplicate hostname":    {map[string]any{"staticLeases": []any{lease(map[string]any{"hostname": "a"}), lease(map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:00", "address": "fd00:1::11", "hostname": "a"})}}, p + "/staticLeases/1/hostname"},
		"delegated 47":          {map[string]any{"prefixDelegation": map[string]any{"prefix": "fd00:beef:1::/48", "delegatedLength": 47}}, p + "/prefixDelegation/delegatedLength"},
		"delegated 65":          {map[string]any{"prefixDelegation": map[string]any{"prefix": "fd00:beef:1::/48", "delegatedLength": 65}}, p + "/prefixDelegation/delegatedLength"},
		"pool overlaps segment": {map[string]any{"staticLeases": nil, "prefixDelegation": map[string]any{"prefix": "fd00::/16", "delegatedLength": 56}}, p + "/prefixDelegation/prefix"},
		"excluded outside":      {map[string]any{"prefixDelegation": map[string]any{"prefix": "fd00:beef:1::/48", "delegatedLength": 56, "excludedPrefix": "fd00:dead::/64"}}, p + "/prefixDelegation/excludedPrefix"},
	} {
		t.Run(name, func(t *testing.T) { refusedAt(t, dhcp6Fixture(t, tc.fields), tc.path) })
	}
	t.Run("segment without a static /64", func(t *testing.T) {
		d := dhcp6Fixture(t, nil)
		item(segmentsOf(d), 1)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "slaac"}
		refusedAt(t, d, p+"/segment")
	})
	t.Run("two entries for one segment", func(t *testing.T) {
		d := dhcp6Fixture(t, map[string]any{"prefixDelegation": nil, "staticLeases": nil})
		second := map[string]any{"id": "dh6_00000002-0000-4000-8000-000000000002", "name": "again", "segment": item(segmentsOf(d), 1)["id"]}
		d["dhcp6"] = append(d["dhcp6"].([]any), second)
		refusedAt(t, d, "/dhcp6/1/segment")
	})
}

func TestDHCPv6StoreVersions(t *testing.T) {
	s := seededState(t)
	lan := s.Config.Segments[1].ID
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"segments","id":"`+lan+`","expectedVersion":"7","value":{"name":"lan","link":"`+s.Config.Segments[1].Link+`","zone":"`+s.Config.Segments[1].Zone+`","addressing":{"ipv4":{"mode":"static","addresses":["192.0.2.7/24"]},"ipv6":{"mode":"static","addresses":["fd00:1::1/64"]}}}}]}`)
	p := mustRun(t, &s, `{"operations":[{"op":"create","collection":"dhcp6","localId":"d","value":{"name":"lan6","segment":"`+lan+`","ranges":[{"first":"fd00:1::1000","last":"fd00:1::1fff"}]}}]}`)
	id := p.Created["d"]
	if !strings.HasPrefix(id, "dh6_") || len(s.Config.DHCP6) != 1 || s.Config.DHCP6[0].LeaseTime != 7200 {
		t.Fatalf("created %q %+v", id, s.Config.DHCP6)
	}
	if _, e := run(t, &s, `{"operations":[{"op":"delete","collection":"segments","id":"`+lan+`","expectedVersion":"`+string(versionOf(&s, objectKey{"segments", lan}).Version)+`"}]}`); e == nil {
		t.Fatal("deleted a segment DHCPv6 serves")
	}
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"dhcp6","id":"`+id+`","expectedVersion":"`+string(versionOf(&s, objectKey{"dhcp6", id}).Version)+`"}]}`)
	if len(s.Config.DHCP6) != 0 {
		t.Fatal("not deleted")
	}
}

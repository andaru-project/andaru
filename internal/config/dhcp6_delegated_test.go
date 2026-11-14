package config

import "testing"

// delegatedFixture serves DHCPv6 on the fixture's delegated LAN (subnet 1 of the WAN's /48 request).
func delegatedFixture(t *testing.T, fields map[string]any) map[string]any {
	t.Helper()
	d := servicesFixture(t)
	v6(d, 1)["subnetId"] = "1"
	e := map[string]any{"id": dh6LAN, "name": "lan6", "segment": item(segmentsOf(d), 1)["id"],
		"ranges":           []any{map[string]any{"first": "::1000", "last": "::1fff"}},
		"staticLeases":     []any{map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:ff", "address": "::10", "prefixId": "1", "hostname": "router"}},
		"prefixDelegation": map[string]any{"subnetId": "8", "length": 60, "delegatedLength": 64}}
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

func TestDHCPv6OnDelegated(t *testing.T) {
	const p = "/dhcp6/0"
	got := accepted(t, delegatedFixture(t, nil))
	if pd := got.DHCP6[0].PrefixDelegation; pd == nil || pd.SubnetID != "8" || pd.Length != 60 || got.DHCP6[0].StaticLeases[0].PrefixID != "1" {
		t.Fatalf("decoded %+v", got.DHCP6)
	}
	lease := func(fields map[string]any) []any {
		l := map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:ff", "address": "::10"}
		for k, v := range fields {
			l[k] = v
		}
		return []any{l}
	}
	pool := func(subnetID string, length, delegated int) map[string]any {
		return map[string]any{"subnetId": subnetID, "length": length, "delegatedLength": delegated}
	}
	for name, tc := range map[string]struct {
		fields map[string]any
		path   string
	}{
		"range not a suffix":    {map[string]any{"ranges": []any{map[string]any{"first": "2001:db8::1000", "last": "::1fff"}}}, p + "/ranges/0/first"},
		"address not a suffix":  {map[string]any{"staticLeases": lease(map[string]any{"address": "2001:db8::10"})}, p + "/staticLeases/0/address"},
		"literal static prefix": {map[string]any{"staticLeases": lease(map[string]any{"prefix": "2001:db8:0:81::/64"})}, p + "/staticLeases/0/prefix"},
		"literal pool prefix":   {map[string]any{"prefixDelegation": map[string]any{"prefix": "2001:db8::/48", "subnetId": "8", "length": 60, "delegatedLength": 64}}, p + "/prefixDelegation/prefix"},
		"excluded prefix":       {map[string]any{"prefixDelegation": map[string]any{"subnetId": "8", "length": 60, "delegatedLength": 64, "excludedPrefix": "::/64"}}, p + "/prefixDelegation/excludedPrefix"},
		"pool not longer":       {map[string]any{"staticLeases": nil, "prefixDelegation": pool("0", 48, 64)}, p + "/prefixDelegation/length"},
		"pool 65":               {map[string]any{"staticLeases": nil, "prefixDelegation": pool("0", 65, 65)}, p + "/prefixDelegation/length"},
		"delegated below pool":  {map[string]any{"staticLeases": nil, "prefixDelegation": pool("8", 60, 56)}, p + "/prefixDelegation/delegatedLength"},
		"pool id too wide":      {map[string]any{"staticLeases": nil, "prefixDelegation": pool("10000", 60, 64)}, p + "/prefixDelegation/subnetId"},
		"prefix id too wide":    {map[string]any{"staticLeases": lease(map[string]any{"prefixId": "10"})}, p + "/staticLeases/0/prefixId"},
		"pool covers the LAN":   {map[string]any{"staticLeases": nil, "prefixDelegation": pool("0", 60, 64)}, p + "/prefixDelegation/subnetId"},
		"overlapping ranges":    {map[string]any{"ranges": []any{map[string]any{"first": "::1000", "last": "::1fff"}, map[string]any{"first": "::1800", "last": "::2fff"}}}, p + "/ranges/1"},
		"address in a range":    {map[string]any{"staticLeases": lease(map[string]any{"address": "::1001"})}, p + "/staticLeases/0/address"},
		"duplicate address": {map[string]any{"staticLeases": []any{
			map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:ff", "address": "::10"},
			map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:00", "address": "::10"}}}, p + "/staticLeases/1/address"},
		"duplicate prefix id": {map[string]any{"staticLeases": []any{
			map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:ff", "prefixId": "1"},
			map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:00", "prefixId": "1"}}}, p + "/staticLeases/1/prefixId"},
	} {
		t.Run(name, func(t *testing.T) { refusedAt(t, delegatedFixture(t, tc.fields), tc.path) })
	}
	t.Run("pools overlap", func(t *testing.T) {
		d := delegatedFixture(t, map[string]any{"staticLeases": nil})
		addSegment(d, "3", "lnk_00000000-0000-4000-8000-000000000004", map[string]any{"mode": "none"},
			map[string]any{"mode": "delegated", "from": item(segmentsOf(d), 0)["id"], "subnetId": "2"})
		second := map[string]any{"id": "dh6_00000002-0000-4000-8000-000000000002", "name": "lan6b", "segment": "seg_00000000-0000-4000-8000-000000000003",
			"prefixDelegation": map[string]any{"subnetId": "8", "length": 60, "delegatedLength": 64}}
		d["dhcp6"] = append(d["dhcp6"].([]any), second)
		refusedAt(t, d, "/dhcp6/1/prefixDelegation/subnetId")
	})
	t.Run("pool without a hint", func(t *testing.T) {
		d := delegatedFixture(t, map[string]any{"staticLeases": nil})
		delete(v6(d, 0), "requestPrefixLength")
		v6(d, 1)["subnetId"] = "0"
		refusedAt(t, d, p+"/prefixDelegation/length")
	})
}

// Pool and prefix positions are stored as segment subnet IDs are: lowercase hexadecimal without leading zeros.
func TestDelegatedPositionsCanonical(t *testing.T) {
	got := accepted(t, delegatedFixture(t, map[string]any{
		"staticLeases":     []any{map[string]any{"duid": "00:01:00:01:aa:bb:cc:dd:ee:ff", "prefixId": "0F"}},
		"prefixDelegation": map[string]any{"subnetId": "08A", "length": 60, "delegatedLength": 64}}))
	if e := got.DHCP6[0]; e.PrefixDelegation.SubnetID != "8a" || e.StaticLeases[0].PrefixID != "f" {
		t.Fatalf("stored %q and %q", e.PrefixDelegation.SubnetID, e.StaticLeases[0].PrefixID)
	}
}

func TestDelegatedSubnetIDWidth(t *testing.T) {
	d := fixtureMap(t)
	v6(d, 1)["subnetId"] = "100"
	v6(d, 0)["requestPrefixLength"] = 56
	_, e := decodeMap(t, d)
	if ve, _ := e.(validationError); len(ve) == 0 || ve[0].Message != "Expected 0 to ff for a /56 request." {
		t.Fatalf("subnet 100 on a /56 request: %v", e)
	}
	v6(d, 0)["requestPrefixLength"] = 48
	if _, e := decodeMap(t, d); e != nil {
		t.Fatalf("subnet 100 on a /48 request refused: %v", e)
	}
}

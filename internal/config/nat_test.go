package config

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

const (
	pfwA     = "pfw_00000001-0000-4000-8000-000000000001"
	pfwB     = "pfw_00000002-0000-4000-8000-000000000002"
	otoA     = "oto_00000001-0000-4000-8000-000000000001"
	otoB     = "oto_00000002-0000-4000-8000-000000000002"
	natPeer  = "adr_00000001-0000-4000-8000-000000000001"
	natOther = "adr_00000002-0000-4000-8000-000000000002"
)

// natFixture is the network fixture with two address objects; the WAN zone is item 0, the LAN segment
// holds 192.0.2.7/24.
func natFixture(t *testing.T) map[string]any {
	t.Helper()
	d := fixtureMap(t)
	d["objects"] = map[string]any{"addresses": []any{
		map[string]any{"id": natPeer, "name": "peer", "kind": "host", "address": "198.51.100.7"},
		map[string]any{"id": natOther, "name": "other", "kind": "host", "address": "198.51.100.8"},
	}, "services": []any{}}
	return d
}

func wanZone(d map[string]any) string { return item(d["zones"].([]any), 0)["id"].(string) }

func forward(d map[string]any, fields map[string]any) map[string]any {
	f := map[string]any{"id": pfwA, "name": "web", "enabled": true, "ingressZones": []any{wanZone(d)}, "protocol": "tcp",
		"from": []any{}, "to": map[string]any{"self": true}, "ports": "8080", "target": map[string]any{"address": "192.0.2.10"}, "targetPort": 80}
	for k, v := range fields {
		if v == nil {
			delete(f, k)
		} else {
			f[k] = v
		}
	}
	return f
}

func setNAT(d map[string]any, list string, entries ...any) {
	nat, ok := d["nat"].(map[string]any)
	if !ok {
		nat = map[string]any{"outbound": map[string]any{"mode": "none"}}
		d["nat"] = nat
	}
	nat[list] = entries
}

func refusedAt(t *testing.T, d map[string]any, path string) {
	t.Helper()
	_, e := decodeMap(t, d)
	ve, _ := e.(validationError)
	if !slices.ContainsFunc(ve, func(f FieldError) bool { return f.Path == path }) {
		t.Fatalf("error %v; fields %+v; want %s", e, ve, path)
	}
}

func accepted(t *testing.T, d map[string]any) declaration {
	t.Helper()
	got, e := decodeMap(t, d)
	if e != nil {
		t.Fatalf("refused: %v %+v", e, []FieldError(func() validationError { v, _ := e.(validationError); return v }()))
	}
	return got
}

func TestPortForwardValidation(t *testing.T) {
	const p = "/nat/portForwards/0"
	for name, entries := range map[string][]func(map[string]any) map[string]any{
		"self to a host": {func(d map[string]any) map[string]any { return forward(d, nil) }},
		"address to": {func(d map[string]any) map[string]any {
			return forward(d, map[string]any{"to": map[string]any{"address": natOther}})
		}},
		"exclusion": {func(d map[string]any) map[string]any {
			return forward(d, map[string]any{"noRedirect": true, "target": nil, "targetPort": nil, "from": []any{natPeer}})
		}},
		"to the firewall": {func(d map[string]any) map[string]any {
			return forward(d, map[string]any{"target": map[string]any{"self": true}, "targetPort": 22})
		}},
		"forwards differing only in source": {
			func(d map[string]any) map[string]any { return forward(d, map[string]any{"from": []any{natPeer}}) },
			func(d map[string]any) map[string]any {
				return forward(d, map[string]any{"id": pfwB, "name": "web2", "from": []any{natOther}})
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := natFixture(t)
			var list []any
			for _, e := range entries {
				list = append(list, e(d))
			}
			setNAT(d, "portForwards", list...)
			got := accepted(t, d)
			if f := got.NAT.PortForwards; len(f) != len(entries) || f[0].FilterRule != map[bool]string{false: "auto", true: ""}[f[0].NoRedirect] {
				t.Fatalf("decoded %+v", got.NAT.PortForwards)
			}
		})
	}
	for name, tc := range map[string]struct {
		fields map[string]any
		path   string
	}{
		"no zone":            {map[string]any{"ingressZones": []any{}}, p + "/ingressZones"},
		"unknown zone":       {map[string]any{"ingressZones": []any{"zon_00000009-0000-4000-8000-000000000009"}}, p + "/ingressZones/0"},
		"port zero":          {map[string]any{"ports": "0"}, p + "/ports"},
		"port too high":      {map[string]any{"ports": "70000"}, p + "/ports"},
		"backwards range":    {map[string]any{"ports": "9-8"}, p + "/ports"},
		"shifted past 65535": {map[string]any{"ports": "1000-1009", "targetPort": 65530}, p + "/targetPort"},
		"loopback target":    {map[string]any{"target": map[string]any{"address": "127.0.0.1"}}, p + "/target/address"},
		"IPv6 target":        {map[string]any{"target": map[string]any{"address": "2001:db8::1"}}, p + "/target/address"},
		"no target":          {map[string]any{"target": nil}, p + "/target"},
		"exclusion target":   {map[string]any{"noRedirect": true}, p + "/target"},
		"unknown from":       {map[string]any{"from": []any{"adr_00000009-0000-4000-8000-000000000009"}}, p + "/from/0"},
		"unknown to":         {map[string]any{"to": map[string]any{"address": "adr_00000009-0000-4000-8000-000000000009"}}, p + "/to/address"},
	} {
		t.Run(name, func(t *testing.T) {
			d := natFixture(t)
			setNAT(d, "portForwards", forward(d, tc.fields))
			refusedAt(t, d, tc.path)
		})
	}
	t.Run("overlap", func(t *testing.T) {
		d := natFixture(t)
		setNAT(d, "portForwards", forward(d, map[string]any{"ports": "8000-8100"}),
			forward(d, map[string]any{"id": pfwB, "name": "web2", "ports": "8080", "target": map[string]any{"address": "192.0.2.11"}}))
		refusedAt(t, d, "/nat/portForwards/1/ports")
	})
}

func mapping(d map[string]any, fields map[string]any) map[string]any {
	o := map[string]any{"id": otoA, "name": "server", "enabled": true, "zone": wanZone(d), "external": "203.0.113.20", "internal": "192.0.2.30", "destination": []any{}}
	for k, v := range fields {
		if v == nil {
			delete(o, k)
		} else {
			o[k] = v
		}
	}
	return o
}

func TestOneToOneValidation(t *testing.T) {
	const p = "/nat/oneToOne/0"
	d := natFixture(t)
	setNAT(d, "oneToOne", mapping(d, nil), mapping(d, map[string]any{"id": otoB, "name": "block", "external": "203.0.113.32/28", "internal": "192.0.2.32/28", "destination": []any{natPeer}}))
	accepted(t, d)
	for name, tc := range map[string]struct {
		fields map[string]any
		path   string
	}{
		"lengths differ":          {map[string]any{"external": "203.0.113.16/28", "internal": "192.0.2.16/29"}, p + "/internal"},
		"IPv6 external":           {map[string]any{"external": "2001:db8::20"}, p + "/external"},
		"external holds own":      {map[string]any{"external": "192.0.2.7"}, p + "/external"},
		"internal holds own":      {map[string]any{"internal": "192.0.2.0/24", "external": "203.0.113.0/24"}, p + "/internal"},
		"host bits in prefix":     {map[string]any{"external": "203.0.113.17/28", "internal": "192.0.2.16/28"}, p + "/external"},
		"exclusion with external": {map[string]any{"noBinat": true}, p + "/external"},
		"unknown zone":            {map[string]any{"zone": "zon_00000009-0000-4000-8000-000000000009"}, p + "/zone"},
		"unknown destination":     {map[string]any{"destination": []any{"adr_00000009-0000-4000-8000-000000000009"}}, p + "/destination/0"},
	} {
		t.Run(name, func(t *testing.T) {
			d := natFixture(t)
			setNAT(d, "oneToOne", mapping(d, tc.fields))
			refusedAt(t, d, tc.path)
		})
	}
	t.Run("overlapping externals", func(t *testing.T) {
		d := natFixture(t)
		setNAT(d, "oneToOne", mapping(d, map[string]any{"external": "203.0.113.16/28", "internal": "192.0.2.16/28"}),
			mapping(d, map[string]any{"id": otoB, "name": "two", "external": "203.0.113.20", "internal": "192.0.2.99"}))
		refusedAt(t, d, "/nat/oneToOne/1/external")
	})
	// An extra address on the WAN is pfSense's IP alias for 1:1: the firewall holds it so ARP is answered.
	t.Run("extra WAN address", func(t *testing.T) {
		d := natFixture(t)
		lan := v4(d, 1)
		lan["addresses"] = []any{"192.0.2.7/24", "192.0.2.8/24"}
		setNAT(d, "oneToOne", mapping(d, map[string]any{"external": "192.0.2.8", "internal": "198.18.0.30"}))
		accepted(t, d)
	})
	t.Run("exclusion", func(t *testing.T) {
		d := natFixture(t)
		setNAT(d, "oneToOne", mapping(d, map[string]any{"id": otoB, "name": "skip", "noBinat": true, "external": nil, "internal": "192.0.2.31"}),
			mapping(d, map[string]any{"external": "203.0.113.16/28", "internal": "192.0.2.16/28"}))
		accepted(t, d)
	})
}

// A zone or address a NAT entry names cannot go while it does.
func TestNATReferencesBlockDeletion(t *testing.T) {
	d := natFixture(t)
	setNAT(d, "portForwards", forward(d, map[string]any{"from": []any{natPeer}}))
	d["objects"].(map[string]any)["addresses"] = d["objects"].(map[string]any)["addresses"].([]any)[1:]
	refusedAt(t, d, "/nat/portForwards/0/from/0")
	d = natFixture(t)
	setNAT(d, "oneToOne", mapping(d, nil))
	d["zones"] = d["zones"].([]any)[1:]
	refusedAt(t, d, "/nat/oneToOne/0/zone")
}

// Shifted ranges render one map entry per port; the whole configuration has a budget.
func TestShiftedRangeBudget(t *testing.T) {
	d := natFixture(t)
	setNAT(d, "portForwards", forward(d, map[string]any{"ports": "1-40000", "targetPort": 2}),
		forward(d, map[string]any{"id": pfwB, "name": "web2", "from": []any{natPeer}, "ports": "1-40000", "targetPort": 2}))
	refusedAt(t, d, "/nat/portForwards/1/targetPort")
	d = natFixture(t)
	setNAT(d, "portForwards", forward(d, map[string]any{"ports": "1-40000", "targetPort": 2}))
	accepted(t, d)
}

// An exclusion is matched only on its internal hosts; conditions it cannot honour are refused.
func TestOneToOneExclusionConditions(t *testing.T) {
	for field, value := range map[string]any{"destination": []any{natPeer}, "reflection": true} {
		d := natFixture(t)
		setNAT(d, "oneToOne", mapping(d, map[string]any{"noBinat": true, "external": nil, "internal": "192.0.2.31", field: value}))
		refusedAt(t, d, "/nat/oneToOne/0/"+field)
	}
}

// Validation runs on every read: many forwards must not make it quadratic in practice.
func TestManyForwardsValidateQuickly(t *testing.T) {
	d := natFixture(t)
	var list []any
	for i := 0; i < 5000; i++ {
		list = append(list, forward(d, map[string]any{"id": fmt.Sprintf("pfw_%08x-0000-4000-8000-000000000001", i+1), "name": fmt.Sprint("f", i), "ports": fmt.Sprint(i + 1)}))
	}
	setNAT(d, "portForwards", list...)
	start := time.Now()
	accepted(t, d)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("validating 5000 forwards took %v", took)
	}
}

// Reflection renders the shifted map again for internal clients, so it counts against the budget too.
func TestShiftedRangeBudgetCountsReflection(t *testing.T) {
	d := natFixture(t)
	setNAT(d, "portForwards", forward(d, map[string]any{"ports": "1-40000", "targetPort": 2, "reflection": true}))
	refusedAt(t, d, "/nat/portForwards/0/targetPort")
	d = natFixture(t)
	setNAT(d, "portForwards", forward(d, map[string]any{"ports": "1-32768", "targetPort": 2, "reflection": true}))
	accepted(t, d)
}

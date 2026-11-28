package config

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	rteA   = "rte_00000001-0000-4000-8000-000000000001"
	rteB   = "rte_00000002-0000-4000-8000-000000000002"
	lanSeg = "seg_00000000-0000-4000-8000-000000000002"
)

func routeFixture(t *testing.T, routes ...map[string]any) map[string]any {
	t.Helper()
	d := fixtureMap(t)
	list := []any{}
	for _, r := range routes {
		list = append(list, r)
	}
	d["routes"] = list
	return d
}

func routeTo(fields map[string]any) map[string]any {
	r := map[string]any{"id": rteA, "name": "office", "destination": "10.20.0.0/24", "target": map[string]any{"kind": "segment", "id": lanSeg}, "gateway": "192.0.2.254", "enabled": true}
	for k, v := range fields {
		if v == nil {
			delete(r, k)
		} else {
			r[k] = v
		}
	}
	return r
}

func TestRouteCanonicalForm(t *testing.T) {
	got := accepted(t, routeFixture(t,
		routeTo(map[string]any{"id": rteB, "destination": "10.20.0.5/24", "gateway": "192.0.2.254", "metric": 100}),
		routeTo(map[string]any{"name": "v6", "destination": "2001:DB8::1/64", "gateway": "2001:db8:0::fe"}),
	))
	if len(got.Routes) != 2 || got.Routes[0].ID != rteA || got.Routes[1].ID != rteB {
		t.Fatalf("routes %+v", got.Routes)
	}
	if r := got.Routes[0]; r.Destination != "2001:db8::/64" || r.Gateway != "2001:db8::fe" {
		t.Fatalf("v6 route %+v", r)
	}
	if r := got.Routes[1]; r.Destination != "10.20.0.0/24" || r.Metric != 100 {
		t.Fatalf("v4 route %+v", r)
	}
	for name, dest := range map[string]string{"default v4": "0.0.0.0/0", "default v6": "::/0", "link-local v6": "fe80::/64", "host": "198.51.100.9/32"} {
		t.Run(name, func(t *testing.T) {
			accepted(t, routeFixture(t, routeTo(map[string]any{"destination": dest, "gateway": nil})))
		})
	}
	if got := accepted(t, routeFixture(t)); got.Routes != nil {
		t.Fatalf("an empty list stays absent: %+v", got.Routes)
	}
}

func TestRouteValidation(t *testing.T) {
	const p = "/routes/0"
	for name, c := range map[string]struct {
		fields map[string]any
		path   string
	}{
		"not a prefix":        {map[string]any{"destination": "10.20.0.0"}, p + "/destination"},
		"bad prefix":          {map[string]any{"destination": "10.20.0.0/33"}, p + "/destination"},
		"mapped":              {map[string]any{"destination": "::ffff:10.0.0.0/120", "gateway": nil}, p + "/destination"},
		"loopback":            {map[string]any{"destination": "127.0.0.0/8", "gateway": nil}, p + "/destination"},
		"loopback inside":     {map[string]any{"destination": "127.1.0.0/16", "gateway": nil}, p + "/destination"},
		"loopback v6":         {map[string]any{"destination": "::1/128", "gateway": nil}, p + "/destination"},
		"multicast":           {map[string]any{"destination": "239.1.0.0/16", "gateway": nil}, p + "/destination"},
		"multicast v6":        {map[string]any{"destination": "ff02::/16", "gateway": nil}, p + "/destination"},
		"family mismatch":     {map[string]any{"gateway": "fd00::1"}, p + "/gateway"},
		"family mismatch v6":  {map[string]any{"destination": "fd00:1::/64"}, p + "/gateway"},
		"gateway not address": {map[string]any{"gateway": "router"}, p + "/gateway"},
		"gateway zone":        {map[string]any{"destination": "fd00:1::/64", "gateway": "fe80::1%eth0"}, p + "/gateway"},
		"gateway unspecified": {map[string]any{"gateway": "0.0.0.0"}, p + "/gateway"},
		"gateway loopback":    {map[string]any{"gateway": "127.0.0.1"}, p + "/gateway"},
		"gateway multicast":   {map[string]any{"gateway": "224.0.0.1"}, p + "/gateway"},
		"gateway broadcast":   {map[string]any{"gateway": "255.255.255.255"}, p + "/gateway"},
		"gateway v6 loopback": {map[string]any{"destination": "fd00:1::/64", "gateway": "::1"}, p + "/gateway"},
		"missing segment":     {map[string]any{"target": map[string]any{"kind": "segment", "id": "seg_00000000-0000-4000-8000-0000000000ff"}}, p + "/target/id"},
		"unregistered kind":   {map[string]any{"target": map[string]any{"kind": "openvpn", "id": "ovp_00000000-0000-4000-8000-000000000001"}}, p + "/target/id"},
		"wireguard gateway":   {map[string]any{"target": map[string]any{"kind": "wireguard", "id": "wg_00000000-0000-4000-8000-000000000001"}}, p + "/gateway"},
		"ipsec gateway":       {map[string]any{"target": map[string]any{"kind": "ipsec", "id": "ips_00000000-0000-4000-8000-000000000001"}}, p + "/gateway"},
		"unknown kind":        {map[string]any{"target": map[string]any{"kind": "tunnel", "id": lanSeg}}, p + "/target/kind"},
		"metric zero":         {map[string]any{"metric": 0}, p + "/metric"},
		"metric too large":    {map[string]any{"metric": 4294967296}, p + "/metric"},
		"enabled left out":    {map[string]any{"enabled": nil}, p},
		"name control":        {map[string]any{"name": "a\nb"}, p + "/name"},
		"unknown field":       {map[string]any{"via": "x"}, p},
		"unresolved label":    {map[string]any{"target": map[string]any{"kind": "segment", "id": "@lan"}}, p + "/target/id"},
	} {
		t.Run(name, func(t *testing.T) { refusedAt(t, routeFixture(t, routeTo(c.fields)), c.path) })
	}
	t.Run("duplicate name", func(t *testing.T) {
		refusedWith(t, routeFixture(t, routeTo(nil), routeTo(map[string]any{"id": rteB, "destination": "10.30.0.0/24"})), "/routes/1/name", "Name already exists")
	})
	t.Run("duplicate tuple", func(t *testing.T) {
		// Another spelling of the same prefix, and the same metric left out.
		d := routeFixture(t, routeTo(nil), routeTo(map[string]any{"id": rteB, "name": "again", "destination": "10.20.0.9/24", "gateway": "192.0.2.253"}))
		refusedWith(t, d, "/routes/1/destination", "Another route has this destination, target and metric.")
	})
	t.Run("same destination, other metric", func(t *testing.T) {
		accepted(t, routeFixture(t, routeTo(nil), routeTo(map[string]any{"id": rteB, "name": "backup", "metric": 200})))
	})
	t.Run("disabled route still checked", func(t *testing.T) {
		refusedAt(t, routeFixture(t, routeTo(map[string]any{"enabled": false, "gateway": "fd00::1"})), p+"/gateway")
	})
}

func TestRouteCollection(t *testing.T) {
	s := seededState(t)
	p := mustRun(t, &s, `{"operations":[{"op":"create","collection":"routes","localId":"office","value":{"name":"office","destination":"10.20.0.5/24","target":{"kind":"segment","id":"`+lanSeg+`"},"gateway":"192.0.2.254","metric":10,"enabled":true}}]}`)
	id := p.Created["office"]
	if !strings.HasPrefix(id, "rte_") || len(s.Config.Routes) != 1 || s.Config.Routes[0].Destination != "10.20.0.0/24" {
		t.Fatalf("created %q: %+v", id, s.Config.Routes)
	}
	// A segment a route names cannot go, enabled or not.
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"routes","id":"`+id+`","expectedVersion":"`+string(s.Version)+`","value":{"name":"office","destination":"10.20.0.0/24","target":{"kind":"segment","id":"`+lanSeg+`"},"enabled":false}}]}`)
	_, e := run(t, &s, `{"operations":[{"op":"delete","collection":"segments","id":"`+lanSeg+`","expectedVersion":"7"}]}`)
	f := requireFault(t, e, "VALIDATION_FAILED")
	if raw, _ := json.Marshal(f); !strings.Contains(string(raw), "Object "+id+" remains referenced or invalid.") {
		t.Fatalf("refusal %s", raw)
	}
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"routes","id":"`+id+`","expectedVersion":"`+string(s.Version)+`"}]}`)
	if s.Config.Routes != nil {
		t.Fatalf("routes left: %+v", s.Config.Routes)
	}
	// Replacing an earlier-ID route to collide with a later-ID route attributes the error to /destination (not /id).
	s = seededState(t)
	c1, _ := ObjectChange("create", "routes", "", nil, []byte(`{"name":"first","destination":"10.20.0.0/24","target":{"kind":"segment","id":"`+lanSeg+`"},"enabled":true}`))
	_ = c1.AssignID(0, rteA)
	p1, err := prepareChanges(&s, c1, "8", fixedTime)
	if err != nil {
		t.Fatal(faultText(err))
	}
	s = p1.State
	c2, _ := ObjectChange("create", "routes", "", nil, []byte(`{"name":"second","destination":"10.30.0.0/24","target":{"kind":"segment","id":"`+lanSeg+`"},"enabled":true}`))
	_ = c2.AssignID(0, rteB)
	p2, err := prepareChanges(&s, c2, "9", fixedTime)
	if err != nil {
		t.Fatal(faultText(err))
	}
	s = p2.State
	vA := s.Versions[objectKey{"routes", rteA}].Version
	dup, _ := ObjectChange("replace", "routes", rteA, &vA, []byte(`{"name":"first","destination":"10.30.0.99/24","target":{"kind":"segment","id":"`+lanSeg+`"},"enabled":true}`))
	_, dupErr := prepareChanges(&s, dup, "10", fixedTime)
	dupFault := requireFault(t, dupErr, "VALIDATION_FAILED")
	if len(dupFault.Details) != 1 || dupFault.Details[0].Path != "/destination" || !strings.Contains(dupFault.Details[0].Message, "Another route") {
		t.Fatalf("duplicate replace error: %+v", dupFault)
	}
	// A route and the segment it names may be created in one batch.
	s = initialState()
	mustRun(t, &s, `{"operations":[{"op":"create","collection":"links","localId":"l","value":{"name":"lan0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01"}},{"op":"create","collection":"zones","localId":"z","value":{"name":"lan"}},{"op":"create","collection":"segments","localId":"seg","value":{"name":"lan","link":"@l","zone":"@z","addressing":{"ipv4":{"mode":"static","addresses":["192.0.2.7/24"]},"ipv6":{"mode":"none"}}}},{"op":"create","collection":"routes","value":{"name":"r","destination":"10.0.0.0/8","target":{"kind":"segment","id":"@seg"},"gateway":"192.0.2.1","enabled":true}}]}`)
	if s.Config.Routes[0].Target.ID != s.Config.Segments[0].ID {
		t.Fatalf("label not resolved: %+v", s.Config.Routes)
	}
}

func TestRouteSameRealization(t *testing.T) {
	in := func(routes string) RealizationInput {
		return RealizationInput{Value: json.RawMessage(`{"links":[],"routes":` + routes + `}`)}
	}
	const on = `{"id":"r1","name":"a","destination":"10.0.0.0/8","target":{"kind":"segment","id":"s"},"enabled":true}`
	const off = `{"id":"r2","name":"b","destination":"10.1.0.0/16","target":{"kind":"segment","id":"s"},"enabled":false}`
	base := in(`[` + on + `,` + off + `]`)
	for _, c := range []struct {
		name  string
		other RealizationInput
		same  bool
	}{
		{"renamed and described", in(`[` + strings.Replace(on, `"name":"a"`, `"name":"z","description":"d"`, 1) + `,` + off + `]`), true},
		{"disabled one edited", in(`[` + on + `,` + strings.Replace(off, "10.1.0.0/16", "10.2.0.0/16", 1) + `]`), true},
		{"disabled one removed", in(`[` + on + `]`), true},
		{"enabled one edited", in(`[` + strings.Replace(on, "10.0.0.0/8", "10.0.0.0/9", 1) + `,` + off + `]`), false},
		{"one enabled", in(`[` + on + `,` + strings.Replace(off, `"enabled":false`, `"enabled":true`, 1) + `]`), false},
		{"one disabled", in(`[` + strings.Replace(on, `"enabled":true`, `"enabled":false`, 1) + `,` + off + `]`), false},
	} {
		if got := SameRealization(base, c.other); got != c.same {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if !SameRealization(in(`[`+off+`]`), RealizationInput{Value: json.RawMessage(`{"links":[]}`)}) {
		t.Error("only disabled routes realize as none")
	}
}

func TestKeptRecoveryKeepsRoutes(t *testing.T) {
	d := routeFixture(t, routeTo(nil))
	d["roles"] = []any{map[string]any{"id": "rol_00000000-0000-4000-8000-000000000001", "name": "administrator", "builtIn": true, "capabilities": []any{"configuration.read", "configuration.write"}}}
	raw, err := json.Marshal(map[string]any{"declaration": d, "secrets": map[string]string{"WAN_PASSWORD": "TEST_ONLY_SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := KeptDeclaration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateBackupDocuments(docs); err != nil {
		t.Fatalf("%v: %s", err, docs.Configuration)
	}
	if !strings.Contains(string(docs.Configuration), rteA) {
		t.Fatalf("the route did not come along: %s", docs.Configuration)
	}
	// LAN closure drops WAN segments and omits routes so no dangling route target remains.
	wanFixture := routeFixture(t, routeTo(map[string]any{"target": map[string]any{"kind": "segment", "id": "seg_00000000-0000-4000-8000-000000000001"}}))
	wanFixture["management"] = map[string]any{"segment": lanSeg}
	wanRaw, err := json.Marshal(wanFixture)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := LANClosure(wanRaw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(closure), `"routes"`) {
		t.Fatalf("lan closure kept routes: %s", closure)
	}
	if closureDocs, err := KeptDeclaration(closure); err != nil {
		t.Fatal(err)
	} else if err := ValidateBackupDocuments(closureDocs); err != nil {
		t.Fatalf("lan closure invalid: %v", err)
	}
}

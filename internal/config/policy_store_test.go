package config

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// run applies a batch to s as the store would, moving s to the next version on success.
func run(t *testing.T, s *savedState, batch string) (preparedChange, error) {
	t.Helper()
	c, e := DecodeChanges([]byte(batch))
	if e != nil {
		return preparedChange{}, e
	}
	n, _ := strconv.Atoi(string(s.Version))
	p, e := prepareChanges(s, c, Version(strconv.Itoa(n+1)), fixedTime)
	if e == nil {
		*s = p.State
	}
	return p, e
}

func mustRun(t *testing.T, s *savedState, batch string) preparedChange {
	t.Helper()
	p, e := run(t, s, batch)
	if e != nil {
		raw, _ := json.Marshal(e)
		t.Fatalf("%s: %v %s", batch, e, raw)
	}
	return p
}

func ruleOp(name, extra string) string {
	return `{"op":"create","collection":"rules","localId":"` + name + `","value":{"name":"` + name + `","enabled":true,"action":"accept","from":{"zones":[],"addresses":[]},"to":{"zones":[],"addresses":[]},"ports":[],"log":false}` + extra + `}`
}

func forwardNames(s savedState) string {
	var names []string
	for _, r := range s.Config.Rules {
		names = append(names, r.Name)
	}
	return strings.Join(names, ",")
}

func TestCreateBefore(t *testing.T) {
	s := seededState(t)
	p := mustRun(t, &s, `{"operations":[`+ruleOp("a", "")+`,`+ruleOp("b", "")+`]}`)
	a := p.Created["a"]
	mustRun(t, &s, `{"operations":[`+ruleOp("c", `,"before":"`+a+`"`)+`]}`)
	if got := forwardNames(s); got != "c,a,b" {
		t.Fatalf("order = %s", got)
	}
	for name, batch := range map[string]string{
		"before on a zone":      `{"operations":[{"op":"create","collection":"zones","before":"` + a + `","value":{"name":"dmz"}}]}`,
		"before a missing rule": `{"operations":[` + ruleOp("d", `,"before":"rul_00000009-0000-4000-8000-000000000009"`) + `]}`,
	} {
		if _, e := run(t, &s, batch); e == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestOrderIsGuardedByListVersion(t *testing.T) {
	s := seededState(t)
	p := mustRun(t, &s, `{"operations":[`+ruleOp("a", "")+`,`+ruleOp("b", "")+`]}`)
	a, b := p.Created["a"], p.Created["b"]
	list := s.Versions[objectKey{"rules", "rules"}].Version
	if list != s.Version {
		t.Fatalf("list version %q after creating its rules at %q", list, s.Version)
	}
	// Another session appends a rule; a reorder built before it is stale.
	mustRun(t, &s, `{"operations":[`+ruleOp("c", "")+`]}`)
	c := s.Config.Rules[2].ID
	_, e := run(t, &s, `{"operations":[{"op":"order","collection":"rules","expectedVersion":"`+string(list)+`","value":["`+b+`","`+a+`"]}]}`)
	if f := requireFault(t, e, "PRECONDITION_FAILED"); f.Current == nil || f.Current.Collection != "rules" {
		t.Fatalf("fault %+v", f)
	}
	list = s.Versions[objectKey{"rules", "rules"}].Version
	_, e = run(t, &s, `{"operations":[{"op":"order","collection":"rules","expectedVersion":"`+string(list)+`","value":["`+b+`","`+a+`"]}]}`)
	requireFault(t, e, "VALIDATION_FAILED")
	ruleVersion := s.Versions[objectKey{"rules", a}].Version
	mustRun(t, &s, `{"operations":[{"op":"order","collection":"rules","expectedVersion":"`+string(list)+`","value":["`+c+`","`+b+`","`+a+`"]}]}`)
	if got := forwardNames(s); got != "c,b,a" {
		t.Fatalf("order = %s", got)
	}
	if s.Versions[objectKey{"rules", a}].Version != ruleVersion || s.Versions[objectKey{"rules", "rules"}].Version != s.Version {
		t.Fatal("a reorder must move the list's version, not its rules'")
	}
}

func TestSingletonVersions(t *testing.T) {
	s := seededState(t)
	seg := s.Config.Segments[0].ID
	set := `{"operations":[{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"` + seg + `"}}]}`
	mustRun(t, &s, set)
	if s.Config.Management == nil || s.Config.Management.Segment != seg || s.Versions[objectKey{"management", "management"}].Version != s.Version {
		t.Fatalf("management %+v versions %+v", s.Config.Management, s.Versions[objectKey{"management", "management"}])
	}
	_, e := run(t, &s, set)
	requireFault(t, e, "PRECONDITION_FAILED")
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}}]}`)
	if s.Config.NAT.Outbound.Mode != "automatic" {
		t.Fatal("outbound NAT not set")
	}
	// none is a saved value of its own, not the absence of one: it keeps a version to replace it by.
	p := mustRun(t, &s, `{"operations":[{"op":"replace","collection":"nat.outbound","expectedVersion":"`+string(s.Versions[listKey("nat.outbound")].Version)+`","value":{"mode":"none"}}]}`)
	if r, e := resultView(p); e != nil || len(r.Items) != 1 || r.Items[0].Version != s.Version {
		t.Fatalf("result %+v %v", r, e)
	}
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"nat.outbound","expectedVersion":"`+string(s.Version)+`","value":{"mode":"automatic"}}]}`)
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"management","expectedVersion":"`+string(s.Versions[objectKey{"management", "management"}].Version)+`"}]}`)
	if s.Config.Management != nil {
		t.Fatal("management not removed")
	}
}

func TestReferencedObjectsCannotBeDeleted(t *testing.T) {
	s := seededState(t)
	zone, seg := s.Config.Zones[0].ID, s.Config.Segments[0].ID
	p := mustRun(t, &s, `{"operations":[
		{"op":"create","collection":"aliases.addresses","localId":"nas","value":{"name":"nas","kind":"host","address":"192.0.2.10"}},
		{"op":"create","collection":"aliases.ports","localId":"dns","value":{"name":"dns","protocol":"udp","ports":["53"]}},
		{"op":"create","collection":"aliases.ports","localId":"web","value":{"name":"web","protocol":"group","members":["@dns"]}},
		{"op":"create","collection":"rules","value":{"name":"r","enabled":true,"action":"accept","from":{"zones":["`+zone+`"],"addresses":[]},"to":{"zones":[],"addresses":["@nas"]},"ports":[],"log":false}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"`+seg+`"}}]}`)
	del := func(collection, id string) string {
		return `{"operations":[{"op":"delete","collection":"` + collection + `","id":"` + id + `","expectedVersion":"` + string(s.Versions[objectKey{collection, id}].Version) + `"}]}`
	}
	for name, batch := range map[string]string{
		"address in a rule":  del("aliases.addresses", p.Created["nas"]),
		"zone in a rule":     del("zones", zone),
		"service in a group": del("aliases.ports", p.Created["dns"]),
		"management segment": del("segments", seg),
	} {
		if _, e := run(t, &s, batch); e == nil {
			t.Fatalf("deleted the %s", name)
		}
	}
}

func TestFilterFaultsRebaseToTheWritingOperation(t *testing.T) {
	s := seededState(t)
	_, e := run(t, &s, `{"operations":[
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}},
		{"op":"create","collection":"aliases.ports","value":{"name":"bad","protocol":"tcp","ports":["0"]}}]}`)
	f := requireFault(t, e, "VALIDATION_FAILED")
	if len(f.Details) != 1 || f.Details[0].Path != "/operations/1/value/ports/0" {
		t.Fatalf("service fault not rebased to its operation: %+v", f.Details)
	}
	_, e = run(t, &s, `{"operations":[
		{"op":"create","collection":"aliases.ports","value":{"name":"ok","protocol":"tcp","ports":["80"]}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"bogus"}}]}`)
	f = requireFault(t, e, "VALIDATION_FAILED")
	if len(f.Details) != 1 || f.Details[0].Path != "/operations/1/value/mode" {
		t.Fatalf("singleton fault not rebased to its operation: %+v", f.Details)
	}
}

// Aliases fed from a URL or from host names are saved, replaced and read back like any alias; their members are
// never saved, so they may stand in rules and port forwards, of either family, but not in groups nor among the
// management sources.
func TestDynamicAliases(t *testing.T) {
	s := seededState(t)
	seg := s.Config.Segments[0].ID
	p := mustRun(t, &s, `{"operations":[
		{"op":"create","collection":"aliases.addresses","localId":"cf","value":{"name":"cloudflare","kind":"url","url":"https://www.cloudflare.com/ips-v4","refreshSeconds":3600}},
		{"op":"create","collection":"aliases.addresses","localId":"vpn","value":{"name":"vpn","kind":"hostnames","names":["vpn.example.com","VPN2.example.com"],"refreshSeconds":300}},
		{"op":"create","collection":"aliases.addresses","localId":"v6","value":{"name":"v6","kind":"network","prefix":"2001:db8::/64"}},
		{"op":"create","collection":"rules","value":{"name":"r","enabled":true,"action":"accept","from":{"zones":[],"addresses":["@cf"]},"to":{"zones":[],"addresses":["@vpn","@v6"]},"ports":[],"log":false}},
		{"op":"create","collection":"nat.portForwards","value":{"name":"web","enabled":true,"ingressZones":["`+s.Config.Zones[0].ID+`"],"protocol":"tcp","from":["@cf"],"to":{"self":true},"ports":"443","target":{"address":"192.0.2.10"}}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"`+seg+`"}}]}`)
	cf, vpn := p.Created["cf"], p.Created["vpn"]
	version := func(id string) string { return string(s.Versions[objectKey{"aliases.addresses", id}].Version) }
	mustRun(t, &s, `{"operations":[
		{"op":"replace","collection":"aliases.addresses","id":"`+cf+`","expectedVersion":"`+version(cf)+`","value":{"name":"cloudflare","kind":"url","url":"http://lists.example/v4.txt","refreshSeconds":86400}},
		{"op":"replace","collection":"aliases.addresses","id":"`+vpn+`","expectedVersion":"`+version(vpn)+`","value":{"name":"vpn","kind":"hostnames","names":["vpn.example.com"],"refreshSeconds":60}}]}`)

	raw, err := canonical(s.Config)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"id":"` + cf + `","kind":"url","name":"cloudflare","refreshSeconds":86400,"url":"http://lists.example/v4.txt"}`,
		`{"id":"` + vpn + `","kind":"hostnames","name":"vpn","names":["vpn.example.com"],"refreshSeconds":60}`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	again, err := decodeDeclaration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back, _ := canonical(again); string(back) != string(raw) {
		t.Fatalf("round trip changed the declaration:\n%s\n%s", raw, back)
	}

	create := func(value string) string {
		return `{"operations":[{"op":"create","collection":"aliases.addresses","value":` + value + `}]}`
	}
	names := make([]string, 21)
	for i := range names {
		names[i] = `"h` + strconv.Itoa(i) + `.example.com"`
	}
	for name, tc := range map[string]struct{ batch, path string }{
		"ftp URL":              {create(`{"name":"x","kind":"url","url":"ftp://lists.example/v4","refreshSeconds":60}`), "/operations/0/value/url"},
		"URL with a user":      {create(`{"name":"x","kind":"url","url":"https://me:pw@lists.example/v4","refreshSeconds":60}`), "/operations/0/value/url"},
		"URL without a host":   {create(`{"name":"x","kind":"url","url":"https:///v4","refreshSeconds":60}`), "/operations/0/value/url"},
		"no refreshSeconds":    {create(`{"name":"x","kind":"url","url":"https://lists.example/v4"}`), "/operations/0/value/refreshSeconds"},
		"refreshSeconds 59":    {create(`{"name":"x","kind":"url","url":"https://lists.example/v4","refreshSeconds":59}`), "/operations/0/value/refreshSeconds"},
		"refreshSeconds 86401": {create(`{"name":"x","kind":"hostnames","names":["a.example"],"refreshSeconds":86401}`), "/operations/0/value/refreshSeconds"},
		"names on a url alias": {create(`{"name":"x","kind":"url","url":"https://lists.example/v4","names":["a.example"],"refreshSeconds":60}`), "/operations/0/value/names"},
		"a url on a host":      {create(`{"name":"x","kind":"host","address":"192.0.2.1","url":"https://lists.example/v4"}`), "/operations/0/value/url"},
		"no names":             {create(`{"name":"x","kind":"hostnames","refreshSeconds":60}`), "/operations/0/value/names"},
		"21 names":             {create(`{"name":"x","kind":"hostnames","names":[` + strings.Join(names, ",") + `],"refreshSeconds":60}`), "/operations/0/value/names"},
		"duplicate names":      {create(`{"name":"x","kind":"hostnames","names":["a.example","A.example"],"refreshSeconds":60}`), "/operations/0/value/names/1"},
		"an address as a name": {create(`{"name":"x","kind":"hostnames","names":["192.0.2.1"],"refreshSeconds":60}`), "/operations/0/value/names/0"},
		"a trailing dot":       {create(`{"name":"x","kind":"hostnames","names":["a.example."],"refreshSeconds":60}`), "/operations/0/value/names/0"},
		"a group member":       {create(`{"name":"x","kind":"group","members":["` + cf + `"]}`), "/operations/0/value/members/0"},
		"a management source": {`{"operations":[{"op":"replace","collection":"management","expectedVersion":"` + string(s.Versions[listKey("management")].Version) + `","value":{"segment":"` + seg + `","allowedSources":["` + vpn + `"]}}]}`,
			"/operations/0/value/allowedSources/0"},
	} {
		t.Run(name, func(t *testing.T) {
			state := s
			_, e := run(t, &state, tc.batch)
			f := requireFault(t, e, "VALIDATION_FAILED")
			if !slices.ContainsFunc(f.Details, func(d FieldError) bool { return d.Path == tc.path }) {
				t.Fatalf("want an error at %s, got %+v", tc.path, f.Details)
			}
		})
	}
}

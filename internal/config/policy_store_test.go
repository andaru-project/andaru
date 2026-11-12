package config

import (
	"encoding/json"
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
	return `{"op":"create","collection":"policy.forward","localId":"` + name + `","value":{"name":"` + name + `","enabled":true,"action":"accept","from":[],"to":[],"services":[],"log":false}` + extra + `}`
}

func forwardNames(s savedState) string {
	var names []string
	for _, r := range s.Config.Policy.Forward {
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
	list := s.Versions[objectKey{"policy.forward", "policy.forward"}].Version
	if list != s.Version {
		t.Fatalf("list version %q after creating its rules at %q", list, s.Version)
	}
	// Another session appends a rule; a reorder built before it is stale.
	mustRun(t, &s, `{"operations":[`+ruleOp("c", "")+`]}`)
	c := s.Config.Policy.Forward[2].ID
	_, e := run(t, &s, `{"operations":[{"op":"order","collection":"policy.forward","expectedVersion":"`+string(list)+`","value":["`+b+`","`+a+`"]}]}`)
	if f := requireFault(t, e, "PRECONDITION_FAILED"); f.Current == nil || f.Current.Collection != "policy.forward" {
		t.Fatalf("fault %+v", f)
	}
	list = s.Versions[objectKey{"policy.forward", "policy.forward"}].Version
	_, e = run(t, &s, `{"operations":[{"op":"order","collection":"policy.forward","expectedVersion":"`+string(list)+`","value":["`+b+`","`+a+`"]}]}`)
	requireFault(t, e, "VALIDATION_FAILED")
	ruleVersion := s.Versions[objectKey{"policy.forward", a}].Version
	mustRun(t, &s, `{"operations":[{"op":"order","collection":"policy.forward","expectedVersion":"`+string(list)+`","value":["`+c+`","`+b+`","`+a+`"]}]}`)
	if got := forwardNames(s); got != "c,b,a" {
		t.Fatalf("order = %s", got)
	}
	if s.Versions[objectKey{"policy.forward", a}].Version != ruleVersion || s.Versions[objectKey{"policy.forward", "policy.forward"}].Version != s.Version {
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
		{"op":"create","collection":"addresses","localId":"nas","value":{"name":"nas","kind":"host","address":"192.0.2.10"}},
		{"op":"create","collection":"services","localId":"dns","value":{"name":"dns","protocol":"udp","ports":["53"]}},
		{"op":"create","collection":"services","localId":"web","value":{"name":"web","protocol":"group","members":["@dns"]}},
		{"op":"create","collection":"policy.forward","value":{"name":"r","enabled":true,"action":"accept","from":["`+zone+`"],"to":["@nas"],"services":[],"log":false}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"`+seg+`"}}]}`)
	del := func(collection, id string) string {
		return `{"operations":[{"op":"delete","collection":"` + collection + `","id":"` + id + `","expectedVersion":"` + string(s.Versions[objectKey{collection, id}].Version) + `"}]}`
	}
	for name, batch := range map[string]string{
		"address in a rule":  del("addresses", p.Created["nas"]),
		"zone in a rule":     del("zones", zone),
		"service in a group": del("services", p.Created["dns"]),
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
		{"op":"create","collection":"services","value":{"name":"bad","protocol":"tcp","ports":["0"]}}]}`)
	f := requireFault(t, e, "VALIDATION_FAILED")
	if len(f.Details) != 1 || f.Details[0].Path != "/operations/1/value/ports/0" {
		t.Fatalf("service fault not rebased to its operation: %+v", f.Details)
	}
	_, e = run(t, &s, `{"operations":[
		{"op":"create","collection":"services","value":{"name":"ok","protocol":"tcp","ports":["80"]}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"bogus"}}]}`)
	f = requireFault(t, e, "VALIDATION_FAILED")
	if len(f.Details) != 1 || f.Details[0].Path != "/operations/1/value/mode" {
		t.Fatalf("singleton fault not rebased to its operation: %+v", f.Details)
	}
}

package config

import (
	"strings"
	"testing"
)

func forwardValue(name, port string, zone string) string {
	return `{"name":"` + name + `","enabled":true,"ingressZones":["` + zone + `"],"protocol":"tcp","from":[],"to":{"self":true},"ports":"` + port + `","target":{"address":"192.0.2.10"}}`
}

// The NAT lists keep their order and versions exactly as the policy lists do, and never move another list's version.
func TestNATListsBehaveLikePolicyLists(t *testing.T) {
	s := seededState(t)
	zone := s.Config.Zones[0].ID
	policy := versionOf(&s, listKey("policy.forward")).Version
	p := mustRun(t, &s, `{"operations":[{"op":"create","collection":"nat.portForwards","localId":"a","value":`+forwardValue("a", "8080", zone)+`}]}`)
	a := p.Created["a"]
	if !strings.HasPrefix(a, "pfw_") {
		t.Fatalf("created %q", a)
	}
	listVersion := versionOf(&s, listKey("nat.portForwards")).Version
	p = mustRun(t, &s, `{"operations":[{"op":"create","collection":"nat.portForwards","localId":"b","before":"`+a+`","value":`+forwardValue("b", "8081", zone)+`}]}`)
	b := p.Created["b"]
	if got := s.Config.NAT.PortForwards; len(got) != 2 || got[0].ID != b || versionOf(&s, listKey("nat.portForwards")).Version == listVersion {
		t.Fatalf("before: %+v", got)
	}
	listVersion = versionOf(&s, listKey("nat.portForwards")).Version
	mustRun(t, &s, `{"operations":[{"op":"order","collection":"nat.portForwards","expectedVersion":"`+string(listVersion)+`","value":["`+a+`","`+b+`"]}]}`)
	if got := s.Config.NAT.PortForwards; got[0].ID != a || versionOf(&s, listKey("nat.portForwards")).Version == listVersion {
		t.Fatalf("order: %+v", got)
	}
	listVersion = versionOf(&s, listKey("nat.portForwards")).Version
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"nat.portForwards","id":"`+a+`","expectedVersion":"`+string(versionOf(&s, objectKey{"nat.portForwards", a}).Version)+`","value":`+forwardValue("a", "9090", zone)+`}]}`)
	if s.Config.NAT.PortForwards[0].Ports != "9090" || versionOf(&s, listKey("nat.portForwards")).Version != listVersion {
		t.Fatal("a replace moved the list version, or was lost")
	}
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"nat.portForwards","id":"`+b+`","expectedVersion":"`+string(versionOf(&s, objectKey{"nat.portForwards", b}).Version)+`"}]}`)
	if len(s.Config.NAT.PortForwards) != 1 || versionOf(&s, listKey("nat.portForwards")).Version == listVersion {
		t.Fatal("delete")
	}
	o := mustRun(t, &s, `{"operations":[{"op":"create","collection":"nat.oneToOne","localId":"o","value":{"name":"o","enabled":true,"zone":"`+zone+`","external":"203.0.113.20","internal":"192.0.2.30","destination":[]}}]}`)
	if !strings.HasPrefix(o.Created["o"], "oto_") || len(s.Config.NAT.OneToOne) != 1 {
		t.Fatalf("1:1 %+v", s.Config.NAT.OneToOne)
	}
	if versionOf(&s, listKey("policy.forward")).Version != policy {
		t.Fatal("a NAT list change moved the forward policy's version")
	}
}

// Deleting outbound NAT is about outbound NAT only: the forwards and 1:1 entries stay.
func TestDeletingOutboundKeepsNATLists(t *testing.T) {
	s := seededState(t)
	zone := s.Config.Zones[0].ID
	mustRun(t, &s, `{"operations":[{"op":"create","collection":"nat.portForwards","localId":"a","value":`+forwardValue("a", "8080", zone)+`},
		{"op":"create","collection":"nat.oneToOne","localId":"o","value":{"name":"o","enabled":true,"zone":"`+zone+`","external":"203.0.113.20","internal":"192.0.2.30","destination":[]}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}}]}`)
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"nat.outbound","expectedVersion":"`+string(versionOf(&s, listKey("nat.outbound")).Version)+`"}]}`)
	if len(s.Config.NAT.PortForwards) != 1 || len(s.Config.NAT.OneToOne) != 1 || s.Config.NAT.Outbound.Mode != "" {
		t.Fatalf("after deleting outbound: %+v", s.Config.NAT)
	}
}

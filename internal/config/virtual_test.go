package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	vipA = "vip_00000001-0000-4000-8000-000000000001"
	vipB = "vip_00000002-0000-4000-8000-000000000002"
)

// vipFixture is the NAT fixture with the LAN on a static IPv6 /64 and two virtual addresses.
func vipFixture(t *testing.T) map[string]any {
	t.Helper()
	d := natFixture(t)
	v6(d, 1)["mode"], v6(d, 1)["addresses"] = "static", []any{"fd00:1::1/64"}
	delete(v6(d, 1), "from")
	delete(v6(d, 1), "subnetId")
	d["virtualAddresses"] = []any{
		map[string]any{"id": vipA, "name": "dns4", "address": "172.16.53.53"},
		map[string]any{"id": vipB, "name": "dns6", "address": "fd00:53:53:53:53:53:53:53"},
	}
	return d
}

func TestVirtualAddressValidation(t *testing.T) {
	got := accepted(t, vipFixture(t))
	if len(got.VirtualAddresses) != 2 || got.VirtualAddresses[1].Address != "fd00:53:53:53:53:53:53:53" {
		t.Fatalf("decoded %+v", got.VirtualAddresses)
	}
	for name, address := range map[string]string{
		"unspecified":     "0.0.0.0",
		"multicast":       "224.0.0.1",
		"loopback":        "127.0.0.2",
		"loopback v6":     "::1",
		"link-local":      "fe80::1",
		"mapped":          "::ffff:1.2.3.4",
		"with a length":   "172.16.53.53/32",
		"segment address": "192.0.2.7",
		"in a subnet":     "192.0.2.99",
		"in a v6 subnet":  "fd00:1::99",
		"zone":            "fd00::1%eth0",
	} {
		t.Run(name, func(t *testing.T) {
			d := vipFixture(t)
			item(d["virtualAddresses"].([]any), 0)["address"] = address
			refusedAt(t, d, "/virtualAddresses/0/address")
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		d := vipFixture(t)
		item(d["virtualAddresses"].([]any), 1)["address"] = "172.16.53.53"
		refusedAt(t, d, "/virtualAddresses/1/address")
	})
	t.Run("65 entries", func(t *testing.T) {
		d := vipFixture(t)
		var list []any
		for i := range 65 {
			list = append(list, map[string]any{"id": fmt.Sprintf("vip_%08x-0000-4000-8000-000000000001", i+1), "name": fmt.Sprint("v", i), "address": fmt.Sprintf("172.16.54.%d", i+1)})
		}
		d["virtualAddresses"] = list
		_, e := decodeMap(t, d)
		if e == nil {
			t.Fatal("65 virtual addresses accepted")
		}
	})
	t.Run("link named andaru-vip", func(t *testing.T) {
		d := vipFixture(t)
		item(linksOf(d), 0)["name"] = "andaru-vip"
		refusedAt(t, d, "/links/0/name")
	})
}

func TestDNSVirtualAddresses(t *testing.T) {
	d := vipFixture(t)
	d["dns"] = map[string]any{"mode": "recursive", "segments": []any{}, "virtualAddresses": []any{vipA, vipB}}
	if got := accepted(t, d); len(got.DNS.VirtualAddresses) != 2 {
		t.Fatalf("decoded %+v", got.DNS)
	}
	d["dns"].(map[string]any)["virtualAddresses"] = []any{"vip_00000009-0000-4000-8000-000000000009"}
	refusedAt(t, d, "/dns/virtualAddresses/0")
}

func TestIPv6PortForwards(t *testing.T) {
	v6forward := func(d map[string]any, fields map[string]any) map[string]any {
		f := forward(d, map[string]any{"family": "ipv6", "target": map[string]any{"self": true}, "targetPort": nil, "ports": "53", "protocol": "tcp-udp"})
		for k, v := range fields {
			if v == nil {
				delete(f, k)
			} else {
				f[k] = v
			}
		}
		return f
	}
	const p = "/nat/portForwards/0"
	for name, fields := range map[string]map[string]any{
		"to self":            nil,
		"to a static v6":     {"target": map[string]any{"address": "fd00:1::1"}},
		"to a v6 virtual":    {"target": map[string]any{"address": "fd00:53:53:53:53:53:53:53"}},
		"a shifted range":    {"ports": "5300-5301", "targetPort": 53, "target": map[string]any{"address": "fd00:53:53:53:53:53:53:53"}},
		"from an v6 address": {"from": []any{"adr_v6"}},
	} {
		t.Run(name, func(t *testing.T) {
			d := vipFixture(t)
			d["objects"].(map[string]any)["addresses"] = append(d["objects"].(map[string]any)["addresses"].([]any),
				map[string]any{"id": "adr_00000003-0000-4000-8000-000000000003", "name": "v6peer", "kind": "host", "address": "2001:db8::7"})
			if fields["from"] != nil {
				fields["from"] = []any{"adr_00000003-0000-4000-8000-000000000003"}
			}
			setNAT(d, "portForwards", v6forward(d, fields))
			accepted(t, d)
		})
	}
	for name, tc := range map[string]struct {
		fields map[string]any
		path   string
	}{
		"to an inside host": {map[string]any{"target": map[string]any{"address": "2001:db8::5"}}, p + "/target/address"},
		"to an IPv4 target": {map[string]any{"target": map[string]any{"address": "172.16.53.53"}}, p + "/target/address"},
		"reflection":        {map[string]any{"reflection": true}, p + "/reflection"},
		"an IPv4 source":    {map[string]any{"from": []any{natPeer}}, p + "/from/0"},
		"unknown family":    {map[string]any{"family": "ipx"}, p + "/family"},
	} {
		t.Run(name, func(t *testing.T) {
			d := vipFixture(t)
			setNAT(d, "portForwards", v6forward(d, tc.fields))
			refusedAt(t, d, tc.path)
		})
	}
	t.Run("message", func(t *testing.T) {
		d := vipFixture(t)
		setNAT(d, "portForwards", v6forward(d, map[string]any{"target": map[string]any{"address": "2001:db8::5"}}))
		_, e := decodeMap(t, d)
		if raw, _ := json.Marshal(e); !strings.Contains(string(raw), "IPv6 port forwards to inside hosts are not offered (#43).") {
			t.Fatalf("%s", raw)
		}
	})
	t.Run("same ports, both families", func(t *testing.T) {
		d := vipFixture(t)
		four := forward(d, map[string]any{"ports": "53", "protocol": "tcp-udp", "targetPort": nil, "target": map[string]any{"address": "172.16.53.53"}})
		six := v6forward(d, map[string]any{"id": pfwB, "name": "dns6"})
		setNAT(d, "portForwards", four, six)
		accepted(t, d)
		six2 := v6forward(d, map[string]any{"id": pfwB, "name": "dns6"})
		six1 := v6forward(d, nil)
		setNAT(d, "portForwards", six1, six2)
		refusedAt(t, d, "/nat/portForwards/1/ports")
	})
	t.Run("an IPv4 forward to the virtual address", func(t *testing.T) {
		d := vipFixture(t)
		setNAT(d, "portForwards", forward(d, map[string]any{"target": map[string]any{"address": "172.16.53.53"}}))
		accepted(t, d)
	})
}

// A virtual address a forward targets cannot go away under it: the forward would become one to an inside host.
func TestVirtualAddressInUse(t *testing.T) {
	s := seededState(t)
	p := mustRun(t, &s, `{"operations":[{"op":"create","collection":"virtualAddresses","localId":"v","value":{"name":"dns4","address":"172.16.53.53"}}]}`)
	id := p.Created["v"]
	if !strings.HasPrefix(id, "vip_") {
		t.Fatalf("created %q", id)
	}
	zone := s.Config.Zones[0].ID
	pf := mustRun(t, &s, `{"operations":[{"op":"create","collection":"nat.portForwards","localId":"f","value":{"name":"dns","enabled":true,"ingressZones":["`+zone+`"],"protocol":"udp","from":[],"to":{"self":true},"ports":"53","target":{"address":"172.16.53.53"}}}]}`)
	fid := pf.Created["f"]
	version := func() string { return string(versionOf(&s, objectKey{"virtualAddresses", id}).Version) }
	for name, op := range map[string]string{
		"delete":     `{"op":"delete","collection":"virtualAddresses","id":"` + id + `","expectedVersion":"` + version() + `"}`,
		"re-address": `{"op":"replace","collection":"virtualAddresses","id":"` + id + `","expectedVersion":"` + version() + `","value":{"name":"dns4","address":"172.16.53.54"}}`,
	} {
		_, e := run(t, &s, `{"operations":[`+op+`]}`)
		raw, _ := json.Marshal(e)
		if e == nil || !strings.Contains(string(raw), "/operations/0/id") || !strings.Contains(string(raw), "targets this address") {
			t.Fatalf("%s: %s", name, raw)
		}
	}
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"nat.portForwards","id":"`+fid+`","expectedVersion":"`+string(versionOf(&s, objectKey{"nat.portForwards", fid}).Version)+`"}]}`)
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"virtualAddresses","id":"`+id+`","expectedVersion":"`+version()+`"}]}`)
	if len(s.Config.VirtualAddresses) != 0 {
		t.Fatal("not deleted")
	}
}

// An explicit ipv4 and an omitted family are the same family: two such forwards on one port conflict.
func TestForwardFamilyDefaultOverlaps(t *testing.T) {
	d := vipFixture(t)
	a := forward(d, map[string]any{"family": "ipv4"})
	b := forward(d, map[string]any{"id": pfwB, "name": "web2"})
	setNAT(d, "portForwards", a, b)
	refusedAt(t, d, "/nat/portForwards/1/ports")
}

// A virtual address is one address however it is written.
func TestVirtualAddressSpelling(t *testing.T) {
	d := vipFixture(t)
	item(d["virtualAddresses"].([]any), 1)["address"] = "FD00:0053:53:53:53:53:53:53"
	setNAT(d, "portForwards", forward(d, map[string]any{"family": "ipv6", "target": map[string]any{"address": "fd00:53:53:53:53:53:53:53"}, "targetPort": nil, "ports": "53"}))
	if got := accepted(t, d); got.VirtualAddresses[1].Address != "fd00:53:53:53:53:53:53:53" {
		t.Fatalf("stored %q", got.VirtualAddresses[1].Address)
	}
}

// An IPv6 forward's destination object must hold IPv6 addresses.
func TestIPv6ForwardDestinationFamily(t *testing.T) {
	d := vipFixture(t)
	setNAT(d, "portForwards", forward(d, map[string]any{"family": "ipv6", "to": map[string]any{"address": natPeer}, "target": map[string]any{"self": true}, "targetPort": nil}))
	refusedAt(t, d, "/nat/portForwards/0/to/address")
}

// The in-use refusal names the forward for an IPv6 virtual address too, and a DNS reference also holds it.
func TestVirtualAddressInUseIPv6AndDNS(t *testing.T) {
	s := seededState(t)
	lan := s.Config.Segments[1].ID
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"segments","id":"`+lan+`","expectedVersion":"7","value":{"name":"lan","link":"`+s.Config.Segments[1].Link+`","zone":"`+s.Config.Segments[1].Zone+`","addressing":{"ipv4":{"mode":"static","addresses":["192.0.2.7/24"]},"ipv6":{"mode":"static","addresses":["fd00:1::1/64"]}}}}]}`)
	p := mustRun(t, &s, `{"operations":[{"op":"create","collection":"virtualAddresses","localId":"v","value":{"name":"dns6","address":"fd00:53:53:53:53:53:53:53"}},
		{"op":"create","collection":"nat.portForwards","localId":"f","value":{"name":"dns6","family":"ipv6","enabled":true,"ingressZones":["`+s.Config.Zones[0].ID+`"],"protocol":"udp","from":[],"to":{"self":true},"ports":"53","target":{"address":"fd00:53:53:53:53:53:53:53"}}}]}`)
	id := p.Created["v"]
	_, e := run(t, &s, `{"operations":[{"op":"delete","collection":"virtualAddresses","id":"`+id+`","expectedVersion":"`+string(versionOf(&s, objectKey{"virtualAddresses", id}).Version)+`"}]}`)
	if raw, _ := json.Marshal(e); e == nil || !strings.Contains(string(raw), `Port forward \"dns6\" targets this address`) {
		t.Fatalf("%s", raw)
	}
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"nat.portForwards","id":"`+p.Created["f"]+`","expectedVersion":"`+string(versionOf(&s, objectKey{"nat.portForwards", p.Created["f"]}).Version)+`"}]}`)
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"dns","expectedVersion":"0","value":{"mode":"recursive","segments":[],"virtualAddresses":["`+id+`"]}}]}`)
	if _, e := run(t, &s, `{"operations":[{"op":"delete","collection":"virtualAddresses","id":"`+id+`","expectedVersion":"`+string(versionOf(&s, objectKey{"virtualAddresses", id}).Version)+`"}]}`); e == nil {
		t.Fatal("deleted a virtual address the DNS service listens on")
	}
}

// An address on a disabled link is not held by the firewall, so an IPv6 forward cannot target it.
func TestIPv6ForwardToDisabledLink(t *testing.T) {
	d := vipFixture(t)
	lan := item(segmentsOf(d), 1)["link"].(string)
	for _, l := range linksOf(d) {
		if l.(map[string]any)["id"] == lan {
			l.(map[string]any)["enabled"] = false
		}
	}
	setNAT(d, "portForwards", forward(d, map[string]any{"family": "ipv6", "target": map[string]any{"address": "fd00:1::1"}, "targetPort": nil, "ports": "53"}))
	_, e := decodeMap(t, d)
	if raw, _ := json.Marshal(e); e == nil || !strings.Contains(string(raw), "is disabled") {
		t.Fatalf("%s", raw)
	}
}

// Renaming a virtual address a forward targets keeps its address, so the forward stays valid.
func TestVirtualAddressRenameWhileTargeted(t *testing.T) {
	s := seededState(t)
	p := mustRun(t, &s, `{"operations":[{"op":"create","collection":"virtualAddresses","localId":"v","value":{"name":"dns4","address":"172.16.53.53"}}]}`)
	id := p.Created["v"]
	mustRun(t, &s, `{"operations":[{"op":"create","collection":"nat.portForwards","value":{"name":"dns","enabled":true,"ingressZones":["`+s.Config.Zones[0].ID+`"],"protocol":"udp","from":[],"to":{"self":true},"ports":"53","target":{"address":"172.16.53.53"}}}]}`)
	mustRun(t, &s, `{"operations":[{"op":"replace","collection":"virtualAddresses","id":"`+id+`","expectedVersion":"`+string(versionOf(&s, objectKey{"virtualAddresses", id}).Version)+`","value":{"name":"renamed","description":"edited","address":"172.16.53.53"}}]}`)
	if s.Config.VirtualAddresses[0].Name != "renamed" {
		t.Fatalf("%+v", s.Config.VirtualAddresses)
	}
}

// A batch can list a virtual address it creates in the DNS service by label.
func TestDNSVirtualAddressByLabel(t *testing.T) {
	s := seededState(t)
	p := mustRun(t, &s, `{"operations":[{"op":"create","collection":"virtualAddresses","localId":"v","value":{"name":"dns4","address":"172.16.53.53"}},
		{"op":"replace","collection":"dns","expectedVersion":"0","value":{"mode":"recursive","segments":[],"virtualAddresses":["@v"]}}]}`)
	if got := s.Config.DNS.VirtualAddresses; len(got) != 1 || got[0] != p.Created["v"] {
		t.Fatalf("dns lists %v, created %v", got, p.Created)
	}
}

// An IPv6 forward's source group must hold at least one IPv6 address; an IPv4-only group never matches.
func TestIPv6ForwardSourceGroupFamily(t *testing.T) {
	const (
		v6host = "adr_00000003-0000-4000-8000-000000000003"
		v4grp  = "adr_00000004-0000-4000-8000-000000000004"
		mixed  = "adr_00000005-0000-4000-8000-000000000005"
	)
	build := func(group string) map[string]any {
		d := vipFixture(t)
		objs := d["objects"].(map[string]any)
		objs["addresses"] = append(objs["addresses"].([]any),
			map[string]any{"id": v6host, "name": "peer6", "kind": "host", "address": "2001:db8::7"},
			map[string]any{"id": v4grp, "name": "g4", "kind": "group", "members": []any{natPeer}},
			map[string]any{"id": mixed, "name": "gmix", "kind": "group", "members": []any{natPeer, v6host}})
		setNAT(d, "portForwards", forward(d, map[string]any{"family": "ipv6", "target": map[string]any{"self": true}, "targetPort": nil, "ports": "53", "from": []any{group}}))
		return d
	}
	refusedAt(t, build(v4grp), "/nat/portForwards/0/from/0")
	accepted(t, build(mixed))
}

package config

import (
	"encoding/json"
	"fmt"
	"slices"
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
		refusedWith(t, d, "/virtualAddresses/1/address", "Another virtual address already has this address.")
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
			d["aliases"].(map[string]any)["addresses"] = append(d["aliases"].(map[string]any)["addresses"].([]any),
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
		objs := d["aliases"].(map[string]any)
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

const (
	vipP = "vip_00000003-0000-4000-8000-000000000003"
	vipQ = "vip_00000004-0000-4000-8000-000000000004"
)

// lanDHCP is a DHCP entry on the LAN, with fields set over a range clear of the proxy ARP entries.
func lanDHCP(d map[string]any, fields map[string]any) map[string]any {
	e := map[string]any{"id": "dhp_00000001-0000-4000-8000-000000000001", "name": "lan", "segment": item(segmentsOf(d), 1)["id"],
		"ranges": []any{map[string]any{"first": "192.0.2.100", "last": "192.0.2.199"}}}
	for k, v := range fields {
		e[k] = v
	}
	return e
}

// proxyFixture adds two proxy ARP entries on the LAN, whose static subnet is 192.0.2.0/24: an address and a /29.
func proxyFixture(t *testing.T) map[string]any {
	t.Helper()
	d := vipFixture(t)
	lan := item(segmentsOf(d), 1)["id"]
	d["virtualAddresses"] = append(d["virtualAddresses"].([]any),
		map[string]any{"id": vipP, "name": "host", "kind": "proxyArp", "segment": lan, "address": "192.0.2.50"},
		map[string]any{"id": vipQ, "name": "block", "kind": "proxyArp", "segment": lan, "address": "192.0.2.56/29"})
	return d
}

func TestProxyARPValidation(t *testing.T) {
	got := accepted(t, proxyFixture(t))
	if v := got.VirtualAddresses[3]; v.Kind != "proxyArp" || v.Address != "192.0.2.56/29" || v.Segment == "" {
		t.Fatalf("decoded %+v", got.VirtualAddresses)
	}
	t.Run("explicit kind address is stored absent", func(t *testing.T) {
		d := proxyFixture(t)
		item(d["virtualAddresses"].([]any), 0)["kind"] = "address"
		if got := accepted(t, d); got.VirtualAddresses[0].Kind != "" {
			t.Fatalf("stored kind %q", got.VirtualAddresses[0].Kind)
		}
	})
	t.Run("a 1:1 external on it", func(t *testing.T) {
		d := proxyFixture(t)
		setNAT(d, "oneToOne", map[string]any{"id": otoA, "name": "block", "enabled": true, "zone": wanZone(d), "external": "192.0.2.56/29", "internal": "10.9.9.8/29", "destination": []any{}})
		accepted(t, d)
	})
	t.Run("a /32 with its length is stored bare", func(t *testing.T) {
		d := proxyFixture(t)
		item(d["virtualAddresses"].([]any), 2)["address"] = "192.0.2.50/32"
		if got := accepted(t, d); got.VirtualAddresses[2].Address != "192.0.2.50" {
			t.Fatalf("stored %q", got.VirtualAddresses[2].Address)
		}
	})
	const p, q = "/virtualAddresses/2", "/virtualAddresses/3"
	for name, tc := range map[string]struct {
		edit    func(d map[string]any)
		path    string
		message string
	}{
		"ipv6":              {func(d map[string]any) { item(d["virtualAddresses"].([]any), 2)["address"] = "fd00:1::50" }, p + "/address", "Expected an IPv4 address; for an IPv6 host on the uplink's prefix, use a proxyNdp entry."},
		"ipv6 prefix":       {func(d map[string]any) { item(d["virtualAddresses"].([]any), 2)["address"] = "fd00:1::/64" }, p + "/address", "Expected an IPv4 address; for an IPv6 host on the uplink's prefix, use a proxyNdp entry."},
		"too short":         {func(d map[string]any) { item(d["virtualAddresses"].([]any), 3)["address"] = "192.0.0.0/23" }, q + "/address", ""},
		"host bits":         {func(d map[string]any) { item(d["virtualAddresses"].([]any), 3)["address"] = "192.0.2.57/29" }, q + "/address", ""},
		"not an address":    {func(d map[string]any) { item(d["virtualAddresses"].([]any), 2)["address"] = "lan" }, p + "/address", ""},
		"off the subnet":    {func(d map[string]any) { item(d["virtualAddresses"].([]any), 2)["address"] = "198.51.100.50" }, p + "/address", "Expected an address on one of the segment's static IPv4 subnets."},
		"holds own address": {func(d map[string]any) { item(d["virtualAddresses"].([]any), 3)["address"] = "192.0.2.0/29" }, q + "/address", "This range holds one of the firewall's own addresses."},
		"holds the gateway": {func(d map[string]any) { v4(d, 1)["gateway"] = "192.0.2.58" }, q + "/address", "This range holds a segment's gateway."},
		"holds the network address": {func(d map[string]any) {
			v4(d, 1)["addresses"] = []any{"192.0.2.7/29"}
			item(d["virtualAddresses"].([]any), 3)["address"] = "192.0.2.0/30"
			item(d["virtualAddresses"].([]any), 2)["address"] = "192.0.2.5"
		}, q + "/address", "This range holds its subnet's network address."},
		"holds the broadcast address": {func(d map[string]any) { item(d["virtualAddresses"].([]any), 2)["address"] = "192.0.2.255" }, p + "/address", "This range holds its subnet's broadcast address."},
		"overlaps a DHCP range": {func(d map[string]any) {
			d["dhcp"] = []any{lanDHCP(d, map[string]any{"ranges": []any{map[string]any{"first": "192.0.2.30", "last": "192.0.2.56"}}})}
		}, q + "/address", "This range overlaps a DHCP range."},
		"holds a static lease": {func(d map[string]any) {
			d["dhcp"] = []any{lanDHCP(d, map[string]any{"staticLeases": []any{map[string]any{"mac": "02:00:00:00:00:10", "address": "192.0.2.50"}}})}
		}, p + "/address", "This range holds a DHCP static lease."},
		"holds a DHCP gateway": {func(d map[string]any) { d["dhcp"] = []any{lanDHCP(d, map[string]any{"gateway": "192.0.2.63"})} }, q + "/address", "This range holds a DHCP entry's gateway."},
		"holds a segment's DNS server": {func(d map[string]any) {
			item(segmentsOf(d), 1)["addressing"].(map[string]any)["dns"] = map[string]any{"servers": []any{"192.0.2.57"}}
		}, q + "/address", "This range holds a segment's DNS server."},
		"holds a DNS upstream": {func(d map[string]any) {
			d["dns"] = map[string]any{"mode": "forward", "segments": []any{}, "upstreams": []any{map[string]any{"address": "192.0.2.50:5353", "protocol": "do53"}}}
		}, p + "/address", "This range holds a DNS upstream."},
		"holds a DHCP DNS server": {func(d map[string]any) {
			d["dhcp"] = []any{lanDHCP(d, map[string]any{"dnsServers": []any{"192.0.2.57"}})}
		}, q + "/address", "This range holds a DHCP entry's DNS server."},
		"holds a DHCP NTP server": {func(d map[string]any) {
			d["dhcp"] = []any{lanDHCP(d, map[string]any{"ntpServers": []any{"192.0.2.58"}})}
		}, q + "/address", "This range holds a DHCP entry's NTP server."},
		"holds a DHCP next server": {func(d map[string]any) { d["dhcp"] = []any{lanDHCP(d, map[string]any{"nextServer": "192.0.2.61"})} }, q + "/address", "This range holds a DHCP entry's next server."},
		"holds a management source network": {func(d map[string]any) {
			const net = "adr_00000005-0000-4000-8000-000000000005"
			objs := d["aliases"].(map[string]any)
			objs["addresses"] = append(objs["addresses"].([]any), map[string]any{"id": net, "name": "admin-net", "kind": "network", "prefix": "192.0.2.60/30"})
			d["management"] = map[string]any{"segment": item(segmentsOf(d), 1)["id"], "allowedSources": []any{net}}
		}, q + "/address", "This range holds a management source address."},
		"holds a management source range": {func(d map[string]any) {
			const r = "adr_00000006-0000-4000-8000-000000000006"
			objs := d["aliases"].(map[string]any)
			objs["addresses"] = append(objs["addresses"].([]any), map[string]any{"id": r, "name": "admin-range", "kind": "range", "first": "192.0.2.57", "last": "192.0.2.59"})
			d["management"] = map[string]any{"segment": item(segmentsOf(d), 1)["id"], "allowedSources": []any{r}}
		}, q + "/address", "This range holds a management source address."},
		"holds a management source in a group": {func(d map[string]any) {
			const host, group = "adr_00000003-0000-4000-8000-000000000003", "adr_00000004-0000-4000-8000-000000000004"
			objs := d["aliases"].(map[string]any)
			objs["addresses"] = append(objs["addresses"].([]any),
				map[string]any{"id": host, "name": "admin", "kind": "host", "address": "192.0.2.59"},
				map[string]any{"id": group, "name": "admins", "kind": "group", "members": []any{host}})
			d["management"] = map[string]any{"segment": item(segmentsOf(d), 1)["id"], "allowedSources": []any{group}}
		}, q + "/address", "This range holds a management source address."},
		"overlaps":   {func(d map[string]any) { item(d["virtualAddresses"].([]any), 2)["address"] = "192.0.2.60" }, q + "/address", `This range overlaps proxy ARP entry \"host\".`}, // quoted as the JSON-encoded error shows it
		"no segment": {func(d map[string]any) { delete(item(d["virtualAddresses"].([]any), 2), "segment") }, p + "/segment", "Expected an existing segment."},
		"unknown segment": {func(d map[string]any) {
			item(d["virtualAddresses"].([]any), 2)["segment"] = "seg_00000009-0000-4000-8000-000000000009"
		}, p + "/segment", "Expected an existing segment."},
		"segment by PPPoE": {func(d map[string]any) {
			item(d["virtualAddresses"].([]any), 2)["segment"] = item(segmentsOf(d), 0)["id"]
		}, p + "/segment", "no known subnet"},
		"segment on an address": {func(d map[string]any) {
			item(d["virtualAddresses"].([]any), 0)["segment"] = item(segmentsOf(d), 1)["id"]
		}, "/virtualAddresses/0/segment", "Only a proxy ARP or proxy NDP entry names a segment."},
		"address inside a proxy entry": {func(d map[string]any) { item(d["virtualAddresses"].([]any), 0)["address"] = "192.0.2.57" }, "/virtualAddresses/0/address", ""},
		"unknown kind":                 {func(d map[string]any) { item(d["virtualAddresses"].([]any), 2)["kind"] = "carp" }, p + "/kind", ""},
		"dns listens on it": {func(d map[string]any) {
			d["dns"] = map[string]any{"mode": "recursive", "segments": []any{}, "virtualAddresses": []any{vipA, vipP}}
		}, "/dns/virtualAddresses/1", "the DNS service cannot listen on it"},
		"forward target": {func(d map[string]any) {
			setNAT(d, "portForwards", forward(d, map[string]any{"target": map[string]any{"address": "192.0.2.59"}}))
		}, "/nat/portForwards/0/target/address", "This address is answered by proxy ARP and held by no host."},
		"1:1 internal": {func(d map[string]any) {
			setNAT(d, "oneToOne", map[string]any{"id": otoA, "name": "server", "enabled": true, "zone": wanZone(d), "external": "203.0.113.20", "internal": "192.0.2.50", "destination": []any{}})
		}, "/nat/oneToOne/0/internal", "This address is answered by proxy ARP and held by no host."},
		"1:1 internal prefix": {func(d map[string]any) {
			setNAT(d, "oneToOne", map[string]any{"id": otoA, "name": "block", "enabled": true, "zone": wanZone(d), "external": "203.0.113.16/28", "internal": "192.0.2.48/28", "destination": []any{}})
		}, "/nat/oneToOne/0/internal", "This address is answered by proxy ARP and held by no host."},
	} {
		t.Run(name, func(t *testing.T) {
			d := proxyFixture(t)
			tc.edit(d)
			refusedAt(t, d, tc.path)
			_, e := decodeMap(t, d)
			if raw, _ := json.Marshal(e); !strings.Contains(string(raw), tc.message) {
				t.Fatalf("%s", raw)
			}
		})
	}
}

// A batch can name the segment it creates in a proxy ARP entry by label.
func TestProxyARPSegmentByLabel(t *testing.T) {
	c, e := DecodeChanges([]byte(`{"operations":[{"op":"create","collection":"links","localId":"uplink","value":{"name":"uplink","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01"}},{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},{"op":"create","collection":"segments","localId":"seg","value":{"name":"wan","link":"@uplink","zone":"@wan","addressing":{"ipv4":{"mode":"static","addresses":["203.0.113.2/24"]},"ipv6":{"mode":"none"}}}},
		{"op":"create","collection":"virtualAddresses","localId":"v","value":{"name":"server","kind":"proxyArp","segment":"@seg","address":"203.0.113.50"}}]}`))
	if e != nil {
		t.Fatal(e)
	}
	s := initialState()
	p, e := prepareChanges(&s, c, "1", fixedTime)
	if e != nil {
		t.Fatal(e)
	}
	if got := p.State.Config.VirtualAddresses[0].Segment; got != p.Created["seg"] {
		t.Fatalf("segment %q, created %v", got, p.Created)
	}
}

const vipR = "vip_00000005-0000-4000-8000-000000000005"

// blockFixture adds held blocks to the virtual address fixture: an IPv4 /29 and an IPv6 /64.
func blockFixture(t *testing.T) map[string]any {
	t.Helper()
	d := vipFixture(t)
	d["virtualAddresses"] = append(d["virtualAddresses"].([]any),
		map[string]any{"id": vipP, "name": "block4", "address": "203.0.113.0/29"},
		map[string]any{"id": vipQ, "name": "block6", "kind": "address", "address": "2001:db8:53::/64"})
	return d
}

func TestRoutedBlockValidation(t *testing.T) {
	got := accepted(t, blockFixture(t))
	if v := got.VirtualAddresses; v[2].Address != "203.0.113.0/29" || v[3].Address != "2001:db8:53::/64" || v[3].Kind != "" {
		t.Fatalf("decoded %+v", v)
	}
	t.Run("a /32 and a /128 are stored bare", func(t *testing.T) {
		d := vipFixture(t)
		item(d["virtualAddresses"].([]any), 0)["address"] = "172.16.53.53/32"
		item(d["virtualAddresses"].([]any), 1)["address"] = "FD00:53:53:53:53:53:53:53/128"
		if v := accepted(t, d).VirtualAddresses; v[0].Address != "172.16.53.53" || v[1].Address != "fd00:53:53:53:53:53:53:53" {
			t.Fatalf("stored %q %q", v[0].Address, v[1].Address)
		}
	})
	t.Run("one spelling", func(t *testing.T) {
		d := blockFixture(t)
		item(d["virtualAddresses"].([]any), 3)["address"] = "2001:DB8:53:0::/64"
		if v := accepted(t, d).VirtualAddresses; v[3].Address != "2001:db8:53::/64" {
			t.Fatalf("stored %q", v[3].Address)
		}
	})
	t.Run("the shortest blocks", func(t *testing.T) {
		d := blockFixture(t)
		item(d["virtualAddresses"].([]any), 2)["address"] = "203.0.113.0/24"
		item(d["virtualAddresses"].([]any), 3)["address"] = "2001:db8:53::/48"
		accepted(t, d)
	})
	const shape = "Expected one unicast IPv4 or IPv6 address, or a block"
	// index is the entry edited: 0 dns4, 1 dns6, 2 block4, 3 block6.
	for name, tc := range map[string]struct {
		index            int
		address, message string
	}{
		"IPv4 shorter than /24":     {2, "203.0.112.0/23", shape},
		"IPv6 shorter than /48":     {3, "2001:db8::/47", shape},
		"host bits":                 {2, "203.0.113.1/29", shape},
		"IPv6 host bits":            {3, "2001:db8:53::1/64", shape},
		"unspecified":               {2, "0.0.0.0/24", shape},
		"multicast":                 {2, "224.0.0.0/24", shape},
		"IPv6 multicast":            {3, "ff05::/64", shape},
		"loopback":                  {2, "127.0.0.0/24", shape},
		"link-local":                {2, "169.254.1.0/24", shape},
		"IPv6 link-local":           {3, "fe80::/64", shape},
		"mapped":                    {3, "::ffff:203.0.113.0/120", shape},
		"holds the broadcast":       {2, "255.255.255.0/24", shape},
		"inside a segment's subnet": {2, "192.0.2.0/29", "This block overlaps a segment's subnet."},
		"a segment's whole subnet":  {2, "192.0.2.0/24", "This block overlaps a segment's subnet."},
		"a v6 segment's subnet":     {3, "fd00:1::/64", "This block overlaps a segment's subnet."},
		"holds a virtual address":   {2, "172.16.53.0/24", `This range overlaps virtual address "dns4".`},
		"holds a v6 one":            {2, "fd00:53:53:53::/64", `This range overlaps virtual address "dns6".`},
		"the same block":            {3, "203.0.113.0/29", "Another virtual address already has this address."},
		"an address inside a block": {3, "203.0.113.4", `This address is inside virtual address "block4".`},
	} {
		t.Run(name, func(t *testing.T) {
			d := blockFixture(t)
			item(d["virtualAddresses"].([]any), tc.index)["address"] = tc.address
			refusedWith(t, d, fmt.Sprintf("/virtualAddresses/%d/address", tc.index), tc.message)
		})
	}
	// The hosts the declaration names: a held address or block would take their traffic.
	const admin = "adr_00000005-0000-4000-8000-000000000005"
	manage := func(d map[string]any, a map[string]any) {
		a["id"], a["name"] = admin, "admin"
		objs := d["aliases"].(map[string]any)
		objs["addresses"] = append(objs["addresses"].([]any), a)
		d["management"] = map[string]any{"segment": item(segmentsOf(d), 1)["id"], "allowedSources": []any{admin}}
	}
	for name, tc := range map[string]struct {
		index   int
		edit    func(d map[string]any)
		message string
	}{
		"a management source host": {2, func(d map[string]any) { manage(d, map[string]any{"kind": "host", "address": "203.0.113.6"}) },
			"This range holds a management source address."},
		"a management source network inside a block": {2, func(d map[string]any) { manage(d, map[string]any{"kind": "network", "prefix": "203.0.113.4/30"}) },
			"This range holds a management source address."},
		"an IPv6 management source": {3, func(d map[string]any) { manage(d, map[string]any{"kind": "host", "address": "2001:db8:53::9"}) },
			"This range holds a management source address."},
		"an IPv6 management range inside a block": {3, func(d map[string]any) {
			manage(d, map[string]any{"kind": "range", "first": "2001:db8:53::10", "last": "2001:db8:53::20"})
		}, "This range holds a management source address."},
	} {
		t.Run(name, func(t *testing.T) {
			d := blockFixture(t)
			tc.edit(d)
			refusedWith(t, d, fmt.Sprintf("/virtualAddresses/%d/address", tc.index), tc.message)
		})
	}
	// A management network or range takes only what lies inside it: an address or a block inside a wide source
	// network, as 0.0.0.0/0, is no lockout.
	t.Run("inside a wide management network", func(t *testing.T) {
		d := blockFixture(t)
		manage(d, map[string]any{"kind": "network", "prefix": "0.0.0.0/0"})
		accepted(t, d)
		d = blockFixture(t)
		manage(d, map[string]any{"kind": "network", "prefix": "2001:db8::/32"}) // the organisation's whole allocation
		accepted(t, d)
	})
	// A single held address keeps the rules it had: a saved version with one on a management host still loads.
	t.Run("a single address keeps its rules", func(t *testing.T) {
		d := blockFixture(t)
		manage(d, map[string]any{"kind": "host", "address": "172.16.53.53"})
		accepted(t, d)
	})
	t.Run("an IPv6 block over the IPv4-mapped range", func(t *testing.T) {
		d := blockFixture(t)
		item(d["virtualAddresses"].([]any), 3)["address"] = "::ffc0:0:0/90"
		refusedAt(t, d, "/virtualAddresses/3/address")
	})
	t.Run("a DHCPv6 delegation pool, both ways", func(t *testing.T) {
		d := dhcp6Fixture(t, nil)
		d["virtualAddresses"] = []any{map[string]any{"id": vipP, "name": "block6", "address": "fd00:beef:1:200::/56"}}
		refusedWith(t, d, "/virtualAddresses/0/address", "This range overlaps a DHCPv6 prefix delegation pool.")
		refusedWith(t, d, "/dhcp6/0/prefixDelegation/prefix", "The pool holds a virtual address.")
		// A single held address keeps its old rules, so a saved version holding one in a pool still loads.
		d["virtualAddresses"] = []any{map[string]any{"id": vipP, "name": "one", "address": "fd00:beef:1:200::1"}}
		accepted(t, d)
		d["virtualAddresses"] = []any{map[string]any{"id": vipP, "name": "block6", "address": "fd00:beef:2::/64"}}
		accepted(t, d)
	})
	// A server the declaration names may be a service on the firewall itself.
	t.Run("named servers", func(t *testing.T) {
		d := blockFixture(t)
		d["dhcp"] = []any{lanDHCP(d, map[string]any{"dnsServers": []any{"172.16.53.53", "203.0.113.5"}, "ntpServers": []any{"203.0.113.6"}})}
		d["dns"] = map[string]any{"mode": "forward", "segments": []any{}, "upstreams": []any{map[string]any{"address": "203.0.113.7:5353", "protocol": "do53"}}}
		accepted(t, d)
	})
	t.Run("overlaps a proxy ARP entry", func(t *testing.T) {
		d := proxyFixture(t)
		d["virtualAddresses"] = append(d["virtualAddresses"].([]any), map[string]any{"id": vipR, "name": "over", "address": "192.0.2.56/29"})
		refusedAt(t, d, "/virtualAddresses/4/address")
	})
	t.Run("the DNS service on a block", func(t *testing.T) {
		d := blockFixture(t)
		d["dns"] = map[string]any{"mode": "recursive", "segments": []any{}, "virtualAddresses": []any{vipA, vipP}}
		refusedAt(t, d, "/dns/virtualAddresses/1")
		_, e := decodeMap(t, d)
		if raw, _ := json.Marshal(e); !strings.Contains(string(raw), "The DNS service listens on single addresses; name one address, not a block.") {
			t.Fatalf("%s", raw)
		}
	})
	t.Run("an IPv6 forward to an address inside a block", func(t *testing.T) {
		d := blockFixture(t)
		setNAT(d, "portForwards", forward(d, map[string]any{"family": "ipv6", "target": map[string]any{"address": "2001:db8:53::35"}, "targetPort": nil, "ports": "53"}))
		accepted(t, d)
	})
	t.Run("1:1 NAT takes over no address of a block", func(t *testing.T) {
		d := blockFixture(t)
		setNAT(d, "oneToOne", map[string]any{"id": otoA, "name": "over", "enabled": true, "zone": wanZone(d), "external": "203.0.113.4", "internal": "192.0.2.50", "destination": []any{}})
		refusedAt(t, d, "/nat/oneToOne/0/external")
		setNAT(d, "oneToOne", map[string]any{"id": otoA, "name": "under", "enabled": true, "zone": wanZone(d), "external": "198.51.100.50", "internal": "203.0.113.4", "destination": []any{}})
		refusedAt(t, d, "/nat/oneToOne/0/internal")
	})
}

// A block a forward targets inside cannot be deleted, nor narrowed so the target falls outside it; widening it, or
// another held address taking the target over, is fine.
func TestRoutedBlockInUse(t *testing.T) {
	s := seededState(t)
	p := mustRun(t, &s, `{"operations":[{"op":"create","collection":"virtualAddresses","localId":"v","value":{"name":"block","address":"203.0.113.0/29"}}]}`)
	id := p.Created["v"]
	mustRun(t, &s, `{"operations":[{"op":"create","collection":"nat.portForwards","value":{"name":"dns","enabled":true,"ingressZones":["`+s.Config.Zones[0].ID+`"],"protocol":"udp","from":[],"to":{"self":true},"ports":"53","target":{"address":"203.0.113.3"}}}]}`)
	version := func() string { return string(versionOf(&s, objectKey{"virtualAddresses", id}).Version) }
	replace := func(address string) string {
		return `{"operations":[{"op":"replace","collection":"virtualAddresses","id":"` + id + `","expectedVersion":"` + version() + `","value":{"name":"block","address":"` + address + `"}}]}`
	}
	for name, body := range map[string]string{
		"delete":    `{"operations":[{"op":"delete","collection":"virtualAddresses","id":"` + id + `","expectedVersion":"` + version() + `"}]}`,
		"narrow":    replace("203.0.113.4/30"),
		"to one":    replace("203.0.113.2"),
		"elsewhere": replace("203.0.114.0/29"),
	} {
		_, e := run(t, &s, body)
		raw, _ := json.Marshal(e)
		if e == nil || !strings.Contains(string(raw), "/operations/0/id") || !strings.Contains(string(raw), `Port forward \"dns\" targets this address`) {
			t.Fatalf("%s: %s", name, raw)
		}
	}
	mustRun(t, &s, replace("203.0.113.0/30"))
	mustRun(t, &s, replace("203.0.113.0/24"))
	mustRun(t, &s, replace("203.0.113.3"))
	mustRun(t, &s, replace("203.0.113.0/28"))
}

// refusedWith checks that the declaration is refused at path with a message containing message.
func refusedWith(t *testing.T, d map[string]any, path, message string) {
	t.Helper()
	_, e := decodeMap(t, d)
	ve, _ := e.(validationError)
	if !slices.ContainsFunc(ve, func(f FieldError) bool { return f.Path == path && strings.Contains(f.Message, message) }) {
		t.Fatalf("error %v; fields %+v; want %s: %s", e, ve, path, message)
	}
}

// ndpFixture adds a proxy NDP entry to the virtual address fixture: the WAN on a static 2001:db8:1::/64 answers for an
// inside host on the LAN.
func ndpFixture(t *testing.T) map[string]any {
	t.Helper()
	d := vipFixture(t)
	item(segmentsOf(d), 0)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "static", "addresses": []any{"2001:db8:1::2/64"}, "gateway": "2001:db8:1::1"}
	d["virtualAddresses"] = append(d["virtualAddresses"].([]any), map[string]any{"id": vipP, "name": "host", "kind": "proxyNdp",
		"segment": item(segmentsOf(d), 0)["id"], "hostSegment": item(segmentsOf(d), 1)["id"], "address": "2001:db8:1::50"})
	return d
}

func TestProxyNDPValidation(t *testing.T) {
	got := accepted(t, ndpFixture(t))
	if v := got.VirtualAddresses[2]; v.Kind != "proxyNdp" || v.Address != "2001:db8:1::50" || v.Segment == "" || v.HostSegment == "" {
		t.Fatalf("decoded %+v", got.VirtualAddresses)
	}
	vips := func(d map[string]any) []any { return d["virtualAddresses"].([]any) }
	t.Run("an uplink by DHCPv6 has no known subnet", func(t *testing.T) {
		d := ndpFixture(t)
		item(segmentsOf(d), 0)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "dhcp6", "requestPrefixLength": 48}
		item(vips(d), 2)["address"] = "2001:db8:7::50"
		accepted(t, d)
	})
	const p = "/virtualAddresses/2"
	const shape = "Expected one global unicast IPv6 address, without a length."
	for name, tc := range map[string]struct {
		edit    func(d map[string]any)
		path    string
		message string
	}{
		"a length":        {func(d map[string]any) { item(vips(d), 2)["address"] = "2001:db8:1::50/128" }, p + "/address", shape},
		"a prefix":        {func(d map[string]any) { item(vips(d), 2)["address"] = "2001:db8:1::/64" }, p + "/address", shape},
		"unique local":    {func(d map[string]any) { item(vips(d), 2)["address"] = "fd00:1::50" }, p + "/address", shape},
		"link-local":      {func(d map[string]any) { item(vips(d), 2)["address"] = "fe80::50" }, p + "/address", shape},
		"with a zone":     {func(d map[string]any) { item(vips(d), 2)["address"] = "2001:db8:1::50%eth0" }, p + "/address", shape},
		"multicast":       {func(d map[string]any) { item(vips(d), 2)["address"] = "ff0e::50" }, p + "/address", shape},
		"loopback":        {func(d map[string]any) { item(vips(d), 2)["address"] = "::1" }, p + "/address", shape},
		"unspecified":     {func(d map[string]any) { item(vips(d), 2)["address"] = "::" }, p + "/address", shape},
		"IPv4-mapped":     {func(d map[string]any) { item(vips(d), 2)["address"] = "::ffff:198.51.100.50" }, p + "/address", shape},
		"IPv4":            {func(d map[string]any) { item(vips(d), 2)["address"] = "198.51.100.50" }, p + "/address", shape},
		"no segment":      {func(d map[string]any) { delete(item(vips(d), 2), "segment") }, p + "/segment", "Expected an existing segment."},
		"no host segment": {func(d map[string]any) { delete(item(vips(d), 2), "hostSegment") }, p + "/hostSegment", "Expected an existing segment."},
		"unknown host segment": {func(d map[string]any) {
			item(vips(d), 2)["hostSegment"] = "seg_00000009-0000-4000-8000-000000000009"
		}, p + "/hostSegment", "Expected an existing segment."},
		"the same segment twice": {func(d map[string]any) { item(vips(d), 2)["hostSegment"] = item(vips(d), 2)["segment"] }, p + "/hostSegment", "Expected a segment other than the one the firewall answers on."},
		"off the static subnet":  {func(d map[string]any) { item(vips(d), 2)["address"] = "2001:db8:2::50" }, p + "/address", "Expected an address on one of the segment's static IPv6 subnets."},
		"a firewall address":     {func(d map[string]any) { item(vips(d), 2)["address"] = "2001:db8:1::2" }, p + "/address", "This address is one of the firewall's own addresses."},
		"another proxy NDP entry": {func(d map[string]any) {
			d["virtualAddresses"] = append(vips(d), map[string]any{"id": vipQ, "name": "twin", "kind": "proxyNdp", "segment": item(vips(d), 2)["segment"], "hostSegment": item(vips(d), 2)["hostSegment"], "address": "2001:db8:1::50"})
		}, "/virtualAddresses/3/address", `This address overlaps virtual address \"host\".`}, // quoted as the JSON-encoded error shows it
		"a held address": {func(d map[string]any) { item(vips(d), 1)["address"] = "2001:db8:1::50" }, p + "/address", `This address overlaps virtual address \"dns6\".`},
		"a held block": {func(d map[string]any) {
			item(segmentsOf(d), 0)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "dhcp6", "requestPrefixLength": 48}
			item(vips(d), 1)["address"] = "2001:db8:1::/64"
		}, p + "/address", `This address overlaps virtual address \"dns6\".`},
		"on the host segment's subnet": {func(d map[string]any) {
			item(segmentsOf(d), 0)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "dhcp6", "requestPrefixLength": 48}
			v6(d, 1)["addresses"] = []any{"fd00:1::1/64", "2001:db8:1::1/64"}
		}, p + "/address", "This address is on the host segment's subnet, where a host already has a route."},
		"in a DHCPv6 delegation pool": {func(d map[string]any) {
			d["dhcp6"] = []any{map[string]any{"id": "dh6_00000001-0000-4000-8000-000000000001", "name": "lan6", "segment": item(segmentsOf(d), 1)["id"],
				"ranges": []any{map[string]any{"first": "fd00:1::1000", "last": "fd00:1::1fff"}}, "prefixDelegation": map[string]any{"prefix": "2001:db8:1::/56", "delegatedLength": 64}}}
			item(segmentsOf(d), 0)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "dhcp6", "requestPrefixLength": 48}
		}, p + "/address", "This address is in a DHCPv6 prefix delegation pool."},
		"a host segment on an address": {func(d map[string]any) {
			item(vips(d), 0)["hostSegment"] = item(segmentsOf(d), 1)["id"]
		}, "/virtualAddresses/0/hostSegment", "Only a proxy NDP entry names a host segment."},
		"dns listens on it": {func(d map[string]any) {
			d["dns"] = map[string]any{"mode": "recursive", "segments": []any{}, "virtualAddresses": []any{vipA, vipP}}
		}, "/dns/virtualAddresses/1", "The firewall does not hold an address answered by proxy NDP, so the DNS service cannot listen on it."},
		"forward target": {func(d map[string]any) {
			setNAT(d, "portForwards", forward(d, map[string]any{"family": "ipv6", "target": map[string]any{"address": "2001:db8:1::50"}, "targetPort": nil, "ports": "443"}))
		}, "/nat/portForwards/0/target/address", "Expected a static IPv6 address of the firewall"},
	} {
		t.Run(name, func(t *testing.T) {
			d := ndpFixture(t)
			tc.edit(d)
			refusedAt(t, d, tc.path)
			_, e := decodeMap(t, d)
			if raw, _ := json.Marshal(e); !strings.Contains(string(raw), tc.message) {
				t.Fatalf("%s", raw)
			}
		})
	}
	t.Run("a host segment on a proxy ARP entry", func(t *testing.T) {
		d := proxyFixture(t)
		item(d["virtualAddresses"].([]any), 2)["hostSegment"] = item(segmentsOf(d), 0)["id"]
		refusedWith(t, d, "/virtualAddresses/2/hostSegment", "Only a proxy NDP entry names a host segment.")
	})
}

// A batch can name the segments it creates in a proxy NDP entry by label.
func TestProxyNDPSegmentsByLabel(t *testing.T) {
	c, e := DecodeChanges([]byte(`{"operations":[{"op":"create","collection":"links","localId":"uplink","value":{"name":"uplink","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01"}},{"op":"create","collection":"links","localId":"inside","value":{"name":"inside","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:02"}},{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},{"op":"create","collection":"zones","localId":"lan","value":{"name":"lan"}},
		{"op":"create","collection":"segments","localId":"up","value":{"name":"wan","link":"@uplink","zone":"@wan","addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","addresses":["2001:db8:1::2/64"]}}}},
		{"op":"create","collection":"segments","localId":"in","value":{"name":"lan","link":"@inside","zone":"@lan","addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"none"}}}},
		{"op":"create","collection":"virtualAddresses","localId":"v","value":{"name":"host","kind":"proxyNdp","segment":"@up","hostSegment":"@in","address":"2001:db8:1::50"}}]}`))
	if e != nil {
		t.Fatal(e)
	}
	s := initialState()
	p, e := prepareChanges(&s, c, "1", fixedTime)
	if e != nil {
		t.Fatal(e)
	}
	if v := p.State.Config.VirtualAddresses[0]; v.Segment != p.Created["up"] || v.HostSegment != p.Created["in"] {
		t.Fatalf("segments %q %q, created %v", v.Segment, v.HostSegment, p.Created)
	}
}

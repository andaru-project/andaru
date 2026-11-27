package config

import (
	"fmt"
	"net/netip"
	"slices"
)

// vipDevice is the dummy interface that holds the virtual addresses; no link may take its name.
const vipDevice = "andaru-vip"

// staticPrefixes are a segment's declared static addresses, IPv4 and IPv6, that parse.
func staticPrefixes(s segment) []netip.Prefix {
	var out []netip.Prefix
	for _, raw := range append(slices.Clone(s.Addressing.IPv4.Addresses), s.Addressing.IPv6.Addresses...) {
		if p, e := netip.ParsePrefix(raw); e == nil {
			out = append(out, p)
		}
	}
	return out
}

// validateVirtual checks the virtual addresses: single unicast addresses the firewall holds on no segment.
func validateVirtual(d *declaration, bad func(string, string)) {
	var subnets []netip.Prefix
	for _, s := range d.Segments {
		subnets = append(subnets, staticPrefixes(s)...)
	}
	seen := map[string]bool{}
	held := map[netip.Addr]bool{}
	var proxies []virtualAddress
	taken := claims(d)
	for i, v := range d.VirtualAddresses {
		p := fmt.Sprintf("/virtualAddresses/%d", i)
		if seen[v.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		if invalidText(v.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if seen["name:"+v.Name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		seen[v.ID], seen["name:"+v.Name] = true, true
		if v.proxied() {
			if validateProxy(d, v, p, subnets, taken, proxies, bad) {
				proxies = append(proxies, v)
			}
			continue
		}
		if v.Segment != "" {
			bad(p+"/segment", "Only a proxy ARP entry names a segment.")
		}
		// An address inside a proxy ARP entry is on its segment's subnet, so the subnet rule below refuses it too.
		a, e := netip.ParseAddr(v.Address)
		switch {
		case e != nil || a.Zone() != "" || a.Is4In6() || !validUnicast(a) || a.IsLinkLocalUnicast():
			bad(p+"/address", "Expected one unicast IPv4 or IPv6 address, without a length; not loopback or link-local.")
		case held[a]:
			bad(p+"/address", "Another virtual address already has this address.")
		case slices.ContainsFunc(subnets, func(s netip.Prefix) bool { return s.Contains(a) }):
			// Linux answers for a local address on every interface, so it would work there by accident.
			bad(p+"/address", "This address is on a segment's subnet: add it to the segment's addresses instead.")
		default:
			held[a] = true
		}
	}
	if d.DNS != nil {
		for j, id := range d.DNS.VirtualAddresses {
			if slices.Contains(d.DNS.VirtualAddresses[:j], id) {
				bad(fmt.Sprintf("/dns/virtualAddresses/%d", j), "This virtual address is listed twice.")
			} else if i := slices.IndexFunc(d.VirtualAddresses, func(v virtualAddress) bool { return v.ID == id }); i < 0 {
				bad(fmt.Sprintf("/dns/virtualAddresses/%d", j), "Expected an existing virtual address.")
			} else if d.VirtualAddresses[i].proxied() {
				bad(fmt.Sprintf("/dns/virtualAddresses/%d", j), "The firewall does not hold an address answered by proxy ARP, so the DNS service cannot listen on it.")
			}
		}
	}
}

// firewallHolds says whether a is one of the firewall's declared static addresses or virtual addresses.
func firewallHolds(d *declaration, a netip.Addr) bool {
	for _, s := range d.Segments {
		if slices.ContainsFunc(staticPrefixes(s), func(p netip.Prefix) bool { return p.Addr() == a }) {
			return true
		}
	}
	return slices.ContainsFunc(d.VirtualAddresses, func(v virtualAddress) bool { return !v.proxied() && sameAddress(v.Address, a.String()) })
}

// proxyPrefix is a proxy ARP entry's address as a prefix: a bare address is a /32.
func proxyPrefix(v virtualAddress) (netip.Prefix, bool) {
	if a, e := netip.ParseAddr(v.Address); e == nil {
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	p, e := netip.ParsePrefix(v.Address)
	return p, e == nil
}

// claim is a span of addresses the declaration gives to a host, or to the subnet itself: no proxy ARP entry may cover it.
type claim struct {
	first, last netip.Addr
	message     string
}

// claims are the addresses the declaration names for hosts other than the firewall: a proxy ARP entry's route to
// andaru-vip would take them over.
func claims(d *declaration) []claim {
	var out []claim
	one := func(raw, message string) {
		a, e := netip.ParseAddr(raw)
		if e != nil {
			ap, e2 := netip.ParseAddrPort(raw)
			a, e = ap.Addr(), e2
		}
		if e == nil && a.Unmap().Is4() {
			out = append(out, claim{a.Unmap(), a.Unmap(), message})
		}
	}
	for _, s := range d.Segments {
		// Its route to andaru-vip would take the uplink's next hop with it.
		one(s.Addressing.IPv4.Gateway, "This range holds a segment's gateway.")
		if s.Addressing.DNS != nil {
			for _, raw := range s.Addressing.DNS.Servers {
				one(raw, "This range holds a segment's DNS server.")
			}
		}
		if s.Addressing.IPv4.Mode != "static" {
			continue
		}
		for _, raw := range s.Addressing.IPv4.Addresses {
			if p, e := netip.ParsePrefix(raw); e == nil && p.Addr().Is4() && p.Bits() < 31 {
				out = append(out, claim{p.Masked().Addr(), p.Masked().Addr(), "This range holds its subnet's network address."})
				b := lastOf(p.Masked())
				out = append(out, claim{b, b, "This range holds its subnet's broadcast address."})
			}
		}
	}
	for _, e := range d.DHCP {
		for _, r := range e.Ranges {
			first, e1 := netip.ParseAddr(r.First)
			last, e2 := netip.ParseAddr(r.Last)
			if e1 == nil && e2 == nil && first.Is4() && last.Is4() {
				out = append(out, claim{first, last, "This range overlaps a DHCP range."})
			}
		}
		for _, l := range e.StaticLeases {
			one(l.Address, "This range holds a DHCP static lease.")
		}
		one(e.Gateway, "This range holds a DHCP entry's gateway.")
	}
	if d.DNS != nil {
		for _, u := range d.DNS.Upstreams {
			one(u.Address, "This range holds a DNS upstream.")
		}
	}
	if d.Management != nil {
		byID := map[string]address{}
		for _, a := range d.Aliases.Addresses {
			byID[a.ID] = a
		}
		seen := map[string]bool{}
		for pending := slices.Clone(d.Management.AllowedSources); len(pending) > 0; pending = pending[1:] {
			if a := byID[pending[0]]; !seen[a.ID] {
				seen[a.ID] = true
				pending = append(pending, a.Members...)
				if a.Kind == "host" {
					one(a.Address, "This range holds a management source address.")
				}
			}
		}
	}
	return out
}

// lastOf is the last address of an IPv4 prefix.
func lastOf(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	n |= 1<<(32-p.Bits()) - 1
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

// validateProxy checks one proxy ARP entry against its segment, the firewall's static addresses (own), the hosts the
// declaration names, and the entries before it; it says whether v is valid.
func validateProxy(d *declaration, v virtualAddress, p string, own []netip.Prefix, taken []claim, before []virtualAddress, bad func(string, string)) bool {
	seg := slices.IndexFunc(d.Segments, func(s segment) bool { return s.ID == v.Segment })
	if seg < 0 {
		bad(p+"/segment", "Expected an existing segment.")
	}
	x, ok := proxyPrefix(v)
	a := x.Addr()
	var last netip.Addr
	if ok && a.Is4() {
		last = lastOf(x)
	}
	claimed := slices.IndexFunc(taken, func(c claim) bool { return !c.last.Less(a) && !last.Less(c.first) })
	overlap := slices.IndexFunc(before, func(o virtualAddress) bool { y, _ := proxyPrefix(o); return y.Overlaps(x) })
	switch {
	case ok && a.Is6() && !a.Is4In6():
		bad(p+"/address", "Proxy NDP is not offered yet (#43).")
	case !ok || !a.Is4() || x.Bits() < 24 || x != x.Masked() || !validUnicast(a) || a.IsLinkLocalUnicast():
		bad(p+"/address", "Expected an IPv4 address, or a prefix of length 24 to 32 with its host bits zero; not loopback or link-local.")
	case seg < 0:
	case d.Segments[seg].Addressing.IPv4.Mode != "static":
		bad(p+"/segment", "Expected a segment with static IPv4 addresses: one addressed by DHCP or PPPoE has no known subnet.")
	case !slices.ContainsFunc(staticPrefixes(d.Segments[seg]), func(s netip.Prefix) bool { return s.Addr().Is4() && s.Bits() <= x.Bits() && s.Contains(a) }):
		bad(p+"/address", "Expected an address on one of the segment's static IPv4 subnets.")
	case slices.ContainsFunc(own, func(s netip.Prefix) bool { return x.Contains(s.Addr()) }):
		bad(p+"/address", "This range holds one of the firewall's own addresses.")
	case claimed >= 0:
		bad(p+"/address", taken[claimed].message)
	case overlap >= 0:
		bad(p+"/address", fmt.Sprintf("This range overlaps proxy ARP entry %q.", before[overlap].Name))
	default:
		return true
	}
	return false
}

// proxied are the valid-looking proxy ARP entries' prefixes: no host holds an address in them.
func proxied(d *declaration) []netip.Prefix {
	var out []netip.Prefix
	for _, v := range d.VirtualAddresses {
		if x, ok := proxyPrefix(v); ok && v.proxied() {
			out = append(out, x.Masked())
		}
	}
	return out
}

// sameAddress compares two addresses however they are written.
func sameAddress(a, b string) bool {
	x, e1 := netip.ParseAddr(a)
	y, e2 := netip.ParseAddr(b)
	return e1 == nil && e2 == nil && x == y
}

// vipStillTargeted refuses an operation that deletes a virtual address, or changes its address, while a port
// forward targets the address: the forward would silently become one to an inside host nobody holds.
func vipStillTargeted(old, d *declaration, input Changes) *Fault {
	for _, v := range old.VirtualAddresses {
		if v.proxied() || slices.ContainsFunc(d.VirtualAddresses, func(n virtualAddress) bool { return !n.proxied() && sameAddress(n.Address, v.Address) }) {
			continue
		}
		i := slices.IndexFunc(d.NAT.PortForwards, func(f portForward) bool { return f.Target != nil && sameAddress(f.Target.Address, v.Address) })
		if i < 0 {
			continue
		}
		for j, op := range input.Operations {
			if op.Collection == "virtualAddresses" && op.ID == v.ID {
				f := fault("VALIDATION_FAILED", "", "")
				f.Details = []FieldError{{input.path(j, "/id", false), fmt.Sprintf("Port forward %q targets this address; change or delete it first.", d.NAT.PortForwards[i].Name)}}
				return f
			}
		}
	}
	return nil
}

// disabledHolder names the disabled link whose segment declares a, or "".
func disabledHolder(d *declaration, a netip.Addr) string {
	for _, s := range d.Segments {
		if !slices.ContainsFunc(staticPrefixes(s), func(p netip.Prefix) bool { return p.Addr() == a }) {
			continue
		}
		for _, l := range d.Links {
			if l.ID == s.Link && !l.Enabled {
				return l.Name
			}
		}
	}
	return ""
}

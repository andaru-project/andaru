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

// validateVirtual checks the virtual addresses: unicast addresses or blocks the firewall holds on no segment.
func validateVirtual(d *declaration, bad func(string, string)) {
	var subnets []netip.Prefix
	for _, s := range d.Segments {
		subnets = append(subnets, staticPrefixes(s)...)
	}
	seen := map[string]bool{}
	var held []int // the valid held addresses and blocks, by index
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
		if v.HostSegment != "" && v.Kind != "proxyNdp" {
			bad(p+"/hostSegment", "Only a proxy NDP entry names a host segment.")
		}
		if v.Kind == "proxyNdp" {
			validateProxyNDP(d, i, p, subnets, taken, bad)
			continue
		}
		if v.proxied() {
			if validateProxy(d, v, p, subnets, taken, proxies, bad) {
				proxies = append(proxies, v)
			}
			continue
		}
		if v.Segment != "" {
			bad(p+"/segment", "Only a proxy ARP or proxy NDP entry names a segment.")
		}
		// An address inside a proxy ARP entry is on its segment's subnet, so the subnet rule below refuses it too.
		x, ok := vipPrefix(v)
		a, block := x.Addr(), !x.IsSingleIP()
		minBits := 48
		if a.Is4() {
			minBits = 24
		}
		other := slices.IndexFunc(held, func(h int) bool { y, _ := vipPrefix(d.VirtualAddresses[h]); return y.Overlaps(x) })
		// A named server may be a service on the firewall itself, so only the hosts the declaration names count. Only
		// blocks, a new form: a saved single address that one of these refusals would now catch must still load.
		claimed := -1
		if block {
			claimed = slices.IndexFunc(taken, func(c claim) bool { return !c.served && c.overlaps(a, lastOf(x)) })
		}
		switch {
		case !ok || !blockShape(x, minBits):
			bad(p+"/address", "Expected one unicast IPv4 or IPv6 address, or a block: an IPv4 prefix of length 24 to 32 or an IPv6 prefix of length 48 to 128, with its host bits zero; not loopback or link-local.")
		case other >= 0:
			o := d.VirtualAddresses[held[other]]
			switch y, _ := vipPrefix(o); {
			case y == x:
				bad(p+"/address", "Another virtual address already has this address.")
			case !block:
				bad(p+"/address", fmt.Sprintf("This address is inside virtual address %q.", o.Name))
			default:
				bad(p+"/address", fmt.Sprintf("This range overlaps virtual address %q.", o.Name))
			}
		case block && slices.ContainsFunc(subnets, x.Overlaps):
			bad(p+"/address", "This block overlaps a segment's subnet.")
		case slices.ContainsFunc(subnets, func(s netip.Prefix) bool { return s.Contains(a) }):
			// Linux answers for a local address on every interface, so it would work there by accident.
			bad(p+"/address", "This address is on a segment's subnet: add it to the segment's addresses instead.")
		case claimed >= 0:
			// The local route would take the host's traffic, replies to it included.
			bad(p+"/address", taken[claimed].message)
		default:
			held = append(held, i)
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
			} else if !d.VirtualAddresses[i].held() {
				bad(fmt.Sprintf("/dns/virtualAddresses/%d", j), "The firewall does not hold an address answered by proxy NDP, so the DNS service cannot listen on it.")
			} else if x, ok := vipPrefix(d.VirtualAddresses[i]); ok && !x.IsSingleIP() {
				bad(fmt.Sprintf("/dns/virtualAddresses/%d", j), "The DNS service listens on single addresses; name one address, not a block.")
			}
		}
	}
}

// firewallHolds says whether a is one of the firewall's declared static addresses, or a held virtual address or an
// address inside a held block.
func firewallHolds(d *declaration, a netip.Addr) bool {
	for _, s := range d.Segments {
		if slices.ContainsFunc(staticPrefixes(s), func(p netip.Prefix) bool { return p.Addr() == a }) {
			return true
		}
	}
	return holdsVirtual(d, a)
}

// holdsVirtual says whether a is a held virtual address of d, or inside a held block.
func holdsVirtual(d *declaration, a netip.Addr) bool {
	return slices.ContainsFunc(d.VirtualAddresses, func(v virtualAddress) bool { return v.held() && heldContains(v, a) })
}

// mapped is the IPv4-mapped IPv6 range, which no virtual address may touch.
var mapped = netip.MustParsePrefix("::ffff:0:0/96")

// blockShape says whether x is a unicast address, or a prefix of at least minBits with its host bits zero, none of
// whose addresses is unspecified, loopback, multicast, link-local, IPv4-mapped or the IPv4 limited broadcast.
func blockShape(x netip.Prefix, minBits int) bool {
	a := x.Addr()
	return x.IsValid() && x.Bits() >= minBits && x == x.Masked() && !x.Overlaps(mapped) && validUnicast(a) && !a.IsLinkLocalUnicast() && validUnicast(lastOf(x))
}

// heldContains says whether the held virtual address v is a, or is a block holding it.
func heldContains(v virtualAddress, a netip.Addr) bool {
	x, ok := vipPrefix(v)
	return ok && x.Contains(a)
}

// vipPrefix is a virtual address's address as a prefix: a bare address is a /32 or /128.
func vipPrefix(v virtualAddress) (netip.Prefix, bool) {
	if a, e := netip.ParseAddr(v.Address); e == nil {
		return netip.PrefixFrom(a, a.BitLen()), a.Zone() == ""
	}
	p, e := netip.ParsePrefix(v.Address)
	return p, e == nil
}

// claim is a span of addresses the declaration gives to a host, or to the subnet itself: no proxy ARP entry or held
// virtual address may cover it. A served one is a server the declaration names (a DNS or NTP server, an upstream, a
// DHCP option), which may be a service on the firewall itself.
type claim struct {
	first, last netip.Addr
	message     string
	served      bool
	// whole: a management source network or range names where admins may come from, not where they are. Only an
	// entry holding all of it takes their traffic for sure; one inside a wide source, as an organisation's /32, is an
	// address the operator routes to the firewall, where no admin sits.
	whole bool
}

// overlaps says whether first to last, of the claim's family, takes the claim: shares an address with it, or holds
// all of it when the claim is whole.
func (c claim) overlaps(first, last netip.Addr) bool {
	if c.first.Is4() != first.Is4() {
		return false
	}
	if c.whole {
		return !c.first.Less(first) && !last.Less(c.last)
	}
	return !c.last.Less(first) && !last.Less(c.first)
}

// The claims a proxy NDP entry may not take either.
const (
	gatewayClaim = "This range holds a segment's gateway."
	poolClaim    = "This range overlaps a DHCPv6 prefix delegation pool."
)

// claims are the addresses the declaration names for hosts other than the firewall: a proxy ARP entry's route to
// andaru-vip, or a held block's local route, would take them over.
func claims(d *declaration) []claim {
	var out []claim
	add := func(raw, message string, served bool) {
		a, e := netip.ParseAddr(raw)
		if e != nil {
			ap, e2 := netip.ParseAddrPort(raw)
			a, e = ap.Addr(), e2
		}
		if e == nil && a.Zone() == "" {
			out = append(out, claim{a.Unmap(), a.Unmap(), message, served, false})
		}
	}
	one := func(raw, message string) { add(raw, message, false) }
	server := func(raw, message string) { add(raw, message, true) }
	span := func(p netip.Prefix, message string) {
		out = append(out, claim{p.Masked().Addr(), lastOf(p), message, false, false})
	}
	for _, s := range d.Segments {
		// Its route to andaru-vip would take the uplink's next hop with it.
		one(s.Addressing.IPv4.Gateway, gatewayClaim)
		one(s.Addressing.IPv6.Gateway, gatewayClaim)
		if s.Addressing.DNS != nil {
			for _, raw := range s.Addressing.DNS.Servers {
				server(raw, "This range holds a segment's DNS server.")
			}
		}
		if s.Addressing.IPv4.Mode != "static" {
			continue
		}
		for _, raw := range s.Addressing.IPv4.Addresses {
			if p, e := netip.ParsePrefix(raw); e == nil && p.Addr().Is4() && p.Bits() < 31 {
				out = append(out, claim{p.Masked().Addr(), p.Masked().Addr(), "This range holds its subnet's network address.", false, false})
				b := lastOf(p.Masked())
				out = append(out, claim{b, b, "This range holds its subnet's broadcast address.", false, false})
			}
		}
	}
	for _, e := range d.DHCP {
		for _, r := range e.Ranges {
			first, e1 := netip.ParseAddr(r.First)
			last, e2 := netip.ParseAddr(r.Last)
			if e1 == nil && e2 == nil && first.Is4() && last.Is4() {
				out = append(out, claim{first, last, "This range overlaps a DHCP range.", false, false})
			}
		}
		for _, l := range e.StaticLeases {
			one(l.Address, "This range holds a DHCP static lease.")
		}
		server(e.Gateway, "This range holds a DHCP entry's gateway.")
		for _, raw := range e.DNSServers {
			server(raw, "This range holds a DHCP entry's DNS server.")
		}
		for _, raw := range e.NTPServers {
			server(raw, "This range holds a DHCP entry's NTP server.")
		}
		server(e.NextServer, "This range holds a DHCP entry's next server.")
	}
	// A delegated segment's pool is under a prefix learned at run time; the renderer warns about that one.
	for _, e := range d.DHCP6 {
		if e.PrefixDelegation != nil {
			if p, err := netip.ParsePrefix(e.PrefixDelegation.Prefix); err == nil && p.Addr().Is6() {
				span(p, poolClaim)
			}
		}
	}
	if d.DNS != nil {
		for _, u := range d.DNS.Upstreams {
			server(u.Address, "This range holds a DNS upstream.")
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
				const message = "This range holds a management source address."
				switch a.Kind {
				case "host":
					one(a.Address, message)
				case "network":
					if p, e := netip.ParsePrefix(a.Prefix); e == nil {
						span(p, message)
						out[len(out)-1].whole = true
					}
				case "range":
					first, e1 := netip.ParseAddr(a.First)
					last, e2 := netip.ParseAddr(a.Last)
					if e1 == nil && e2 == nil && first.Is4() == last.Is4() {
						out = append(out, claim{first.Unmap(), last.Unmap(), message, false, true})
					}
				}
			}
		}
	}
	return out
}

// lastOf is the last address of a prefix.
func lastOf(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := max(p.Bits(), 0); i < len(b)*8; i++ {
		b[i/8] |= 1 << (7 - i%8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// validateProxy checks one proxy ARP entry against its segment, the firewall's static addresses (own), the hosts the
// declaration names, and the entries before it; it says whether v is valid.
func validateProxy(d *declaration, v virtualAddress, p string, own []netip.Prefix, taken []claim, before []virtualAddress, bad func(string, string)) bool {
	seg := slices.IndexFunc(d.Segments, func(s segment) bool { return s.ID == v.Segment })
	if seg < 0 {
		bad(p+"/segment", "Expected an existing segment.")
	}
	x, ok := vipPrefix(v)
	a := x.Addr()
	claimed := slices.IndexFunc(taken, func(c claim) bool { return c.overlaps(a, lastOf(x)) })
	overlap := slices.IndexFunc(before, func(o virtualAddress) bool { y, _ := vipPrefix(o); return y.Overlaps(x) })
	switch {
	case ok && a.Is6() && !a.Is4In6():
		bad(p+"/address", "Expected an IPv4 address; for an IPv6 host on the uplink's prefix, use a proxyNdp entry.")
	case !ok || !a.Is4() || !blockShape(x, 24):
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

// ndpClaims are the claims a proxy NDP entry may not take, in its words: its route would take the gateway's traffic,
// or a delegated prefix's. Other named hosts are the inside host itself, which the route leads to.
var ndpClaims = map[string]string{
	gatewayClaim: "This address is a segment's gateway.",
	poolClaim:    "This address is in a DHCPv6 prefix delegation pool.",
}

// validateProxyNDP checks the proxy NDP entry at index i against its two segments, the firewall's static addresses
// (own), the other virtual addresses, and the gateways and DHCPv6 delegation pools the declaration names (taken).
func validateProxyNDP(d *declaration, i int, p string, own []netip.Prefix, taken []claim, bad func(string, string)) {
	v := d.VirtualAddresses[i]
	byID := func(id string) int { return slices.IndexFunc(d.Segments, func(s segment) bool { return s.ID == id }) }
	seg, host := byID(v.Segment), byID(v.HostSegment)
	if seg < 0 {
		bad(p+"/segment", "Expected an existing segment.")
	}
	switch {
	case host < 0:
		bad(p+"/hostSegment", "Expected an existing segment.")
	case v.HostSegment == v.Segment:
		bad(p+"/hostSegment", "Expected a segment other than the one the firewall answers on.")
	}
	a, e := netip.ParseAddr(v.Address)
	// Before i, every kind; after it, all but proxy NDP entries, which refuse the overlap themselves.
	other := -1
	for j, o := range d.VirtualAddresses {
		if j != i && (j < i || o.Kind != "proxyNdp") && heldContains(o, a) {
			other = j
			break
		}
	}
	claimed := slices.IndexFunc(taken, func(c claim) bool { return ndpClaims[c.message] != "" && c.overlaps(a, a) })
	v6 := func(s int) []netip.Prefix {
		return slices.DeleteFunc(staticPrefixes(d.Segments[s]), func(x netip.Prefix) bool { return !x.Addr().Is6() })
	}
	switch {
	// The kernel's proxy table holds single addresses, and the address is a host's on the uplink's prefix.
	case e != nil || a.Zone() != "" || !a.Is6() || a.Is4In6() || !a.IsGlobalUnicast() || a.IsPrivate():
		bad(p+"/address", "Expected one global unicast IPv6 address, without a length.")
	case seg >= 0 && d.Segments[seg].Addressing.IPv6.Mode == "static" && !slices.ContainsFunc(v6(seg), func(x netip.Prefix) bool { return x.Contains(a) }):
		bad(p+"/address", "Expected an address on one of the segment's static IPv6 subnets.")
	case slices.ContainsFunc(own, func(x netip.Prefix) bool { return x.Addr() == a }):
		bad(p+"/address", "This address is one of the firewall's own addresses.")
	case other >= 0:
		bad(p+"/address", fmt.Sprintf("This address overlaps virtual address %q.", d.VirtualAddresses[other].Name))
	case host >= 0 && slices.ContainsFunc(v6(host), func(x netip.Prefix) bool { return x.Contains(a) }):
		bad(p+"/address", "This address is on the host segment's subnet, where a host already has a route.")
	case claimed >= 0:
		bad(p+"/address", ndpClaims[taken[claimed].message])
	}
}

// proxied are the valid-looking proxy ARP entries' prefixes: no host holds an address in them.
func proxied(d *declaration) []netip.Prefix {
	var out []netip.Prefix
	for _, v := range d.VirtualAddresses {
		if x, ok := vipPrefix(v); ok && v.proxied() {
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

// vipStillTargeted refuses an operation that deletes a held virtual address, or changes its address, while a port
// forward targets it or an address inside it, and no held virtual address of d still holds the target: the forward
// would silently become one to an inside host nobody holds.
func vipStillTargeted(old, d *declaration, input Changes) *Fault {
	for _, v := range old.VirtualAddresses {
		if !v.held() {
			continue
		}
		i := slices.IndexFunc(d.NAT.PortForwards, func(f portForward) bool {
			if f.Target == nil {
				return false
			}
			a, e := netip.ParseAddr(f.Target.Address)
			return e == nil && heldContains(v, a) && !holdsVirtual(d, a)
		})
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

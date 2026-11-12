package config

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// dnsLabel is one name label: what a static lease's hostname may be and what a lease name is cut to.
var dnsLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// servedPrefix is a segment's first static IPv4 address with its prefix; ok is false for any other segment.
func servedPrefix(d *declaration, id string) (netip.Prefix, bool) {
	all := ownAddresses(d, id)
	if len(all) == 0 {
		return netip.Prefix{}, false
	}
	return all[0], true
}

// ownAddresses are a static segment's IPv4 addresses, none of which DHCP may hand out.
func ownAddresses(d *declaration, id string) []netip.Prefix {
	i := slices.IndexFunc(d.Segments, func(s segment) bool { return s.ID == id })
	if i < 0 || d.Segments[i].Addressing.IPv4.Mode != "static" {
		return nil
	}
	var out []netip.Prefix
	for _, raw := range d.Segments[i].Addressing.IPv4.Addresses {
		if p, e := netip.ParsePrefix(raw); e == nil && p.Addr().Is4() {
			out = append(out, p)
		}
	}
	return out
}

// validateServices checks the DHCP and DNS sections against each other and the topology.
func validateServices(d *declaration) []FieldError {
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{path, msg})
		}
	}
	served := map[string]bool{}
	seen := map[string]bool{}
	// Static lease names share the local domain, so one name names one device across every segment.
	names := map[string]bool{}
	for i, e := range d.DHCP {
		p := fmt.Sprintf("/dhcp/%d", i)
		if seen[e.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		if invalidText(e.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if seen["name:"+e.Name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		seen[e.ID], seen["name:"+e.Name] = true, true
		own, ok := servedPrefix(d, e.Segment)
		switch {
		case !ok:
			bad(p+"/segment", "Expected an existing segment with a static IPv4 address.")
			continue
		case served[e.Segment]:
			bad(p+"/segment", "This segment is already served by another DHCP entry.")
		}
		served[e.Segment] = true
		subnet := own.Masked()
		mine := ownAddresses(d, e.Segment)
		holdsOwn := func(first, last netip.Addr) bool {
			return slices.ContainsFunc(mine, func(p netip.Prefix) bool { return !p.Addr().Less(first) && !last.Less(p.Addr()) })
		}
		// The subnet's own network and broadcast addresses are never a host's.
		edge := func(a netip.Addr) bool {
			b, n := a.As4(), subnet.Addr().As4()
			host := binary.BigEndian.Uint32(b[:]) - binary.BigEndian.Uint32(n[:])
			return own.Bits() <= 30 && (host == 0 || host == 1<<(32-own.Bits())-1)
		}
		if e.LeaseTime != 0 && (e.LeaseTime < 300 || e.LeaseTime > 604800) {
			bad(p+"/leaseTime", "Expected 300 to 604800 seconds.")
		}
		var taken [][2]netip.Addr
		for j, r := range e.Ranges {
			rp := fmt.Sprintf("%s/ranges/%d", p, j)
			first, e1 := netip.ParseAddr(r.First)
			last, e2 := netip.ParseAddr(r.Last)
			switch {
			case e1 != nil || e2 != nil || !first.Is4() || !last.Is4():
				bad(rp, "Expected two IPv4 addresses.")
			case last.Less(first):
				bad(rp, "The first address comes after the last.")
			case !subnet.Contains(first) || !subnet.Contains(last):
				bad(rp, "The range must lie inside the segment's subnet.")
			case holdsOwn(first, last):
				bad(rp, "The range holds one of the segment's own addresses.")
			case edge(first) || edge(last):
				bad(rp, "The range holds the subnet's network or broadcast address.")
			case slices.ContainsFunc(taken, func(t [2]netip.Addr) bool { return !last.Less(t[0]) && !t[1].Less(first) }):
				bad(rp, "The range overlaps another range.")
			default:
				taken = append(taken, [2]netip.Addr{first, last})
			}
		}
		for j, raw := range e.DNSServers {
			if a, err := netip.ParseAddr(raw); err != nil || !a.Is4() {
				bad(fmt.Sprintf("%s/dnsServers/%d", p, j), "Expected an IPv4 address.")
			}
		}
		macs, addresses := map[string]bool{}, map[netip.Addr]bool{}
		for j, l := range e.StaticLeases {
			lp := fmt.Sprintf("%s/staticLeases/%d", p, j)
			if hw, err := net.ParseMAC(l.MAC); err != nil || len(hw) != 6 {
				bad(lp+"/mac", "Expected a MAC address.")
			} else if macs[hw.String()] {
				bad(lp+"/mac", "This MAC address already has a static lease.")
			} else {
				macs[hw.String()] = true
				e.StaticLeases[j].MAC = hw.String()
			}
			a, err := netip.ParseAddr(l.Address)
			switch {
			case err != nil || !a.Is4() || !subnet.Contains(a):
				bad(lp+"/address", "Expected an IPv4 address inside the segment's subnet.")
			case holdsOwn(a, a):
				bad(lp+"/address", "This is one of the segment's own addresses.")
			case edge(a):
				bad(lp+"/address", "This is the subnet's network or broadcast address.")
			case addresses[a]:
				bad(lp+"/address", "This address already has a static lease.")
			default:
				addresses[a] = true
			}
			if l.Hostname != "" && !dnsLabel.MatchString(l.Hostname) {
				bad(lp+"/hostname", "Expected one name label: letters, digits and inner hyphens.")
			} else if name := strings.ToLower(l.Hostname); name != "" && names[name] {
				bad(lp+"/hostname", "Another static lease already has this name.")
			} else {
				names[name] = true
			}
		}
	}

	s := d.DNS
	if s == nil {
		return out
	}
	// The host's servers live in one place: the service's upstreams.
	for i, seg := range d.Segments {
		if seg.Addressing.DNS != nil {
			bad(fmt.Sprintf("/segments/%d/addressing/dns", i), "The DNS service is declared: move these servers to its upstreams.")
		}
	}
	switch {
	case s.Mode == "forward" && len(s.Upstreams) == 0:
		bad("/dns/upstreams", "Forwarding needs at least one upstream.")
	case s.Mode == "recursive" && len(s.Upstreams) > 0:
		bad("/dns/upstreams", "A recursive resolver asks no upstream.")
	}
	for j, u := range s.Upstreams {
		up := fmt.Sprintf("/dns/upstreams/%d", j)
		a, err := netip.ParseAddr(u.Address)
		if err != nil {
			var ap netip.AddrPort
			ap, err = netip.ParseAddrPort(u.Address)
			a = ap.Addr()
		}
		// A zone could carry any text into the rendered file, and an upstream needs none.
		if err != nil || a.Zone() != "" {
			bad(up+"/address", "Expected an IP address, optionally with a port.")
		}
		if u.Protocol == "dot" && !searchDomain.MatchString(u.TLSName) {
			bad(up+"/tlsName", "DNS over TLS needs the name the upstream's certificate carries.")
		}
		// Encryption is set for the whole forward zone, not per upstream.
		if j > 0 && u.Protocol != s.Upstreams[0].Protocol {
			bad("/dns/upstreams", "All upstreams must use the same protocol.")
		}
	}
	for j, id := range s.Segments {
		if slices.Contains(s.Segments[:j], id) {
			bad(fmt.Sprintf("/dns/segments/%d", j), "This segment is listed twice.")
		} else if _, ok := servedPrefix(d, id); !ok {
			bad(fmt.Sprintf("/dns/segments/%d", j), "Expected an existing segment with a static IPv4 address.")
		}
	}
	// A name label (up to 63 characters) and a dot go in front of it, within DNS's 253.
	if s.LocalDomain != "" && (!searchDomain.MatchString(s.LocalDomain) || len(s.LocalDomain) > 253-64) {
		bad("/dns/localDomain", "Invalid domain name, or too long to hold a name in front of it (189 characters at most).")
	}
	for j, name := range s.SearchDomains {
		if !searchDomain.MatchString(name) {
			bad(fmt.Sprintf("/dns/searchDomains/%d", j), "Invalid search domain.")
		}
	}
	return out
}

// DHCPOffer is the range a takeover proposes for a static address: the upper half of its subnet without the
// address itself and the broadcast address, or the lower half when the address is in the upper one.
func DHCPOffer(address string) (map[string]string, bool) {
	p, err := netip.ParsePrefix(address)
	if err != nil || !p.Addr().Is4() || p.Bits() > 29 {
		return nil, false
	}
	base, own := p.Masked().Addr().As4(), p.Addr().As4()
	start, size := binary.BigEndian.Uint32(base[:]), uint32(1)<<(32-p.Bits())
	first, last := start+size/2, start+size-2
	if binary.BigEndian.Uint32(own[:]) >= first {
		first, last = start+1, start+size/2-1
	}
	addr := func(v uint32) string {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		return netip.AddrFrom4(b).String()
	}
	return map[string]string{"first": addr(first), "last": addr(last)}, true
}

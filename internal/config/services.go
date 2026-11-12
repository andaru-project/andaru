package config

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
)

// dnsLabel is one name label: what a static lease's hostname may be and what a lease name is cut to.
var dnsLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// servedPrefix is a segment's first static IPv4 address with its prefix; ok is false for any other segment.
func servedPrefix(d *declaration, id string) (netip.Prefix, bool) {
	i := slices.IndexFunc(d.Segments, func(s segment) bool { return s.ID == id })
	if i < 0 || d.Segments[i].Addressing.IPv4.Mode != "static" {
		return netip.Prefix{}, false
	}
	for _, raw := range d.Segments[i].Addressing.IPv4.Addresses {
		if p, e := netip.ParsePrefix(raw); e == nil && p.Addr().Is4() {
			return p, true
		}
	}
	return netip.Prefix{}, false
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
			case !own.Addr().Less(first) && !last.Less(own.Addr()):
				bad(rp, "The range holds the segment's own address.")
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
			case a == own.Addr():
				bad(lp+"/address", "This is the segment's own address.")
			case addresses[a]:
				bad(lp+"/address", "This address already has a static lease.")
			default:
				addresses[a] = true
			}
			if l.Hostname != "" && !dnsLabel.MatchString(l.Hostname) {
				bad(lp+"/hostname", "Expected one name label: letters, digits and inner hyphens.")
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
		if _, err := netip.ParseAddr(u.Address); err != nil {
			if _, err := netip.ParseAddrPort(u.Address); err != nil {
				bad(up+"/address", "Expected an IP address, optionally with a port.")
			}
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
		if _, ok := servedPrefix(d, id); !ok {
			bad(fmt.Sprintf("/dns/segments/%d", j), "Expected an existing segment with a static IPv4 address.")
		}
	}
	if s.LocalDomain != "" && !searchDomain.MatchString(s.LocalDomain) {
		bad("/dns/localDomain", "Invalid domain name.")
	}
	for j, name := range s.SearchDomains {
		if !searchDomain.MatchString(name) {
			bad(fmt.Sprintf("/dns/searchDomains/%d", j), "Invalid search domain.")
		}
	}
	return out
}

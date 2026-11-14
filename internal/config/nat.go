package config

import (
	"cmp"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// maxNATMapEntries caps the map entries shifted port ranges render, which grow with ports, zones and sources.
const maxNATMapEntries = 65536

// validateNAT checks the port forwards and 1:1 entries against the zones, address objects and the
// firewall's own static addresses, with pfSense's rules.
func validateNAT(d *declaration) []FieldError {
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{path, msg})
		}
	}
	zones := map[string]bool{}
	for _, z := range d.Zones {
		zones[z.ID] = true
	}
	addresses := map[string]address{}
	for _, a := range d.Objects.Addresses {
		addresses[a.ID] = a
	}
	// has says whether an address object can match the family six selects. Groups hold no groups (the
	// policy validator refuses them, but this runs regardless), so one level is followed and never more.
	var has func(id string, six, nested bool) bool
	has = func(id string, six, nested bool) bool {
		a, ok := addresses[id]
		var v netip.Addr
		var e error
		switch {
		case !ok:
			return false
		case a.Kind == "group":
			return !nested && slices.ContainsFunc(a.Members, func(m string) bool { return has(m, six, true) })
		case a.Kind == "network":
			var p netip.Prefix
			p, e = netip.ParsePrefix(a.Prefix)
			v = p.Addr()
		case a.Kind == "range":
			v, e = netip.ParseAddr(a.First)
		default:
			v, e = netip.ParseAddr(a.Address)
		}
		return e == nil && (six && v.Is6() && !v.Is4In6() || !six && v.Is4())
	}
	refFamily := func(path, id string, six bool) {
		if _, ok := addresses[id]; !ok {
			bad(path, "Expected an existing address.")
		} else if six && !has(id, true, false) {
			bad(path, "Expected an address with IPv6 addresses for an IPv6 port forward.")
		} else if !six && !has(id, false, false) {
			bad(path, "Expected an address with IPv4 addresses; inbound NAT is IPv4 only.")
		}
	}
	ref := func(path, id string) { refFamily(path, id, false) }
	unique := func(prefix string, i int, v identity, seen map[string]bool) {
		if seen[v.ID] {
			bad(fmt.Sprintf("%s/%d/id", prefix, i), "Duplicate object identity.")
		}
		if invalidText(v.Name, true) {
			bad(fmt.Sprintf("%s/%d/name", prefix, i), "Control or format characters are not allowed.")
		}
		if seen["name:"+v.Name] {
			bad(fmt.Sprintf("%s/%d/name", prefix, i), "Name already exists in this collection.")
		}
		seen[v.ID], seen["name:"+v.Name] = true, true
	}

	type span struct{ first, last int }
	spans := make([]span, len(d.NAT.PortForwards))
	budget := maxNATMapEntries
	seen := map[string]bool{}
	for i, f := range d.NAT.PortForwards {
		p := fmt.Sprintf("/nat/portForwards/%d", i)
		unique("/nat/portForwards", i, f.identity, seen)
		if len(f.IngressZones) == 0 {
			bad(p+"/ingressZones", "Expected at least one zone.")
		}
		for j, z := range f.IngressZones {
			if !zones[z] {
				bad(fmt.Sprintf("%s/ingressZones/%d", p, j), "Expected an existing zone.")
			}
		}
		six := f.Family == "ipv6"
		for j, a := range f.From {
			refFamily(fmt.Sprintf("%s/from/%d", p, j), a, six)
		}
		if f.To.Address != "" {
			refFamily(p+"/to/address", f.To.Address, six)
		}
		if six && f.Reflection {
			bad(p+"/reflection", "An IPv6 port forward reaches the firewall itself from every zone it names, so it has no reflection.")
		}
		first, last, ok := portSpan(f.Ports)
		if !ok {
			bad(p+"/ports", "Expected a port or a first-last range within 1-65535.")
		}
		spans[i] = span{first, last}
		switch {
		case f.NoRedirect && (f.Target != nil || f.TargetPort != nil):
			bad(p+"/target", "An exclusion translates nothing, so it has no target.")
		case !f.NoRedirect && f.Target == nil:
			bad(p+"/target", "Expected a target.")
		case f.Target != nil && !f.Target.Self && six:
			a, e := netip.ParseAddr(f.Target.Address)
			switch {
			case e != nil || !a.Is6() || a.Is4In6():
				bad(p+"/target/address", "Expected an IPv6 address the firewall holds.")
			case disabledHolder(d, a) != "":
				bad(p+"/target/address", fmt.Sprintf("This address is on link %q, which is disabled.", disabledHolder(d, a)))
			case !firewallHolds(d, a):
				// The firewall's addresses from DHCPv6 or a delegated prefix are not known here; self reaches them.
				bad(p+"/target/address", "Expected a static IPv6 address of the firewall or an IPv6 virtual address; for its addresses from DHCPv6 or a delegated prefix, use target self. IPv6 port forwards to inside hosts are not offered (#43).")
			}
		case f.Target != nil && !f.Target.Self:
			a, e := netip.ParseAddr(f.Target.Address)
			switch {
			case e != nil || !a.Is4():
				bad(p+"/target/address", "Expected an IPv4 address; inbound NAT is IPv4 only.")
			case a.IsLoopback():
				// Linux drops a connection translated to 127.0.0.0/8 from another interface unless route_localnet is set.
				bad(p+"/target/address", "Loopback addresses cannot be targets. Use a virtual address instead; proxy ARP and CARP are not supported yet.")
			case !validUnicast(a):
				bad(p+"/target/address", "Expected a unicast address.")
			}
		}
		if f.TargetPort != nil && ok && *f.TargetPort+last-first > 65535 {
			bad(p+"/targetPort", "The shifted range would end past port 65535.")
		}
		// A shifted range renders one map entry per port, zone and source; the whole configuration has a budget.
		if f.Enabled && f.TargetPort != nil && ok && *f.TargetPort != first && last != first {
			sources := 1
			if !f.FromNegated {
				sources = max(1, len(f.From))
			}
			lines := len(f.IngressZones) * sources
			if f.Reflection {
				lines += sources // the reflection copy, once for internal clients
			}
			if budget -= (last - first + 1) * lines; budget < 0 {
				bad(p+"/targetPort", fmt.Sprintf("Shifted port ranges render more than %d map entries in all. Keep the ports, or shift fewer.", maxNATMapEntries))
				budget = maxNATMapEntries // report once per crossing forward, not for every one after it
			}
		}
	}
	// pfSense refuses two forwards that would catch the same connections: same source, destination, an
	// ingress zone, a protocol and overlapping ports. Forwards differing in any of those are ordered. Validation
	// runs on every read, so the forwards are bucketed by those keys and each bucket swept in port order.
	type entry struct{ index, first, last int }
	buckets := map[string][]entry{}
	for i, f := range d.NAT.PortForwards {
		if !f.Enabled || f.NoRedirect || spans[i].first == 0 {
			continue
		}
		from := strings.Join(slices.Sorted(slices.Values(f.From)), ",")
		protocols := []string{f.Protocol}
		if f.Protocol == "tcp-udp" {
			protocols = []string{"tcp", "udp"}
		}
		for _, zone := range slices.Compact(slices.Sorted(slices.Values(f.IngressZones))) {
			for _, protocol := range protocols {
				key := fmt.Sprintf("%s|%t|%s|%t|%s|%s|%s", cmp.Or(f.Family, "ipv4"), f.FromNegated, from, f.To.Self, f.To.Address, zone, protocol)
				buckets[key] = append(buckets[key], entry{i, spans[i].first, spans[i].last})
			}
		}
	}
	overlapping := map[int]int{}
	for _, list := range buckets {
		slices.SortFunc(list, func(a, b entry) int { return a.first - b.first })
		reach := list[0]
		for _, e := range list[1:] {
			if e.first <= reach.last {
				later, other := max(e.index, reach.index), min(e.index, reach.index)
				if prev, seen := overlapping[later]; !seen || other < prev {
					overlapping[later] = other
				}
			}
			if e.last > reach.last {
				reach = e
			}
		}
	}
	for _, j := range slices.Sorted(maps.Keys(overlapping)) {
		bad(fmt.Sprintf("/nat/portForwards/%d/ports", j), fmt.Sprintf("These ports overlap port forward %q for the same connections.", d.NAT.PortForwards[overlapping[j]].Name))
	}

	// own holds the firewall's static IPv4 addresses; primary is each segment's first, which the firewall's own
	// traffic and the outbound masquerade use. 1:1 may take over an extra address, pfSense's IP alias, but never these.
	var own, primary []netip.Addr
	for _, s := range d.Segments {
		if s.Addressing.IPv4.Mode != "static" {
			continue
		}
		for j, raw := range slices.Sorted(slices.Values(s.Addressing.IPv4.Addresses)) {
			if p, e := netip.ParsePrefix(raw); e == nil {
				own = append(own, p.Addr())
				if j == 0 {
					primary = append(primary, p.Addr())
				}
			}
		}
	}
	// A virtual address is the firewall's own, held for its services: 1:1 NAT takes it over in neither direction.
	var virtual []netip.Addr
	for _, v := range d.VirtualAddresses {
		if a, e := netip.ParseAddr(v.Address); e == nil && a.Is4() {
			own, virtual = append(own, a), append(virtual, a)
		}
	}
	holdsOwn := func(p netip.Prefix) bool { return slices.ContainsFunc(own, p.Contains) }
	holdsVirtual := func(p netip.Prefix) bool { return slices.ContainsFunc(virtual, p.Contains) }
	holdsPrimary := func(p netip.Prefix) bool { return slices.ContainsFunc(primary, p.Contains) }
	externals := make([]netip.Prefix, len(d.NAT.OneToOne))
	seen = map[string]bool{}
	for i, o := range d.NAT.OneToOne {
		p := fmt.Sprintf("/nat/oneToOne/%d", i)
		unique("/nat/oneToOne", i, o.identity, seen)
		if !zones[o.Zone] {
			bad(p+"/zone", "Expected an existing zone.")
		}
		for j, a := range o.Destination {
			ref(fmt.Sprintf("%s/destination/%d", p, j), a)
		}
		internal, ok := prefix4(o.Internal)
		if !ok {
			bad(p+"/internal", "Expected an IPv4 address or a prefix with its host bits zero.")
		} else if holdsOwn(internal) {
			bad(p+"/internal", "This range holds one of the firewall's own addresses.")
		}
		if o.NoBinat {
			if o.External != "" {
				bad(p+"/external", "An exclusion maps nothing, so it has no external address.")
			}
			// It is matched on its internal hosts and zone alone; anything else would be silently ignored.
			if len(o.Destination) > 0 {
				bad(p+"/destination", "An exclusion applies to every peer; it has no destination.")
			}
			if o.Reflection {
				bad(p+"/reflection", "An exclusion translates nothing, so it has no reflection.")
			}
			continue
		}
		external, extOK := prefix4(o.External)
		switch {
		case !extOK:
			bad(p+"/external", "Expected an IPv4 address or a prefix with its host bits zero.")
		case holdsVirtual(external):
			bad(p+"/external", "This range holds a virtual address, which belongs to a service on the firewall.")
		case holdsPrimary(external):
			bad(p+"/external", "This range holds a segment's first address, which the firewall's own traffic uses and 1:1 NAT would take over.")
		case ok && external.Bits() != internal.Bits():
			bad(p+"/internal", "Expected a prefix of the same length as the external one.")
		}
		if !extOK || !o.Enabled {
			continue
		}
		externals[i] = external
		for k, other := range externals[:i] {
			if other.IsValid() && other.Overlaps(external) {
				bad(p+"/external", fmt.Sprintf("This range overlaps 1:1 entry %q.", d.NAT.OneToOne[k].Name))
				break
			}
		}
	}
	return out
}

// portSpan parses "port" or "first-last" within 1-65535.
func portSpan(s string) (int, int, bool) {
	lo, hi, isRange := strings.Cut(s, "-")
	if !isRange {
		hi = lo
	}
	a, e1 := strconv.Atoi(lo)
	b, e2 := strconv.Atoi(hi)
	if e1 != nil || e2 != nil || a < 1 || b > 65535 || a > b || strconv.Itoa(a) != lo || strconv.Itoa(b) != hi {
		return 0, 0, false
	}
	return a, b, true
}

// prefix4 parses an IPv4 address (as a /32) or an IPv4 prefix with its host bits zero.
func prefix4(s string) (netip.Prefix, bool) {
	if a, e := netip.ParseAddr(s); e == nil {
		return netip.PrefixFrom(a, 32), a.Is4()
	}
	p, e := netip.ParsePrefix(s)
	return p, e == nil && p.Addr().Is4() && p == p.Masked()
}

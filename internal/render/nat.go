package render

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// ownStatic is the firewall's IPv4 addresses, declared and learned: a translation to one of them is local delivery.
func (r *renderer) ownStatic() []netip.Addr {
	var out []netip.Addr
	for _, l := range r.in.Links {
		for _, a := range l.Addresses {
			if v, e := netip.ParseAddr(a.Address); e == nil && a.Family == "ipv4" {
				out = append(out, v)
			}
		}
	}
	// Declared, not learned: the device may still hold an address a change just removed.
	for _, v := range r.d.VirtualAddresses {
		if a, e := netip.ParseAddr(v.Address); e == nil && a.Is4() {
			out = append(out, a)
		}
	}
	for _, s := range r.d.Segments {
		if s.Addressing.IPv4.Mode != "static" {
			continue
		}
		for _, raw := range s.Addressing.IPv4.Addresses {
			if p, e := netip.ParsePrefix(raw); e == nil {
				out = append(out, p.Addr())
			}
		}
	}
	return out
}

// portMatch is the protocol and port match of a forward, for its original or translated ports.
func portMatch(protocol string, first, last int) string {
	ports := strconv.Itoa(first)
	if last != first {
		ports += "-" + strconv.Itoa(last)
	}
	if protocol == "tcp-udp" {
		return "meta l4proto { tcp, udp } th dport " + ports
	}
	return protocol + " dport " + ports
}

// portForwards renders the forwards in order: nat prerouting lines, and each one's associated filter rule for
// the forward chain (an inside host) or the input chain (the firewall itself).
func (f *filter) portForwards() (prerouting, forward, input []string) {
	own := f.r.ownStatic()
	for i, pf := range f.r.d.NAT.PortForwards {
		if !pf.Enabled {
			continue
		}
		first, last := spanOf(pf.Ports)
		// An IPv6 forward reaches only the firewall itself; everything else matches IPv4.
		six := pf.Family == "ipv6"
		ip, fam, nfproto := "ip", "_4", "meta nfproto ipv4"
		if six {
			ip, fam, nfproto = "ip6", "_6", "meta nfproto ipv6"
		}
		// Alternatives on one side need one line each; excluded sources all go on every line.
		froms := []string{""}
		if len(pf.From) > 0 {
			if pf.FromNegated {
				var parts []string
				for _, id := range pf.From {
					parts = append(parts, ip+" saddr != @"+setName(id)+fam)
				}
				froms = []string{strings.Join(parts, " ")}
			} else {
				froms = froms[:0]
				for _, id := range pf.From {
					froms = append(froms, ip+" saddr @"+setName(id)+fam)
				}
			}
		}
		// fib matches either family: the guard keeps an IPv4 forward to self off the firewall's IPv6 addresses.
		to := nfproto + " fib daddr . iif type local"
		if !pf.To.Self {
			to = ip + " daddr @" + setName(pf.To.Address) + fam
		}
		// ct status dnat says a connection was translated, not by whom: each forward marks what it translates in the
		// low 16 bits of the connection mark, and its rule admits that mark only. The high bits are left to others.
		mark := fmt.Sprintf("0x%08x", i+1)
		var action string
		tfirst := first
		if pf.TargetPort != nil {
			tfirst = *pf.TargetPort
		}
		tlast := tfirst + last - first
		local := pf.Target != nil && (six || pf.Target.Self || slices.ContainsFunc(own, func(a netip.Addr) bool { return a.String() == pf.Target.Address }))
		key := map[bool]string{false: pf.Protocol, true: "th"}[pf.Protocol == "tcp-udp"]
		switch {
		case pf.NoRedirect:
			action = "accept"
		case pf.Target.Self:
			action = "redirect"
			if pf.TargetPort != nil && tfirst != first {
				action = "redirect to :" + strconv.Itoa(tfirst)
				if last != first {
					action = "redirect to " + key + " dport map { " + portMap(first, last, tfirst, "") + " }"
				}
			}
		default:
			action = "dnat " + ip + " to " + pf.Target.Address
			if six && (pf.TargetPort != nil && last == first || last != first && tfirst != first) {
				// IPv6 addresses take brackets before a port.
				action = "dnat ip6 to [" + pf.Target.Address + "]"
			}
			switch {
			case last != first && tfirst != first:
				// nft has no port shift: a range would be a pool, so each port maps to its own.
				action = "dnat " + ip + " addr . port to " + key + " dport map { " + portMap(first, last, tfirst, pf.Target.Address+" . ") + " }"
			case pf.TargetPort != nil && last == first:
				action += ":" + strconv.Itoa(tfirst)
			}
		}
		if !pf.NoRedirect && pf.FilterRule == "auto" {
			action = "ct mark set ct mark & 0xffff0000 | " + mark + " " + action
		}
		if pf.Log && !pf.NoRedirect {
			action = strings.Replace(action, " dnat ", fmt.Sprintf(" log prefix %q dnat ", "andaru "+pf.ID+" "), 1)
			action = strings.Replace(action, " redirect", fmt.Sprintf(" log prefix %q redirect", "andaru "+pf.ID+" "), 1)
			if !strings.Contains(action, "log prefix") {
				action = fmt.Sprintf("log prefix %q ", "andaru "+pf.ID+" ") + action
			}
		}
		comment := fmt.Sprintf("comment %q", pf.ID)
		for _, zone := range pf.IngressZones {
			for _, from := range froms {
				parts := slices.DeleteFunc([]string{"iifname @" + setName(zone), from, to, portMatch(pf.Protocol, first, last), action, comment}, func(s string) bool { return s == "" })
				prerouting = append(prerouting, strings.Join(parts, " "))
				if pf.NoRedirect || pf.FilterRule != "auto" {
					continue
				}
				// Bound to this forward: only connections it translated (its mark), from its sources, to its target.
				// The translated address names the family; a local target has none, so the rule says it.
				daddr := nfproto
				if !local {
					daddr = "ip daddr " + pf.Target.Address
				}
				rule := slices.DeleteFunc([]string{"iifname @" + setName(zone), from, "ct mark & 0x0000ffff == " + mark, daddr, portMatch(pf.Protocol, tfirst, tlast), "accept", comment}, func(s string) bool { return s == "" })
				if local {
					input = append(input, strings.Join(rule, " "))
				} else {
					forward = append(forward, strings.Join(rule, " "))
				}
			}
		}
		if pf.Reflection && !pf.NoRedirect && !six {
			// Pure NAT reflection: internal clients asking the outside address reach the target too. With self, any
			// firewall address but the internal segments' own, so asking the firewall's LAN address is not redirected.
			f.reflection = true
			if pf.To.Self {
				to = "fib daddr type local ip daddr != @internal_4"
			} else if len(f.virtual4()) > 0 {
				// A virtual address in the destination belongs to its service, as with self.
				to += " ip daddr != @virtual_4"
			}
			bare := strings.Replace(action, fmt.Sprintf("log prefix %q ", "andaru "+pf.ID+" "), "", 1)
			for _, from := range froms {
				parts := slices.DeleteFunc([]string{"iifname @internal", from, to, portMatch(pf.Protocol, first, last), bare, comment}, func(s string) bool { return s == "" })
				prerouting = append(prerouting, strings.Join(parts, " "))
			}
		}
	}
	return
}

// portMap lists each original port with its translated one, prefixed (the target address and " . ") for dnat.
func portMap(first, last, tfirst int, prefix string) string {
	var entries []string
	for p := first; p <= last; p++ {
		entries = append(entries, fmt.Sprintf("%d : %s%d", p, prefix, tfirst+p-first))
	}
	return strings.Join(entries, ", ")
}

func spanOf(s string) (int, int) {
	lo, hi, isRange := strings.Cut(s, "-")
	if !isRange {
		hi = lo
	}
	a, _ := strconv.Atoi(lo)
	b, _ := strconv.Atoi(hi)
	return a, b
}

// internalSegments are the usable segments that are not uplinks, with their interface and static IPv4 prefixes:
// where reflected clients come from and where their replies must come back through the firewall.
func (r *renderer) internalSegments() (names []string, prefixes []netip.Prefix, owners []string) {
	for _, s := range r.d.Segments {
		name, ok := r.name[s.Link]
		if !ok || !r.usable[s.Link] || r.uplink(s) {
			continue
		}
		names = append(names, name)
		if s.Addressing.IPv4.Mode != "static" {
			continue
		}
		for _, raw := range s.Addressing.IPv4.Addresses {
			if p, e := netip.ParsePrefix(raw); e == nil && p.Addr().Is4() {
				prefixes, owners = append(prefixes, p), append(owners, name)
			}
		}
	}
	return
}

// uplink is a segment outbound NAT leaves through: a static IPv4 gateway, DHCPv4 or a PPPoE link.
func (r *renderer) uplink(s segment) bool {
	return s.Addressing.IPv4.Gateway != "" || s.Addressing.IPv4.Mode == "dhcp" || r.d.Links[r.index[s.Link]].Kind == "pppoe"
}

func v4int(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func v4addr(n uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

// mapping renders a 1:1 map entry and the keyword its dnat or snat needs: an address alone, or the prefix form.
func mapping(from, to netip.Prefix) (keyword, entry string) {
	if from.Bits() == 32 {
		return "ip", from.Addr().String() + " : " + to.Addr().String()
	}
	return "ip prefix", from.String() + " : " + to.String()
}

func host32(p netip.Prefix) string { return strings.TrimSuffix(p.String(), "/32") }

// oneToOne renders the 1:1 entries in order: dnat in prerouting and snat in postrouting, both scoped to the entry's
// zone. An exclusion carves its hosts out of the entries after it, in both directions, so they fall through to the
// outbound masquerade.
func (f *filter) oneToOne() (prerouting, postrouting []string) {
	r := f.r
	join := func(parts ...string) string {
		return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " ")
	}
	not := func(field string, list []string) string {
		if len(list) == 0 {
			return ""
		}
		return field + " != { " + strings.Join(list, ", ") + " }"
	}
	excluded := map[string][]netip.Prefix{} // per zone: an exclusion carves hosts out of its own zone's entries
	for i, o := range r.d.NAT.OneToOne {
		if !o.Enabled {
			continue
		}
		internal := prefixOf(o.Internal)
		if o.NoBinat {
			excluded[o.Zone] = append(excluded[o.Zone], internal)
			continue
		}
		external := prefixOf(o.External)
		f.checkExternal(external, fmt.Sprintf("/nat/oneToOne/%d/external", i))
		var outside, inside []string
		whole := false
		for _, x := range excluded[o.Zone] {
			switch {
			case x.Bits() <= internal.Bits() && x.Contains(internal.Addr()):
				whole = true
			case internal.Overlaps(x):
				// The excluded hosts and the external addresses they would map to.
				inside = append(inside, host32(x))
				outside = append(outside, host32(netip.PrefixFrom(v4addr(v4int(external.Addr())+v4int(x.Addr())-v4int(internal.Addr())), x.Bits())))
			}
		}
		if whole {
			continue
		}
		comment := fmt.Sprintf("comment %q", o.ID)
		kw, in := mapping(external, internal)
		_, out := mapping(internal, external)
		// Each destination object is an alternative, one line each; none means any peer.
		sources, peers := []string{""}, []string{""}
		if len(o.Destination) > 0 {
			sources, peers = nil, nil
			for _, id := range o.Destination {
				sources = append(sources, "ip saddr @"+setName(id)+"_4")
				peers = append(peers, "ip daddr @"+setName(id)+"_4")
			}
		}
		for _, s := range sources {
			prerouting = append(prerouting, join("iifname @"+setName(o.Zone), s, not("ip daddr", outside), "dnat "+kw+" to ip daddr map { "+in+" }", comment))
		}
		if o.Reflection {
			f.reflection = true
			for _, s := range sources {
				prerouting = append(prerouting, join("iifname @internal", s, not("ip daddr", outside), "dnat "+kw+" to ip daddr map { "+in+" }", comment))
			}
		}
		for _, p := range peers {
			postrouting = append(postrouting, join("oifname @"+setName(o.Zone), p, not("ip saddr", inside), "snat "+kw+" to ip saddr map { "+out+" }", comment))
		}
	}
	return
}

func prefixOf(s string) netip.Prefix {
	if a, e := netip.ParseAddr(s); e == nil {
		return netip.PrefixFrom(a, 32)
	}
	p, _ := netip.ParsePrefix(s)
	return p
}

// checkExternal reports a 1:1 external range holding an address the firewall uses now, and one on an uplink
// subnet the firewall does not hold, which nothing would answer ARP for.
func (f *filter) checkExternal(external netip.Prefix, path string) {
	r := f.r
	// Declared static addresses were judged when saved; only one learned since, by DHCP or PPPoE, is new here.
	declared := map[netip.Addr]bool{}
	for _, s := range r.d.Segments {
		for _, raw := range s.Addressing.IPv4.Addresses {
			if p, e := netip.ParsePrefix(raw); e == nil {
				declared[p.Addr()] = true
			}
		}
	}
	for _, l := range r.in.Links {
		for _, a := range l.Addresses {
			if v, e := netip.ParseAddr(a.Address); e == nil && a.Family == "ipv4" && external.Contains(v) && !declared[v] {
				r.find("error", "ONE_TO_ONE_OWN_ADDRESS", path, "This range holds "+v.String()+", an address the firewall uses now, whose traffic 1:1 NAT would take over.")
				return
			}
		}
	}
	for _, s := range r.d.Segments {
		if !r.uplink(s) || s.Addressing.IPv4.Mode != "static" {
			continue
		}
		held := false
		var subnet netip.Prefix
		for _, raw := range s.Addressing.IPv4.Addresses {
			p, e := netip.ParsePrefix(raw)
			if e != nil {
				continue
			}
			if p.Masked().Contains(external.Addr()) {
				subnet = p.Masked()
			}
			if external.Bits() == 32 && p.Addr() == external.Addr() {
				held = true
			}
		}
		if subnet.IsValid() && !held {
			r.find("warning", "PROXY_ARP_REQUIRED", path, "This address is on the "+subnet.String()+" uplink subnet but not assigned to the firewall, so nothing answers ARP for it. Add it to the segment's addresses; proxy ARP comes with virtual IPs (#37).")
		}
	}
}

// virtual4 are the IPv4 virtual addresses, which reflection never takes.
func (f *filter) virtual4() []string {
	var out []string
	for _, v := range f.r.d.VirtualAddresses {
		if a, e := netip.ParseAddr(v.Address); e == nil && a.Is4() {
			out = append(out, a.String())
		}
	}
	return out
}

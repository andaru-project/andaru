package render

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// ownStatic is the firewall's static IPv4 addresses: a translation to one of them is local delivery.
func (r *renderer) ownStatic() []netip.Addr {
	var out []netip.Addr
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
	for _, pf := range f.r.d.NAT.PortForwards {
		if !pf.Enabled {
			continue
		}
		first, last := spanOf(pf.Ports)
		// Alternatives on one side need one line each; excluded sources all go on every line.
		froms := []string{""}
		if len(pf.From) > 0 {
			if pf.FromNegated {
				var parts []string
				for _, id := range pf.From {
					parts = append(parts, "ip saddr != @"+setName(id)+"_4")
				}
				froms = []string{strings.Join(parts, " ")}
			} else {
				froms = froms[:0]
				for _, id := range pf.From {
					froms = append(froms, "ip saddr @"+setName(id)+"_4")
				}
			}
		}
		to, original := "fib daddr . iif type local", ""
		if !pf.To.Self {
			to = "ip daddr @" + setName(pf.To.Address) + "_4"
			original = "ct original ip daddr @" + setName(pf.To.Address) + "_4 "
		}
		var action string
		tfirst := first
		if pf.TargetPort != nil {
			tfirst = *pf.TargetPort
		}
		tlast := tfirst + last - first
		local := pf.Target != nil && (pf.Target.Self || slices.ContainsFunc(own, func(a netip.Addr) bool { return a.String() == pf.Target.Address }))
		key := map[bool]string{false: pf.Protocol, true: "th"}[pf.Protocol == "tcp-udp"]
		switch {
		case pf.NoRedirect:
			action = "accept"
		case pf.Target.Self:
			action = "redirect"
			if pf.TargetPort != nil {
				action = "redirect to :" + strconv.Itoa(tfirst)
				if last != first && tfirst != first {
					action = "redirect to " + key + " dport map { " + portMap(first, last, tfirst, "") + " }"
				}
			}
		default:
			action = "dnat ip to " + pf.Target.Address
			switch {
			case last != first && tfirst != first:
				// nft has no port shift: a range would be a pool, so each port maps to its own.
				action = "dnat ip addr . port to " + key + " dport map { " + portMap(first, last, tfirst, pf.Target.Address+" . ") + " }"
			case pf.TargetPort != nil && last == first:
				action += ":" + strconv.Itoa(tfirst)
			}
		}
		if pf.Log && !pf.NoRedirect {
			action = fmt.Sprintf("log prefix %q ", "andaru "+pf.ID+" ") + action
		}
		comment := fmt.Sprintf("comment %q", pf.ID)
		for _, zone := range pf.IngressZones {
			for _, from := range froms {
				parts := slices.DeleteFunc([]string{"iifname @" + setName(zone), from, to, portMatch(pf.Protocol, first, last), action, comment}, func(s string) bool { return s == "" })
				prerouting = append(prerouting, strings.Join(parts, " "))
				if pf.NoRedirect || pf.FilterRule != "auto" {
					continue
				}
				// Bound to this forward: only connections it translated, from its sources, to its target.
				ports := strconv.Itoa(first)
				if last != first {
					ports += "-" + strconv.Itoa(last)
				}
				daddr := ""
				if !local {
					daddr = "ip daddr " + pf.Target.Address
				}
				rule := slices.DeleteFunc([]string{"iifname @" + setName(zone), from, "ct status dnat", original + "ct original proto-dst " + ports, daddr, portMatch(pf.Protocol, tfirst, tlast), "accept", comment}, func(s string) bool { return s == "" })
				if local {
					input = append(input, strings.Join(rule, " "))
				} else {
					forward = append(forward, strings.Join(rule, " "))
				}
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

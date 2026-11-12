package config

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// maxRuleLines caps the nft lines one rule renders to.
const maxRuleLines = 4096

// ICMP types a service may name; nft knows them by these names.
var icmpTypes = map[string][]string{
	"icmp":   {"echo-request", "echo-reply", "destination-unreachable", "time-exceeded", "parameter-problem", "timestamp-request", "timestamp-reply"},
	"icmpv6": {"echo-request", "echo-reply", "destination-unreachable", "packet-too-big", "time-exceeded", "parameter-problem"},
}

// validatePolicy checks the filter sections against each other and the topology.
func validatePolicy(d *declaration) []FieldError {
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{path, msg})
		}
	}
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

	addresses := map[string]*address{}
	seen := map[string]bool{}
	for i := range d.Objects.Addresses {
		a := &d.Objects.Addresses[i]
		unique("/objects/addresses", i, a.identity, seen)
		addresses[a.ID] = a
	}
	// families says which address families an address object holds: 4, 6 or both for a group.
	families := map[string]map[int]bool{}
	familyOf := func(a netip.Addr) int {
		if a.Is4() {
			return 4
		}
		return 6
	}
	for i := range d.Objects.Addresses {
		a := &d.Objects.Addresses[i]
		p := fmt.Sprintf("/objects/addresses/%d", i)
		f := map[int]bool{}
		fields := map[string]bool{"address": a.Address != "", "prefix": a.Prefix != "", "first": a.First != "" || a.Last != "", "members": a.Members != nil}
		want := map[string]string{"host": "address", "network": "prefix", "range": "first", "group": "members"}[a.Kind]
		for field, set := range fields {
			if set && field != want {
				bad(p+"/"+field, "Not used by this kind of address.")
			}
		}
		switch a.Kind {
		case "host":
			v, e := netip.ParseAddr(a.Address)
			if e != nil || v.Zone() != "" || v.Is4In6() || !v.IsValid() {
				bad(p+"/address", "Expected one IPv4 or IPv6 address.")
			} else {
				f[familyOf(v)] = true
			}
		case "network":
			v, e := netip.ParsePrefix(a.Prefix)
			if e != nil || v.Addr().Is4In6() || v != v.Masked() {
				bad(p+"/prefix", "Expected a network prefix with its host bits zero.")
			} else {
				f[familyOf(v.Addr())] = true
			}
		case "range":
			first, e1 := netip.ParseAddr(a.First)
			last, e2 := netip.ParseAddr(a.Last)
			switch {
			case e1 != nil || first.Zone() != "":
				bad(p+"/first", "Expected an IPv4 or IPv6 address.")
			case e2 != nil || last.Zone() != "" || first.Is4() != last.Is4() || last.Less(first):
				bad(p+"/last", "Expected an address of the same family, not before the first.")
			default:
				f[familyOf(first)] = true
			}
		case "group":
			if len(a.Members) == 0 {
				bad(p+"/members", "A group needs at least one member.")
			}
			for j, m := range a.Members {
				member, ok := addresses[m]
				if !ok || member.Kind == "group" {
					bad(fmt.Sprintf("%s/members/%d", p, j), "Members must be existing addresses that are not groups.")
				}
			}
		}
		families[a.ID] = f
	}
	for _, a := range d.Objects.Addresses {
		if a.Kind == "group" {
			for _, m := range a.Members {
				for family := range families[m] {
					families[a.ID][family] = true
				}
			}
		}
	}

	services := map[string]*service{}
	seen = map[string]bool{}
	for i := range d.Objects.Services {
		s := &d.Objects.Services[i]
		unique("/objects/services", i, s.identity, seen)
		services[s.ID] = s
	}
	for i := range d.Objects.Services {
		s := &d.Objects.Services[i]
		p := fmt.Sprintf("/objects/services/%d", i)
		ports, types, members := s.Protocol == "tcp" || s.Protocol == "udp", icmpTypes[s.Protocol] != nil, s.Protocol == "group"
		if !ports && s.Ports != nil {
			bad(p+"/ports", "Only tcp and udp services have ports.")
		}
		if !types && s.Types != nil {
			bad(p+"/types", "Only icmp and icmpv6 services have types.")
		}
		if !members && s.Members != nil {
			bad(p+"/members", "Only groups have members.")
		}
		if ports && len(s.Ports) == 0 {
			bad(p+"/ports", "A tcp or udp service needs at least one port.")
		}
		for j, raw := range s.Ports {
			lo, hi, isRange := strings.Cut(raw, "-")
			if !isRange {
				hi = lo
			}
			a, e1 := strconv.Atoi(lo)
			b, e2 := strconv.Atoi(hi)
			if e1 != nil || e2 != nil || a < 1 || b > 65535 || a > b || strconv.Itoa(a) != lo || strconv.Itoa(b) != hi {
				bad(fmt.Sprintf("%s/ports/%d", p, j), "Expected a port or a first-last range within 1-65535.")
			}
		}
		for j, t := range s.Types {
			if !slices.Contains(icmpTypes[s.Protocol], t) {
				bad(fmt.Sprintf("%s/types/%d", p, j), "Unknown "+s.Protocol+" type.")
			}
		}
		if members && len(s.Members) == 0 {
			bad(p+"/members", "A group needs at least one member.")
		}
		for j, m := range s.Members {
			if member, ok := services[m]; !ok || member.Protocol == "group" {
				bad(fmt.Sprintf("%s/members/%d", p, j), "Members must be existing services that are not groups.")
			}
		}
	}

	zones := map[string]bool{}
	for _, z := range d.Zones {
		zones[z.ID] = true
	}
	for chain, rules := range map[string][]rule{"input": d.Policy.Input, "forward": d.Policy.Forward} {
		seen := map[string]bool{}
		for i, r := range rules {
			p := fmt.Sprintf("/policy/%s/%d", chain, i)
			unique("/policy/"+chain, i, r.identity, seen)
			if !slices.Contains([]string{"accept", "drop", "reject"}, r.Action) {
				bad(p+"/action", "Expected accept, drop or reject.")
			}
			if chain == "input" && len(r.To) > 0 {
				bad(p+"/to", "An input rule's destination is the firewall itself.")
			}
			// sides collects the families of each side's addresses; a side without addresses matches both.
			side := func(field string, refs []string) map[int]bool {
				f := map[int]bool{}
				for j, ref := range refs {
					switch {
					case zones[ref]:
						// A zone is an alternative that matches either family.
						f[4], f[6] = true, true
					case addresses[ref] != nil:
						for family := range families[ref] {
							f[family] = true
						}
					default:
						bad(fmt.Sprintf("%s/%s/%d", p, field, j), "Expected an existing zone or address.")
					}
				}
				return f
			}
			from, to := side("from", r.From), side("to", r.To)
			if len(from) > 0 && len(to) > 0 && !(from[4] && to[4]) && !(from[6] && to[6]) {
				bad(p+"/to", "The source and destination addresses are of different families, so this rule can never match.")
			}
			// The families a service can match: icmp only IPv4, icmpv6 only IPv6, ports both.
			offered, leaves := map[int]bool{}, 0
			for j, ref := range r.Services {
				s := services[ref]
				if s == nil {
					bad(fmt.Sprintf("%s/services/%d", p, j), "Expected an existing service.")
					continue
				}
				for _, m := range append([]string{ref}, s.Members...) {
					if member := services[m]; member != nil {
						switch member.Protocol {
						case "icmp":
							offered[4] = true
							leaves++
						case "icmpv6":
							offered[6] = true
							leaves++
						case "tcp", "udp":
							offered[4], offered[6] = true, true
							leaves++
						}
					}
				}
			}
			meets := func(f int) bool { return (len(from) == 0 || from[f]) && (len(to) == 0 || to[f]) && offered[f] }
			if len(offered) > 0 && !meets(4) && !meets(6) {
				bad(p+"/services", "No service is of the family of the rule's addresses, so this rule can never match.")
			}
			// Each from x to x service combination is one nft line; a runaway rule would exhaust the host's filter.
			if max(1, len(r.From))*max(1, len(r.To))*max(1, leaves) > maxRuleLines {
				bad(p, "This rule expands to too many filter lines. Use address groups or fewer services.")
			}
		}
	}

	if m := d.Management; m != nil {
		if !slices.ContainsFunc(d.Segments, func(s segment) bool { return s.ID == m.Segment }) {
			bad("/management/segment", "Expected an existing segment.")
		}
		for j, ref := range m.AllowedSources {
			if addresses[ref] == nil {
				bad(fmt.Sprintf("/management/allowedSources/%d", j), "Expected an existing address.")
			}
		}
	}
	if mode := d.NAT.Outbound.Mode; mode != "" && mode != "none" && mode != "automatic" {
		bad("/nat/outbound/mode", "Expected none or automatic.")
	}
	return out
}

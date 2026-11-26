package config

import (
	"cmp"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// maxRuleLines caps the nft lines one rule renders to.
const maxRuleLines = 4096

// maxLeftOut caps the aliases left out on one nft line, where each is one match whatever it holds. The kernel caps a
// rule's expression bytes: it refused the heaviest line a port forward renders at 49 aliases (6.12, amd64, measured
// 2026-11-22), and 40 leaves room for other kernels and one more match. A group is one alias.
// TestLeftOutCapLoads (render, integration) loads that line at this cap: change both together.
const maxLeftOut = 40

// What a rule that can never match is told. A legacy rule split by upgradeLegacy may be such a rule.
const (
	neverAddresses = "The source and destination addresses are of different families, so this rule can never match."
	neverPorts     = "No port alias is of the family of the rule's addresses, so this rule can never match."
)

// ICMP types a port alias may name; nft knows them by these names.
var icmpTypes = map[string][]string{
	"icmp":   {"echo-request", "echo-reply", "destination-unreachable", "time-exceeded", "parameter-problem", "timestamp-request", "timestamp-reply"},
	"icmpv6": {"echo-request", "echo-reply", "destination-unreachable", "packet-too-big", "time-exceeded", "parameter-problem"},
}

// negatedDynamic refuses a URL or host-name alias on a side that leaves it out: its set is empty until its list is
// fetched, and leaving out an empty set lets every address through.
const negatedDynamic = "An alias fed from a URL or from host names cannot be left out yet: until its list is fetched it is empty, and leaving it out would let every address through."

// validatePolicy checks the aliases, the rules and the management section against each other and the topology.
// It reports limit errors at most.
func validatePolicy(d *declaration, limit int) []FieldError {
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < limit {
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
	for i := range d.Aliases.Addresses {
		a := &d.Aliases.Addresses[i]
		unique("/aliases/addresses", i, a.identity, seen)
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
	for i := range d.Aliases.Addresses {
		a := &d.Aliases.Addresses[i]
		p := fmt.Sprintf("/aliases/addresses/%d", i)
		f := map[int]bool{}
		fields := map[string]bool{"address": a.Address != "", "prefix": a.Prefix != "", "first": a.First != "" || a.Last != "", "members": a.Members != nil,
			"url": a.URL != "", "names": a.Names != nil, "refreshSeconds": a.RefreshSeconds != 0}
		want := map[string][]string{"host": {"address"}, "network": {"prefix"}, "range": {"first"}, "group": {"members"},
			"url": {"url", "refreshSeconds"}, "hostnames": {"names", "refreshSeconds"}}[a.Kind]
		for field, set := range fields {
			if set && !slices.Contains(want, field) {
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
					bad(fmt.Sprintf("%s/members/%d", p, j), "Members must be existing address aliases that are not groups.")
				} else if member.dynamic() {
					bad(fmt.Sprintf("%s/members/%d", p, j), "An alias fed from a URL or from host names cannot be a group's member yet.")
				}
			}
		case "url":
			if !aliasURL(a.URL) {
				bad(p+"/url", "Expected an https or http URL with a host, a valid port if any, no user name or password, and no spaces or invisible characters.")
			}
		case "hostnames":
			for j, name := range a.Names {
				lower := strings.ToLower(name)
				labels := strings.Split(lower, ".")
				// A resolver reads a name whose last label is a number, such as 127.1 or 0x7f.1, as an address.
				last := labels[len(labels)-1]
				_, numeric := strconv.ParseUint(last, 10, 64)
				if hex, ok := strings.CutPrefix(last, "0x"); ok {
					_, numeric = strconv.ParseUint(hex, 16, 64)
				}
				if _, e := netip.ParseAddr(name); e == nil || !searchDomain.MatchString(lower) || numeric == nil {
					bad(fmt.Sprintf("%s/names/%d", p, j), "Expected a host name, such as vpn.example.com, not an address.")
				} else if slices.ContainsFunc(a.Names[:j], func(n string) bool { return strings.EqualFold(n, name) }) {
					bad(fmt.Sprintf("%s/names/%d", p, j), "Host name is listed twice.")
				}
			}
		}
		if a.dynamic() {
			if a.RefreshSeconds < 60 || a.RefreshSeconds > 86400 {
				bad(p+"/refreshSeconds", "Expected a refresh interval from 60 to 86400 seconds.")
			}
			if a.Kind == "hostnames" && len(a.Names) == 0 {
				bad(p+"/names", "Expected at least one host name.")
			}
			// Its members are known only once fetched or resolved.
			f[4], f[6] = true, true
		}
		families[a.ID] = f
	}
	for _, a := range d.Aliases.Addresses {
		if a.Kind == "group" {
			for _, m := range a.Members {
				for family := range families[m] {
					families[a.ID][family] = true
				}
			}
		}
	}

	services := map[string]*portAlias{}
	seen = map[string]bool{}
	for i := range d.Aliases.Ports {
		s := &d.Aliases.Ports[i]
		unique("/aliases/ports", i, s.identity, seen)
		services[s.ID] = s
	}
	for i := range d.Aliases.Ports {
		s := &d.Aliases.Ports[i]
		p := fmt.Sprintf("/aliases/ports/%d", i)
		ports, types, members := s.Protocol == "tcp" || s.Protocol == "udp", icmpTypes[s.Protocol] != nil, s.Protocol == "group"
		if !ports && s.Ports != nil {
			bad(p+"/ports", "Only tcp and udp port aliases have ports.")
		}
		if !types && s.Types != nil {
			bad(p+"/types", "Only icmp and icmpv6 port aliases have types.")
		}
		if !members && s.Members != nil {
			bad(p+"/members", "Only groups have members.")
		}
		if ports && len(s.Ports) == 0 {
			bad(p+"/ports", "A tcp or udp port alias needs at least one port.")
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
				bad(fmt.Sprintf("%s/members/%d", p, j), "Members must be existing port aliases that are not groups.")
			}
		}
	}

	zones := map[string]bool{}
	for _, z := range d.Zones {
		zones[z.ID] = true
	}
	ruleIDs := map[string]bool{}
	for i, r := range d.Rules {
		p := fmt.Sprintf("/rules/%d", i)
		// A rule's name says what it is for and need not be the only one: its place in the list tells rules apart.
		if ruleIDs[r.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		ruleIDs[r.ID] = true
		if invalidText(r.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if !slices.Contains([]string{"accept", "drop", "reject"}, r.Action) {
			bad(p+"/action", "Expected accept, drop or reject.")
		}
		// side checks one side and returns the families its addresses hold; none means it matches both.
		side := func(field string, s ruleSide) map[int]bool {
			for j, ref := range s.Zones {
				if ref != firewallZone && !zones[ref] {
					bad(fmt.Sprintf("%s/%s/zones/%d", p, field, j), "Expected an existing zone.")
				}
			}
			f := map[int]bool{}
			for j, ref := range s.Addresses {
				if addresses[ref] == nil {
					bad(fmt.Sprintf("%s/%s/addresses/%d", p, field, j), "Expected an existing address alias.")
				}
				for family := range families[ref] {
					f[family] = true
				}
			}
			if s.Not {
				if len(s.Addresses) == 0 {
					bad(fmt.Sprintf("%s/%s/not", p, field), "Name the addresses to leave out.")
				}
				for j, ref := range s.Addresses {
					if a := addresses[ref]; a != nil && a.dynamic() {
						bad(fmt.Sprintf("%s/%s/addresses/%d", p, field, j), negatedDynamic)
					}
				}
				if len(s.Addresses) > maxLeftOut {
					bad(fmt.Sprintf("%s/%s/addresses", p, field), fmt.Sprintf("A side leaves out at most %d aliases. Put more of them in a group.", maxLeftOut))
				}
				// Every address but the named ones: either family can match.
				return map[int]bool{}
			}
			return f
		}
		from, to := side("from", r.From), side("to", r.To)
		if j := slices.Index(r.From.Zones, firewallZone); j >= 0 {
			bad(fmt.Sprintf("%s/from/zones/%d", p, j), "The firewall is a destination only: there are no rules yet for what the firewall itself sends.")
		}
		if r.From.Not && r.To.Not && len(r.From.Addresses) <= maxLeftOut && len(r.To.Addresses) <= maxLeftOut && len(r.From.Addresses)+len(r.To.Addresses) > maxLeftOut {
			bad(p+"/to/addresses", fmt.Sprintf("A rule leaves out at most %d aliases on its two sides together. Put more of them in a group.", maxLeftOut))
		}
		if r.toFirewall() && len(r.To.Zones) > 1 {
			bad(p+"/to/zones", "The firewall stands alone as a destination. Reaching it and other zones takes two rules.")
		}
		if len(from) > 0 && len(to) > 0 && !(from[4] && to[4]) && !(from[6] && to[6]) {
			bad(p+"/to/addresses", neverAddresses)
		}
		// The families a port alias can match: icmp only IPv4, icmpv6 only IPv6, ports both.
		offered, leaves := map[int]bool{}, 0
		for j, ref := range r.Ports {
			s := services[ref]
			if s == nil {
				bad(fmt.Sprintf("%s/ports/%d", p, j), "Expected an existing port alias.")
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
			bad(p+"/ports", neverPorts)
		}
		// Each zone x address x port alias combination of the two sides is one nft line; a runaway rule would
		// exhaust the host's filter. Times two: an alias holding both families renders one line per family.
		// A negated side is one line whatever it names.
		lines := func(s ruleSide) int {
			if s.Not {
				return max(1, len(s.Zones))
			}
			return max(1, len(s.Zones)) * max(1, len(s.Addresses))
		}
		if 2*lines(r.From)*lines(r.To)*max(1, leaves) > maxRuleLines {
			bad(p, "This rule expands to too many filter lines. Use address groups or fewer port aliases.")
		}
	}

	if m := d.Management; m != nil {
		if !slices.ContainsFunc(d.Segments, func(s segment) bool { return s.ID == m.Segment }) {
			bad("/management/segment", "Expected an existing segment.")
		}
		for j, ref := range m.AllowedSources {
			if addresses[ref] == nil {
				bad(fmt.Sprintf("/management/allowedSources/%d", j), "Expected an existing address alias.")
			} else if addresses[ref].dynamic() {
				bad(fmt.Sprintf("/management/allowedSources/%d", j), "An alias fed from a URL or from host names cannot be a management source yet.")
			}
		}
	}
	if mode := d.NAT.Outbound.Mode; mode != "" && mode != "none" && mode != "automatic" {
		bad("/nat/outbound/mode", "Expected none or automatic.")
	}
	return out
}

// aliasURL reports whether s is a URL an alias may be fed from: https or http, a host, a port in range when one is
// given, no credentials, and nothing a reader cannot see.
func aliasURL(s string) bool {
	u, e := url.Parse(s)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil ||
		invalidText(s, true) || strings.ContainsAny(s, " \t") {
		return false
	}
	port, e := strconv.Atoi(cmp.Or(u.Port(), "443"))
	return e == nil && port >= 1 && port <= 65535
}

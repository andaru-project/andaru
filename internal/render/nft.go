package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// nftPath labels the packet filter in a generation; it is loaded with nft, never written to /run.
const nftPath = "nft:inet/andaru"

// setName turns an object ID, which already names its kind, into an nft identifier; nft allows no "-".
func setName(id string) string { return strings.ReplaceAll(id, "-", "_") }

// family is 4 or 6; 0 means a line that matches both.
type family int

// filter renders table inet andaru: fixed rules, the policy lists in order, and outbound NAT.
type filter struct {
	r        *renderer
	sets     []string
	families map[string]map[family][]string // address ID -> family -> nft elements
	services map[string]service
	// reflection is set once any NAT entry reflects; it adds the internal sets and the hairpin masquerade.
	reflection bool
}

// filterChains renders the packet filter, or reports why it cannot; without a management segment there is none.
func (r *renderer) filterChains() {
	d := r.d
	if !d.filtered() {
		if len(d.Policy.Input)+len(d.Policy.Forward) > 0 || d.NAT.Outbound.Mode == "automatic" || len(d.NAT.PortForwards)+len(d.NAT.OneToOne) > 0 {
			r.find("error", "MANAGEMENT_REQUIRED", "/management", "Rules and NAT take effect only with a management segment, which keeps SSH reachable whatever the rules say. Set management.segment.")
		}
		return
	}
	if !r.in.Facts.NftTool {
		r.find("error", "TOOL_MISSING", "", "nft is not installed, so the packet filter cannot be loaded; install the nftables package.")
	}
	for _, unit := range r.in.Facts.FilterUnits {
		r.find("error", "FOREIGN_FILTER", unit, unit+" is enabled and would replace or override Andaru's packet filter at boot. Disable it.")
	}
	f := &filter{r: r, families: map[string]map[family][]string{}, services: map[string]service{}}
	for _, s := range d.Objects.Services {
		f.services[s.ID] = s
	}
	f.addresses()

	// Zones are sets of interface names, so an interface that appears later is matched too.
	zones := map[string][]string{}
	uplinks := []string{}
	for _, s := range d.Segments {
		name, ok := r.name[s.Link]
		if !ok || !r.usable[s.Link] {
			continue
		}
		zones[s.Zone] = append(zones[s.Zone], name)
		l := d.Links[r.index[s.Link]]
		if s.Addressing.IPv4.Gateway != "" || s.Addressing.IPv4.Mode == "dhcp" || l.Kind == "pppoe" {
			uplinks = append(uplinks, name)
		}
	}
	for _, z := range d.Zones {
		f.set(setName(z.ID), "ifname", "", quoted(zones[z.ID]))
	}
	f.set("uplinks", "ifname", "", quoted(uplinks))

	mgmt := ""
	for _, s := range d.Segments {
		if s.ID == d.Management.Segment && r.usable[s.Link] {
			mgmt = r.name[s.Link]
		}
	}
	if mgmt == "" {
		r.find("error", "MANAGEMENT_REQUIRED", "/management/segment", "The management segment's interface is unavailable, so SSH would not be reachable.")
		return
	}
	var allowed [2][]string
	for _, id := range d.Management.AllowedSources {
		for i, fam := range []family{4, 6} {
			allowed[i] = append(allowed[i], f.families[id][fam]...)
		}
	}
	input := []string{
		"ct state established,related accept",
		"ct state invalid drop",
		"iif lo accept",
		"icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-solicit, nd-router-advert } accept",
		"ip6 saddr fe80::/10 udp sport 547 udp dport 546 accept",
		// Multicast listener discovery never leaves the link, and RFC 4890 says not to drop it.
		"ip6 saddr fe80::/10 icmpv6 type { mld-listener-query, mld-listener-report, mld-listener-done, mld2-listener-report } accept",
	}
	// DHCP requests come from clients without an address yet, which no rule could name.
	for _, e := range d.DHCP {
		if name, _, ok := r.served(e.Segment); ok {
			input = append(input, fmt.Sprintf("iifname %q udp sport 68 udp dport 67 accept", name))
		}
	}
	for _, e := range d.DHCP6 {
		name, _, ok := r.served6(e.Segment)
		if !ok {
			name, _, _, ok = r.delegated6(e.Segment)
		}
		if ok {
			input = append(input, fmt.Sprintf("iifname %q ip6 saddr fe80::/10 udp sport 546 udp dport 547 accept", name))
		}
	}
	if len(d.Management.AllowedSources) == 0 {
		input = append(input, fmt.Sprintf("iifname %q tcp dport 22 accept", mgmt))
	}
	for i, fam := range []family{4, 6} {
		if len(allowed[i]) > 0 {
			set := fmt.Sprintf("mgmt_%d", fam)
			f.set(set, map[family]string{4: "ipv4_addr", 6: "ipv6_addr"}[fam], "interval", allowed[i])
			input = append(input, fmt.Sprintf("iifname %q %s saddr @%s tcp dport 22 accept", mgmt, ipKey(fam), set))
		}
	}
	prerouting, boundForward, boundInput := f.portForwards()
	binat, postrouting := f.oneToOne()
	prerouting = append(prerouting, binat...)
	if f.reflection {
		names, prefixes, owners := r.internalSegments()
		var own []string
		for i, p := range prefixes {
			own = append(own, p.Addr().String())
			// Replies to a hairpin connection must come back through the firewall, not straight from the server.
			postrouting = append(postrouting, fmt.Sprintf("oifname %q ct status dnat ip saddr %s masquerade comment %q", owners[i], p.Masked(), "reflection"))
		}
		f.set("internal", "ifname", "", quoted(names))
		// A virtual address belongs to its service: reflection never takes it.
		v := f.virtual4()
		f.set("internal_4", "ipv4_addr", "", append(own, v...))
		if len(v) > 0 {
			f.set("virtual_4", "ipv4_addr", "", v)
		}
	}
	// A forward's own rule comes before the policy, so no later rule can shadow it.
	input = append(input, boundInput...)
	for _, rule := range d.Policy.Input {
		input = append(input, f.rule(rule, false)...)
	}
	forward := append([]string{"ct state established,related accept", "ct state invalid drop"}, boundForward...)
	for _, rule := range d.Policy.Forward {
		forward = append(forward, f.rule(rule, true)...)
	}

	var body strings.Builder
	for _, s := range f.sets {
		body.WriteString("\t" + s + "\n")
	}
	chain := func(name, hook string, lines []string) {
		fmt.Fprintf(&body, "\tchain %s {\n\t\t%s\n", name, hook)
		for _, l := range lines {
			body.WriteString("\t\t" + l + "\n")
		}
		body.WriteString("\t}\n")
	}
	chain("input", "type filter hook input priority filter; policy drop;", input)
	chain("forward", "type filter hook forward priority filter; policy drop;", forward)
	if len(prerouting) > 0 {
		chain("prerouting", "type nat hook prerouting priority dstnat;", prerouting)
	}
	if d.NAT.Outbound.Mode == "automatic" {
		// fib saddr type != local keeps host-originated traffic out of the masquerade. It comes last, so a 1:1 snat wins.
		postrouting = append(postrouting, "fib saddr type != local oifname @uplinks meta nfproto ipv4 masquerade")
	}
	if len(postrouting) > 0 {
		chain("postrouting", "type nat hook postrouting priority srcnat;", postrouting)
	}
	// Created, deleted and defined in one file, which nft -f applies as one transaction.
	head := "table inet andaru {}\ndelete table inet andaru\ntable inet andaru {\n"
	sum := sha256.Sum256([]byte(head + body.String() + "}\n"))
	// Each card's name now and its MAC, so the filter can follow a card the kernel names differently at boot.
	var cards strings.Builder
	for _, l := range d.Links {
		if l.Kind == "ethernet" && r.rendered[l.ID] {
			fmt.Fprintf(&cards, "# interface %s %s\n", r.name[l.ID], l.PermanentMac)
		}
	}
	script := cards.String() + head + "\tcomment \"sha256:" + hex.EncodeToString(sum[:]) + "\"\n" + body.String() + "}\n"
	r.file(nftPath, "0644", script, []string{"/policy", "/nat", "/management"})
}

func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (f *filter) set(name, typ, flags string, elements []string) {
	s := "set " + name + " { type " + typ + ";"
	if flags != "" {
		s += " flags " + flags + ";"
	}
	if flags == "interval" {
		// Overlapping elements, such as a host inside a network of the same group, are merged rather than refused.
		s += " auto-merge;"
	}
	if len(elements) > 0 {
		s += " elements = { " + strings.Join(elements, ", ") + " };"
	}
	f.sets = append(f.sets, s+" }")
}

// addresses renders each address object as one interval set per family it holds.
func (f *filter) addresses() {
	byID := map[string]address{}
	for _, a := range f.r.d.Objects.Addresses {
		byID[a.ID] = a
	}
	single := func(a address) (family, string) {
		switch a.Kind {
		case "host":
			v, _ := netip.ParseAddr(a.Address)
			return familyOf(v), v.String()
		case "network":
			p, _ := netip.ParsePrefix(a.Prefix)
			return familyOf(p.Addr()), p.String()
		case "range":
			v, _ := netip.ParseAddr(a.First)
			return familyOf(v), a.First + "-" + a.Last
		}
		return 0, ""
	}
	for _, a := range f.r.d.Objects.Addresses {
		elements := map[family][]string{}
		members := []address{a}
		if a.Kind == "group" {
			members = members[:0]
			for _, m := range a.Members {
				members = append(members, byID[m])
			}
		}
		for _, m := range members {
			if fam, e := single(m); fam != 0 {
				elements[fam] = append(elements[fam], e)
			}
		}
		f.families[a.ID] = elements
		for _, fam := range []family{4, 6} {
			if len(elements[fam]) > 0 {
				f.set(setName(a.ID)+fmt.Sprintf("_%d", fam), map[family]string{4: "ipv4_addr", 6: "ipv6_addr"}[fam], "interval", elements[fam])
			}
		}
	}
}

func familyOf(a netip.Addr) family {
	if a.Is4() {
		return 4
	}
	return 6
}

func ipKey(f family) string {
	if f == 4 {
		return "ip"
	}
	return "ip6"
}

// rule renders one policy rule as nft lines: one per source entry, destination entry, service and family.
// Entries of one side are alternatives, which one nft line cannot express when they mix zones and addresses.
func (f *filter) rule(r rule, forward bool) []string {
	if !r.Enabled {
		return nil
	}
	type match struct {
		expr string
		fam  family // 0: any family
	}
	side := func(refs []string, iface, dir string) []match {
		if len(refs) == 0 {
			return []match{{}}
		}
		var out []match
		for _, ref := range refs {
			if fams, ok := f.families[ref]; ok {
				for _, fam := range []family{4, 6} {
					if len(fams[fam]) > 0 {
						out = append(out, match{fmt.Sprintf("%s %s @%s_%d", ipKey(fam), dir, setName(ref), fam), fam})
					}
				}
				continue
			}
			out = append(out, match{fmt.Sprintf("%s @%s", iface, setName(ref)), 0})
		}
		return out
	}
	from := side(r.From, "iifname", "saddr")
	to := []match{{}}
	if forward {
		to = side(r.To, "oifname", "daddr")
	}
	services := []service{{}}
	if len(r.Services) > 0 {
		services = nil
		for _, id := range r.Services {
			s := f.services[id]
			if s.Protocol != "group" {
				services = append(services, s)
				continue
			}
			for _, m := range s.Members {
				services = append(services, f.services[m])
			}
		}
	}
	var lines []string
	for _, a := range from {
		for _, b := range to {
			if a.fam != 0 && b.fam != 0 && a.fam != b.fam {
				continue
			}
			fam := max(a.fam, b.fam)
			for _, s := range services {
				expr, ok := serviceMatch(s, fam)
				if !ok {
					continue
				}
				parts := slices.DeleteFunc([]string{a.expr, b.expr, expr}, func(p string) bool { return p == "" })
				if r.Log {
					parts = append(parts, fmt.Sprintf("log prefix %q", "andaru "+r.ID+" "))
				}
				verdict := r.Action
				if verdict == "reject" && s.Protocol == "tcp" {
					verdict = "reject with tcp reset"
				}
				parts = append(parts, verdict, fmt.Sprintf("comment %q", r.ID))
				lines = append(lines, strings.Join(parts, " "))
			}
		}
	}
	return lines
}

// serviceMatch renders a service for a line of the given family; false when the service cannot match it.
func serviceMatch(s service, fam family) (string, bool) {
	switch s.Protocol {
	case "":
		return "", true
	case "tcp", "udp":
		return s.Protocol + " dport { " + strings.Join(s.Ports, ", ") + " }", true
	case "icmp", "icmpv6":
		if s.Protocol == "icmp" && fam == 6 || s.Protocol == "icmpv6" && fam == 4 {
			return "", false
		}
		if len(s.Types) == 0 {
			return "meta l4proto " + map[string]string{"icmp": "icmp", "icmpv6": "ipv6-icmp"}[s.Protocol], true
		}
		return s.Protocol + " type { " + strings.Join(s.Types, ", ") + " }", true
	}
	return "", false
}

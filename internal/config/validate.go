package config

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

type locatedPrefix struct {
	prefix netip.Prefix
	owner  string
	path   string
}

func invalidText(s string, format bool) bool {
	for _, r := range s {
		if unicode.IsControl(r) || (format && unicode.Is(unicode.Cf, r)) {
			return true
		}
	}
	return false
}
func validateTopology(d *declaration) []FieldError {
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{path, msg})
		}
	}
	if len(d.Links)+len(d.Segments)+len(d.Zones) > objectLimit {
		bad("", "Too many network objects.")
		return out
	}
	ids := map[string]bool{}
	names := map[string]bool{}
	check := func(v identity, p string) {
		if ids[v.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		ids[v.ID] = true
		if invalidText(v.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if names[v.Name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		names[v.Name] = true
	}
	links := make(map[string]*link, len(d.Links))
	indexes := map[string]int{}
	macs := map[string]bool{}
	vlans := map[string]bool{}
	slave := map[string]string{}
	for i := range d.Links {
		l := &d.Links[i]
		p := fmt.Sprintf("/links/%d", i)
		check(l.identity, p)
		links[l.ID] = l
		indexes[l.ID] = i
		if l.Kind == "ethernet" {
			m, e := net.ParseMAC(l.PermanentMAC)
			if e != nil || len(m) != 6 {
				bad(p+"/permanentMac", "Expected a six-byte unicast MAC.")
			} else {
				zero := true
				for _, b := range m {
					if b != 0 {
						zero = false
					}
				}
				if zero || m[0]&1 != 0 {
					bad(p+"/permanentMac", "Expected a nonzero unicast MAC.")
				}
				l.PermanentMAC = m.String()
				if macs[l.PermanentMAC] {
					bad(p+"/permanentMac", "Permanent MAC is already assigned.")
				}
				macs[l.PermanentMAC] = true
			}
		}
		if l.Kind == "vlan" && l.VLANID != nil {
			key := l.Parent + ":" + strconv.Itoa(*l.VLANID)
			if vlans[key] {
				bad(p+"/vlanId", "Duplicate VLAN parent and tag.")
			}
			vlans[key] = true
		}
		if invalidText(l.Username, false) {
			bad(p+"/username", "Control characters are not allowed.")
		}
		for _, m := range linkMembers(*l) {
			if _, ok := slave[m]; ok {
				bad(p+"/members", "A member can have only one master.")
			}
			slave[m] = l.ID
		}
	}
	edge := map[string][]string{}
	for i := range d.Links {
		l := &d.Links[i]
		p := fmt.Sprintf("/links/%d", i)
		if l.Parent != "" {
			parent, ok := links[l.Parent]
			if !ok || parent.Kind == "pppoe" {
				bad(p+"/parent", "Parent must refer to an Ethernet-like link.")
			} else {
				overhead := 0
				if l.Kind == "pppoe" {
					overhead = 8
				}
				if l.MTU > parent.MTU-overhead {
					bad(p+"/mtu", "MTU exceeds the parent payload.")
				}
			}
			if _, ok := slave[l.Parent]; ok {
				bad(p+"/parent", "Use the member's bridge or bond master.")
			}
			edge[l.ID] = append(edge[l.ID], l.Parent)
		}
		for j, m := range linkMembers(*l) {
			target, ok := links[m]
			if !ok || target.Kind == "pppoe" || target.Kind == "bridge" || (l.Kind == "bond" && target.Kind != "ethernet") {
				bad(fmt.Sprintf("%s/members/%d", p, j), "Invalid member link kind or reference.")
			}
			edge[l.ID] = append(edge[l.ID], m)
		}
	}
	colors := map[string]uint8{}
	var visit func(string) bool
	visit = func(id string) bool {
		if colors[id] == 1 {
			return false
		}
		if colors[id] == 2 {
			return true
		}
		colors[id] = 1
		for _, next := range edge[id] {
			if !visit(next) {
				return false
			}
		}
		colors[id] = 2
		return true
	}
	for _, l := range d.Links {
		if !visit(l.ID) {
			bad(fmt.Sprintf("/links/%d", indexes[l.ID]), "Link dependencies contain a cycle.")
			break
		}
	}
	names = map[string]bool{}
	zones := map[string]bool{}
	for i, z := range d.Zones {
		check(z.identity, fmt.Sprintf("/zones/%d", i))
		zones[z.ID] = true
	}
	names = map[string]bool{}
	segments := map[string]*segment{}
	usedLinks := map[string]bool{}
	for i := range d.Segments {
		s := &d.Segments[i]
		check(s.identity, fmt.Sprintf("/segments/%d", i))
		segments[s.ID] = s
	}
	var ranges []locatedPrefix
	hosts := map[string]bool{}
	prefixes := map[string][]netip.Prefix{}
	delegations := map[string]bool{}
	for i := range d.Segments {
		s := &d.Segments[i]
		p := fmt.Sprintf("/segments/%d", i)
		l, ok := links[s.Link]
		if !ok {
			bad(p+"/link", "Link reference does not exist.")
		} else {
			if usedLinks[s.Link] {
				bad(p+"/link", "Link already has a segment.")
			}
			usedLinks[s.Link] = true
			if _, ok := slave[s.Link]; ok {
				bad(p+"/link", "Use the member's master link.")
			}
			if s.Addressing.IPv6.Mode != "none" && l.MTU < 1280 {
				bad(p+"/addressing/ipv6", "IPv6 requires MTU at least 1280.")
			}
			if (s.Addressing.IPv4.Mode == "ppp" && l.Kind != "pppoe") || (l.Kind == "pppoe" && s.Addressing.IPv4.Mode != "ppp" && s.Addressing.IPv4.Mode != "none") {
				bad(p+"/addressing/ipv4/mode", "IPv4 mode does not match link kind.")
			}
		}
		if !zones[s.Zone] {
			bad(p+"/zone", "Zone reference does not exist.")
		}
		if s.Addressing.IPv6.RA != nil && s.Addressing.IPv6.RA.Enabled && s.Addressing.IPv6.Mode == "none" {
			bad(p+"/addressing/ipv6/ra", "Router advertisements require IPv6.")
		}
		a6 := &s.Addressing.IPv6
		if a6.Mode == "delegated" {
			source, ok := segments[a6.From]
			if !ok || source.ID == s.ID || source.Addressing.IPv6.Mode != "dhcp6" {
				bad(p+"/addressing/ipv6/from", "Prefix source must be another DHCPv6 segment.")
			}
			n, e := strconv.ParseUint(a6.SubnetID, 16, 16)
			if e != nil {
				bad(p+"/addressing/ipv6/subnetId", "Invalid hexadecimal subnet ID.")
			} else {
				a6.SubnetID = strconv.FormatUint(n, 16)
				key := a6.From + ":" + a6.SubnetID
				if delegations[key] {
					bad(p+"/addressing/ipv6/subnetId", "Delegated subnet is already assigned.")
				}
				delegations[key] = true
			}
		}
		for family, addresses := range map[int][]string{4: s.Addressing.IPv4.Addresses, 6: a6.Addresses} {
			for j, raw := range addresses {
				path := fmt.Sprintf("%s/addressing/ipv%d/addresses/%d", p, family, j)
				prefix, e := netip.ParsePrefix(raw)
				if e != nil || prefix.Addr().Is4In6() || prefix.Addr().Zone() != "" || (family == 4) != prefix.Addr().Is4() || !validLocal(prefix) {
					bad(path, "Invalid host address or prefix.")
					continue
				}
				addr := prefix.Addr()
				key := hostKey(addr, s.ID)
				if hosts[key] {
					bad(path, "Local address is already assigned.")
				}
				hosts[key] = true
				addresses[j] = prefix.String()
				prefixes[s.ID] = append(prefixes[s.ID], prefix)
				if !addr.Is6() || !addr.IsLinkLocalUnicast() {
					ranges = append(ranges, locatedPrefix{prefix.Masked(), s.ID, path})
				}
			}
		}
	}
	slices.SortFunc(ranges, func(a, b locatedPrefix) int {
		if n := a.prefix.Addr().Compare(b.prefix.Addr()); n != 0 {
			return n
		}
		return a.prefix.Bits() - b.prefix.Bits()
	})
	var previous *locatedPrefix
	for i := range ranges {
		v := &ranges[i]
		if previous != nil && previous.prefix.Contains(v.prefix.Addr()) {
			if previous.owner != v.owner {
				bad(v.path, "Static subnet overlaps another segment.")
			}
			continue
		}
		previous = v
	}
	for i, s := range d.Segments {
		for family, raw := range map[int]string{4: s.Addressing.IPv4.Gateway, 6: s.Addressing.IPv6.Gateway} {
			if raw == "" {
				continue
			}
			path := fmt.Sprintf("/segments/%d/addressing/ipv%d/gateway", i, family)
			a, e := netip.ParseAddr(raw)
			if e != nil || (family == 4) != a.Is4() || a.Is4In6() || a.Zone() != "" || !validUnicast(a) || hosts[hostKey(a, s.ID)] {
				bad(path, "Invalid or locally assigned gateway.")
				continue
			}
			onLink := a.Is6() && a.IsLinkLocalUnicast()
			for _, p := range prefixes[s.ID] {
				if p.Contains(a) && validLocal(netip.PrefixFrom(a, p.Bits())) {
					onLink = true
				}
			}
			if !onLink {
				bad(path, "Gateway is not on this segment.")
			}
		}
	}
	return out
}
func hostKey(a netip.Addr, owner string) string {
	if a.Is6() && a.IsLinkLocalUnicast() {
		return owner + ":" + a.String()
	}
	return a.String()
}
func validUnicast(a netip.Addr) bool {
	return a.IsValid() && !a.IsUnspecified() && !a.IsLoopback() && !a.IsMulticast() && !(a.Is4() && a.As4() == [4]byte{255, 255, 255, 255})
}
func validLocal(p netip.Prefix) bool {
	a := p.Addr()
	if !validUnicast(a) {
		return false
	}
	if a.Is4() && p.Bits() <= 30 {
		if a == p.Masked().Addr() {
			return false
		}
		b := p.Masked().Addr().As4()
		for bit := p.Bits(); bit < 32; bit++ {
			b[bit/8] |= 1 << uint(7-bit%8)
		}
		if a == netip.AddrFrom4(b) {
			return false
		}
	}
	return true
}
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

package config

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/delegation"
)

// subnetBits is how many bits a DHCPv6 segment's requested prefix leaves for subnet IDs; false without a request.
func subnetBits(wan *segment) (int, bool) {
	if wan == nil {
		return 0, false
	}
	if l := wan.Addressing.IPv6.RequestPrefixLength; l != nil && *l <= 64 {
		return 64 - *l, true
	}
	return 0, false
}

// delegatedFrom returns the DHCPv6 segment a delegated segment takes its prefix from.
func delegatedFrom(d *declaration, id string) (segment, bool) {
	i := slices.IndexFunc(d.Segments, func(s segment) bool { return s.ID == id })
	if i < 0 || d.Segments[i].Addressing.IPv6.Mode != "delegated" {
		return segment{}, false
	}
	j := slices.IndexFunc(d.Segments, func(s segment) bool { return s.ID == d.Segments[i].Addressing.IPv6.From })
	if j < 0 {
		return segment{}, false
	}
	return d.Segments[j], true
}

// suffix parses an interface identifier: an IPv6 address whose first 64 bits are zero.
func suffix(raw string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(raw)
	if err != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" {
		return a, false
	}
	b := a.As16()
	return a, b == [16]byte{} || [8]byte(b[:8]) == [8]byte{}
}

// validateDelegatedDHCP6 checks an entry on a delegated segment. Positions are judged under the WAN's requested
// length, the only size known before the provider answers; the prefix actually delegated is judged at apply.
func validateDelegatedDHCP6(d *declaration, wan segment, e dhcp6Entry, p string, names map[string]bool, wanPools map[string][]netip.Prefix, bad func(string, string)) {
	hint := 64
	if l := wan.Addressing.IPv6.RequestPrefixLength; l != nil {
		hint = *l
	}
	base := netip.PrefixFrom(netip.IPv6Unspecified(), hint)
	// The /64s of the delegated segments on this WAN, at their subnet IDs.
	var lans []netip.Prefix
	for _, s := range d.Segments {
		if a6 := s.Addressing.IPv6; a6.Mode == "delegated" && a6.From == wan.ID {
			if n, err := strconv.ParseUint(a6.SubnetID, 16, 16); err == nil {
				if lan, ok := delegation.Place(base, n, 64); ok {
					lans = append(lans, lan)
				}
			}
		}
	}
	if e.LeaseTime != 0 && (e.LeaseTime < 300 || e.LeaseTime > 604800) {
		bad(p+"/leaseTime", "Expected 300 to 604800 seconds.")
	}
	const notSuffix = "Expected a suffix: an IPv6 address whose first 64 bits are zero."
	var taken [][2]netip.Addr
	for j, r := range e.Ranges {
		rp := fmt.Sprintf("%s/ranges/%d", p, j)
		first, ok1 := suffix(r.First)
		last, ok2 := suffix(r.Last)
		switch {
		case !ok1:
			bad(rp+"/first", notSuffix)
		case !ok2:
			bad(rp+"/last", notSuffix)
		case last.Less(first):
			bad(rp, "The first address comes after the last.")
		case slices.ContainsFunc(taken, func(t [2]netip.Addr) bool { return !last.Less(t[0]) && !t[1].Less(first) }):
			bad(rp, "The range overlaps another range.")
		default:
			taken = append(taken, [2]netip.Addr{first, last})
		}
	}
	for j, raw := range e.DNSServers {
		if a, err := netip.ParseAddr(raw); err != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" || a.IsLinkLocalUnicast() || !validUnicast(a) {
			bad(fmt.Sprintf("%s/dnsServers/%d", p, j), "Expected a unicast IPv6 address that is not link-local.")
		}
	}
	idBits := -1 // the bits a prefixId has in the pool; -1 without a pool
	if pd := e.PrefixDelegation; pd != nil {
		pp := p + "/prefixDelegation"
		if pd.Prefix != "" {
			bad(pp+"/prefix", "A delegated segment's pool is a position in the WAN's prefix: set subnetId and length.")
		}
		if pd.ExcludedPrefix != "" {
			bad(pp+"/excludedPrefix", "An excluded prefix is not offered on a delegated segment.")
		}
		switch {
		case hint >= 64:
			bad(pp+"/length", "A pool needs the WAN to request a prefix of 63 or shorter (requestPrefixLength).")
		case pd.Length <= hint || pd.Length > 64:
			bad(pp+"/length", fmt.Sprintf("Expected %d to 64: longer than the WAN's /%d request.", hint+1, hint))
		case pd.DelegatedLength < pd.Length || pd.DelegatedLength > 64:
			bad(pp+"/delegatedLength", fmt.Sprintf("Expected %d to 64: the pool's length or longer.", pd.Length))
		default:
			n, err := strconv.ParseUint(pd.SubnetID, 16, 16)
			pool, ok := delegation.Place(base, n, pd.Length)
			switch {
			case err != nil || !ok:
				bad(pp+"/subnetId", fmt.Sprintf("Expected a hexadecimal position of %d bits.", pd.Length-hint))
			case slices.ContainsFunc(lans, pool.Overlaps):
				bad(pp+"/subnetId", "The pool overlaps a delegated segment's /64.")
			case slices.ContainsFunc(wanPools[wan.ID], pool.Overlaps):
				bad(pp+"/subnetId", "The pool overlaps another DHCPv6 entry's pool.")
			default:
				wanPools[wan.ID] = append(wanPools[wan.ID], pool)
				idBits = pd.DelegatedLength - pd.Length
			}
		}
	}
	duids, addresses, ids := map[string]bool{}, map[netip.Addr]bool{}, map[uint64]bool{}
	for j, l := range e.StaticLeases {
		lp := fmt.Sprintf("%s/staticLeases/%d", p, j)
		duid := strings.ToLower(l.DUID)
		switch {
		case !validDUID(duid):
			bad(lp+"/duid", "Expected a DUID of type 1 to 4 with the length its type needs (RFC 8415).")
		case duids[duid]:
			bad(lp+"/duid", "This DUID already has a static lease.")
		default:
			duids[duid] = true
			e.StaticLeases[j].DUID = duid
		}
		if l.Prefix != "" {
			bad(lp+"/prefix", "A delegated segment's prefix changes with the provider's; name a position in the pool with prefixId.")
		}
		if l.Address == "" && l.PrefixID == "" {
			bad(lp, "Expected an address, a prefixId or both.")
		}
		if l.Address != "" {
			a, ok := suffix(l.Address)
			switch {
			case !ok:
				bad(lp+"/address", notSuffix)
			case slices.ContainsFunc(taken, func(t [2]netip.Addr) bool { return !a.Less(t[0]) && !t[1].Less(a) }):
				bad(lp+"/address", "This address is inside a range; reserve one outside them.")
			case addresses[a]:
				bad(lp+"/address", "This address already has a static lease.")
			default:
				addresses[a] = true
			}
		}
		if l.PrefixID != "" {
			n, err := strconv.ParseUint(l.PrefixID, 16, 16)
			switch {
			case idBits < 0:
				bad(lp+"/prefixId", "Expected a pool on this entry (prefixDelegation).")
			case err != nil || (idBits < 16 && n>>idBits != 0):
				bad(lp+"/prefixId", fmt.Sprintf("Expected 0 to %x: a position in the pool.", uint64(1)<<idBits-1))
			case ids[n]:
				bad(lp+"/prefixId", "This position already has a static lease.")
			default:
				ids[n] = true
			}
		}
		if l.Hostname != "" && !dnsLabel.MatchString(l.Hostname) {
			bad(lp+"/hostname", "Expected one name label: letters, digits and inner hyphens.")
		} else if name := strings.ToLower(l.Hostname); name != "" && names[name] {
			bad(lp+"/hostname", "Another DHCPv6 static lease already has this name.")
		} else {
			names[name] = true
		}
	}
}

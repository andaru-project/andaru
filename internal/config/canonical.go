package config

import (
	"encoding/json"
	"encoding/json/jsontext"
	"net/netip"
	"slices"
	"strings"
)

func canonical(v any) ([]byte, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	value := jsontext.Value(raw)
	if e = value.Canonicalize(jsontext.CanonicalizeRawInts(false)); e != nil {
		return nil, e
	}
	if len(value) >= documentLimit {
		return nil, errTooLarge
	}
	return append(value, '\n'), nil
}
func normalizeDeclaration(d *declaration) {
	slices.SortFunc(d.Links, func(a, b link) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(d.Segments, func(a, b segment) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(d.Zones, func(a, b zone) int { return strings.Compare(a.ID, b.ID) })
	for i := range d.Links {
		if d.Links[i].Members != nil {
			slices.Sort(*d.Links[i].Members)
		}
	}
	for i := range d.Segments {
		slices.Sort(d.Segments[i].Addressing.IPv4.Addresses)
		slices.Sort(d.Segments[i].Addressing.IPv6.Addresses)
	}
	// Objects are sets; policy lists are ordered and keep their order.
	slices.SortFunc(d.Objects.Addresses, func(a, b address) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(d.Objects.Services, func(a, b service) int { return strings.Compare(a.ID, b.ID) })
	for i := range d.Objects.Addresses {
		slices.Sort(d.Objects.Addresses[i].Members)
	}
	for i := range d.Objects.Services {
		slices.Sort(d.Objects.Services[i].Members)
	}
	if len(d.Objects.Addresses)+len(d.Objects.Services) == 0 {
		d.Objects = objectSet{}
	}
	if len(d.Policy.Input)+len(d.Policy.Forward) == 0 {
		d.Policy = policy{}
	}
	if d.Management != nil {
		slices.Sort(d.Management.AllowedSources)
	}
	slices.SortFunc(d.DHCP, func(a, b dhcpEntry) int { return strings.Compare(a.ID, b.ID) })
	for i := range d.DHCP {
		e := &d.DHCP[i]
		slices.SortFunc(e.Ranges, func(a, b dhcpRange) int { return compareAddr(a.First, b.First) })
		slices.SortFunc(e.StaticLeases, func(a, b staticLease) int { return strings.Compare(a.MAC, b.MAC) })
		if e.LeaseTime == 0 {
			e.LeaseTime = 7200
		}
	}
	if d.DNS != nil {
		slices.Sort(d.DNS.Segments)
		if d.DNS.LocalDomain == "" {
			d.DNS.LocalDomain = "home.arpa"
		}
		if d.DNS.RegisterLeases == nil {
			register := true
			d.DNS.RegisterLeases = &register
		}
	}
}

// compareAddr orders addresses numerically; validation has already parsed them.
func compareAddr(a, b string) int {
	x, _ := netip.ParseAddr(a)
	y, _ := netip.ParseAddr(b)
	return x.Compare(y)
}

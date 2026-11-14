package config

import (
	"fmt"
	"net/netip"
	"slices"
)

// vipDevice is the dummy interface that holds the virtual addresses; no link may take its name.
const vipDevice = "andaru-vip"

// validateVirtual checks the virtual addresses: single unicast addresses the firewall holds on no segment.
func validateVirtual(d *declaration, bad func(string, string)) {
	var subnets []netip.Prefix
	for _, s := range d.Segments {
		for _, raw := range append(slices.Clone(s.Addressing.IPv4.Addresses), s.Addressing.IPv6.Addresses...) {
			if p, e := netip.ParsePrefix(raw); e == nil {
				subnets = append(subnets, p)
			}
		}
	}
	seen := map[string]bool{}
	held := map[netip.Addr]bool{}
	for i, v := range d.VirtualAddresses {
		p := fmt.Sprintf("/virtualAddresses/%d", i)
		if seen[v.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		if invalidText(v.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if seen["name:"+v.Name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		seen[v.ID], seen["name:"+v.Name] = true, true
		a, e := netip.ParseAddr(v.Address)
		switch {
		case e != nil || a.Zone() != "" || a.Is4In6() || !validUnicast(a) || a.IsLinkLocalUnicast():
			bad(p+"/address", "Expected one unicast IPv4 or IPv6 address, without a length; not loopback or link-local.")
		case held[a]:
			bad(p+"/address", "Another virtual address already has this address.")
		case slices.ContainsFunc(subnets, func(s netip.Prefix) bool { return s.Contains(a) }):
			// Linux answers for a local address on every interface, so it would work there by accident.
			bad(p+"/address", "This address is on a segment's subnet: add it to the segment's addresses instead.")
		default:
			held[a] = true
		}
	}
	if d.DNS != nil {
		for j, id := range d.DNS.VirtualAddresses {
			if slices.Contains(d.DNS.VirtualAddresses[:j], id) {
				bad(fmt.Sprintf("/dns/virtualAddresses/%d", j), "This virtual address is listed twice.")
			} else if !slices.ContainsFunc(d.VirtualAddresses, func(v virtualAddress) bool { return v.ID == id }) {
				bad(fmt.Sprintf("/dns/virtualAddresses/%d", j), "Expected an existing virtual address.")
			}
		}
	}
}

// firewallHolds says whether a is one of the firewall's declared static addresses or virtual addresses.
func firewallHolds(d *declaration, a netip.Addr) bool {
	for _, s := range d.Segments {
		for _, raw := range append(slices.Clone(s.Addressing.IPv4.Addresses), s.Addressing.IPv6.Addresses...) {
			if p, e := netip.ParsePrefix(raw); e == nil && p.Addr() == a {
				return true
			}
		}
	}
	return slices.ContainsFunc(d.VirtualAddresses, func(v virtualAddress) bool { return sameAddress(v.Address, a.String()) })
}

// sameAddress compares two addresses however they are written.
func sameAddress(a, b string) bool {
	x, e1 := netip.ParseAddr(a)
	y, e2 := netip.ParseAddr(b)
	return e1 == nil && e2 == nil && x == y
}

// vipStillTargeted refuses an operation that deletes a virtual address, or changes its address, while a port
// forward targets the address: the forward would silently become one to an inside host nobody holds.
func vipStillTargeted(old, d *declaration, input Changes) *Fault {
	for _, v := range old.VirtualAddresses {
		if slices.ContainsFunc(d.VirtualAddresses, func(n virtualAddress) bool { return sameAddress(n.Address, v.Address) }) {
			continue
		}
		i := slices.IndexFunc(d.NAT.PortForwards, func(f portForward) bool { return f.Target != nil && sameAddress(f.Target.Address, v.Address) })
		if i < 0 {
			continue
		}
		for j, op := range input.Operations {
			if op.Collection == "virtualAddresses" && op.ID == v.ID {
				f := fault("VALIDATION_FAILED", "", "")
				f.Details = []FieldError{{input.path(j, "/id", false), fmt.Sprintf("Port forward %q targets this address; change or delete it first.", d.NAT.PortForwards[i].Name)}}
				return f
			}
		}
	}
	return nil
}

// disabledHolder names the disabled link whose segment declares a, or "".
func disabledHolder(d *declaration, a netip.Addr) string {
	for _, s := range d.Segments {
		for _, raw := range append(slices.Clone(s.Addressing.IPv4.Addresses), s.Addressing.IPv6.Addresses...) {
			if p, e := netip.ParsePrefix(raw); e == nil && p.Addr() == a {
				for _, l := range d.Links {
					if l.ID == s.Link && !l.Enabled {
						return l.Name
					}
				}
			}
		}
	}
	return ""
}

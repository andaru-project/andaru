package config

import (
	"fmt"
	"net/netip"
	"slices"
)

// route is a static route to destination through a target interface, by way of gateway when one is named.
type route struct {
	identity
	Destination string      `json:"destination"`
	Target      routeTarget `json:"target"`
	Gateway     string      `json:"gateway,omitempty"`
	Metric      uint32      `json:"metric,omitempty"`
	Enabled     bool        `json:"enabled"`
}

// routeTarget is the interface a route leaves by: a segment's link, or a VPN interface.
type routeTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// routeTargets says, by target kind, whether a declaration holds a target. A kind without an entry has no
// targets yet: the VPN kinds join it with their collections.
var routeTargets = map[string]func(d *declaration, id string) bool{
	"segment": func(d *declaration, id string) bool {
		return slices.ContainsFunc(d.Segments, func(s segment) bool { return s.ID == id })
	},
}

// pointToPoint target kinds have no next hop on their link, so a route through one names no gateway.
func pointToPoint(kind string) bool { return kind == "wireguard" || kind == "ipsec" }

// routeTupleKey identifies a route's kernel uniqueness tuple (masked destination, target, metric).
func routeTupleKey(r *route) string {
	dest, err := netip.ParsePrefix(r.Destination)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("route:%s %s %s %d", dest.Masked(), r.Target.Kind, r.Target.ID, r.Metric)
}

func validateRoutes(d *declaration) []FieldError {
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{path, msg})
		}
	}
	seen := map[string]bool{}
	for i := range d.Routes {
		r := &d.Routes[i]
		p := fmt.Sprintf("/routes/%d", i)
		if seen[r.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		if invalidText(r.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if seen["name:"+r.Name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		seen[r.ID], seen["name:"+r.Name] = true, true
		if found, known := routeTargets[r.Target.Kind]; !known || !found(d, r.Target.ID) {
			bad(p+"/target/id", "Expected an existing target of this kind.")
		}
		dest, err := netip.ParsePrefix(r.Destination)
		if err != nil || dest.Addr().Is4In6() {
			bad(p+"/destination", "Expected an IPv4 or IPv6 prefix in CIDR notation.")
			continue
		}
		dest = dest.Masked()
		if dest.Addr().IsLoopback() || dest.Addr().IsMulticast() {
			bad(p+"/destination", "Loopback and multicast destinations cannot be routed.")
		}
		if r.Gateway != "" {
			gw, err := netip.ParseAddr(r.Gateway)
			switch {
			case pointToPoint(r.Target.Kind):
				bad(p+"/gateway", "A route through a point-to-point interface names no gateway.")
			case err != nil || gw.Zone() != "" || gw.Is4In6():
				bad(p+"/gateway", "Expected an IPv4 or IPv6 address.")
			case gw.Is4() != dest.Addr().Is4():
				bad(p+"/gateway", "Expected a gateway of the destination's address family.")
			case !validUnicast(gw):
				bad(p+"/gateway", "Expected a unicast gateway address.")
			}
		}
		// One route per destination, target and metric: the kernel would refuse the second.
		key := routeTupleKey(r)
		if seen[key] {
			bad(p+"/destination", "Another route has this destination, target and metric.")
		}
		seen[key] = true
	}
	return out
}

// canonicalizeRoute stores one spelling of a validated route's prefix and gateway: the prefix masked.
func canonicalizeRoute(r *route) {
	if x, err := netip.ParsePrefix(r.Destination); err == nil {
		r.Destination = x.Masked().String()
	}
	if a, err := netip.ParseAddr(r.Gateway); err == nil {
		r.Gateway = a.String()
	}
}

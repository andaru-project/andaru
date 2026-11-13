package render

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/netip"
	"slices"
	"strings"
)

const (
	keaPath      = "/run/andaru-kea/kea-dhcp4.conf"
	kea6Path     = "/run/andaru-kea/kea-dhcp6.conf"
	unboundPath  = "/run/andaru-unbound/andaru.conf"
	resolvedPath = "/run/systemd/resolved.conf.d/50-andaru.conf"
)

type dhcpEntry struct {
	ID           string `json:"id"`
	Segment      string `json:"segment"`
	Ranges       []struct{ First, Last string }
	LeaseTime    int      `json:"leaseTime"`
	DNSServers   []string `json:"dnsServers"`
	StaticLeases []struct {
		MAC, Address, Hostname string
	} `json:"staticLeases"`
}
type dnsService struct {
	Mode      string `json:"mode"`
	Upstreams []struct {
		Address, Protocol string
		TLSName           string `json:"tlsName"`
	} `json:"upstreams"`
	Segments       []string `json:"segments"`
	LocalDomain    string   `json:"localDomain"`
	SearchDomains  []string `json:"searchDomains"`
	RegisterLeases *bool    `json:"registerLeases"`
}

// served is a segment's interface name and static IPv4 address, when it has both.
func (r *renderer) served(id string) (string, netip.Prefix, bool) {
	for _, s := range r.d.Segments {
		if s.ID != id || !r.usable[s.Link] || s.Addressing.IPv4.Mode != "static" {
			continue
		}
		for _, raw := range s.Addressing.IPv4.Addresses {
			if p, err := netip.ParsePrefix(raw); err == nil && p.Addr().Is4() {
				return r.name[s.Link], p, true
			}
		}
	}
	return "", netip.Prefix{}, false
}

// servedV6 is a usable static or delegated segment's interface name, first static IPv6 address that is not
// link-local (the stable address the DNS service answers on, whatever prefix the upstream delegates) and mode.
func (r *renderer) servedV6(id string) (string, netip.Prefix, string, bool) {
	for _, s := range r.d.Segments {
		if s.ID != id || !r.usable[s.Link] || (s.Addressing.IPv6.Mode != "static" && s.Addressing.IPv6.Mode != "delegated") {
			continue
		}
		for _, raw := range slices.Sorted(slices.Values(s.Addressing.IPv6.Addresses)) {
			if p, err := netip.ParsePrefix(raw); err == nil && p.Addr().Is6() && !p.Addr().IsLinkLocalUnicast() {
				return r.name[s.Link], p, s.Addressing.IPv6.Mode, true
			}
		}
	}
	return "", netip.Prefix{}, "", false
}

// services renders the DHCP server, the DNS service and the host's resolver.
func (r *renderer) services() {
	d := r.d
	if len(d.DHCP) > 0 && (!r.in.Facts.KeaTool || r.in.Facts.KeaHooks == "") {
		r.find("error", "TOOL_MISSING", "/dhcp", "The DHCP server is not installed; install the kea-dhcp4-server package.")
	}
	if d.DNS != nil && !r.in.Facts.UnboundTool {
		r.find("error", "TOOL_MISSING", "/dns", "The DNS service is not installed; install the unbound package.")
	}
	if d.DNS != nil && !r.in.Facts.ResolvedInCharge {
		r.find("warning", "DNS_NOT_APPLIED", "/dns", "This host's /etc/resolv.conf is not managed by systemd-resolved, so the host itself does not use the DNS service; the LAN does.")
	}
	dnsServes := func(segment string) bool { return d.DNS != nil && slices.Contains(d.DNS.Segments, segment) }
	register := d.DNS != nil && (d.DNS.RegisterLeases == nil || *d.DNS.RegisterLeases)

	var interfaces []string
	var subnets []map[string]any
	var names []string // static lease records, published by the DNS service
	ids := map[uint32]bool{}
	for i, e := range d.DHCP {
		name, own, ok := r.served(e.Segment)
		if !ok {
			continue // the segment's own finding says why
		}
		interfaces = append(interfaces, name)
		servers := e.DNSServers
		if len(servers) == 0 && dnsServes(e.Segment) {
			servers = []string{own.Addr().String()}
		}
		if len(servers) == 0 {
			r.find("warning", "DHCP_WITHOUT_DNS", fmt.Sprintf("/dhcp/%d/dnsServers", i), "Clients get no DNS server: the DNS service does not serve this segment and none is listed.")
		}
		options := []map[string]any{{"name": "routers", "data": own.Addr().String()}}
		if len(servers) > 0 {
			options = append(options, map[string]any{"name": "domain-name-servers", "data": strings.Join(servers, ", ")})
		}
		domain := ""
		if d.DNS != nil {
			domain = d.DNS.LocalDomain
			options = append(options, map[string]any{"name": "domain-name", "data": domain})
		}
		pools := []map[string]any{}
		for _, rg := range e.Ranges {
			pools = append(pools, map[string]any{"pool": rg.First + " - " + rg.Last})
		}
		reservations := []map[string]any{}
		for _, l := range e.StaticLeases {
			res := map[string]any{"hw-address": l.MAC, "ip-address": l.Address}
			if l.Hostname != "" {
				res["hostname"] = l.Hostname
				if dnsServes(e.Segment) {
					fqdn := l.Hostname + "." + domain + "."
					names = append(names, fmt.Sprintf("local-data: %q", fqdn+" 3600 IN A "+l.Address), fmt.Sprintf("local-data-ptr: %q", l.Address+" 3600 "+fqdn))
				}
			}
			reservations = append(reservations, res)
		}
		subnets = append(subnets, map[string]any{
			"id": subnetID(e.Segment, ids), "subnet": own.Masked().String(), "pools": pools, "valid-lifetime": e.LeaseTime,
			"reservations": reservations, "option-data": options,
			// The lease sync reads this, so it needs nothing but Kea's own configuration.
			"user-context": map[string]any{"andaru": map[string]any{"domain": domain, "register": register && dnsServes(e.Segment)}},
		})
	}
	if len(subnets) > 0 {
		kea := map[string]any{"Dhcp4": map[string]any{
			// Kea starts before networkd has the addresses; it binds as they appear.
			"interfaces-config": map[string]any{"interfaces": interfaces, "service-sockets-max-retries": 30, "service-sockets-retry-wait-time": 2000},
			"control-socket":    map[string]any{"socket-type": "unix", "socket-name": "/run/kea/kea4-ctrl-socket"},
			"lease-database":    map[string]any{"type": "memfile", "persist": true, "lfc-interval": 3600},
			"hooks-libraries": []map[string]any{
				{"library": r.in.Facts.KeaHooks + "/libdhcp_lease_cmds.so"},
				{"library": r.in.Facts.KeaHooks + "/libdhcp_run_script.so", "parameters": map[string]any{"name": "/usr/share/kea/scripts/andaru-lease-event", "sync": false}},
			},
			"subnet4": subnets,
		}}
		raw, _ := json.MarshalIndent(kea, "", "  ")
		r.file(keaPath, "0644", string(raw)+"\n", []string{"/dhcp"})
	}

	names = append(names, r.dhcp6(dnsServes, register)...)

	if d.DNS == nil {
		return
	}
	lines := []string{"server:", "\tinterface: 127.0.0.1", "\tinterface: ::1", "\t# Unbound may start before networkd has the addresses.", "\tip-freebind: yes"}
	acl := []string{"\taccess-control: 127.0.0.0/8 allow", "\taccess-control: ::1/128 allow"}
	for _, id := range d.DNS.Segments {
		if _, own, ok := r.served(id); ok {
			lines = append(lines, "\tinterface: "+own.Addr().String())
			acl = append(acl, "\taccess-control: "+own.Masked().String()+" allow")
		}
		if _, own, mode, ok := r.servedV6(id); ok {
			lines = append(lines, "\tinterface: "+own.Addr().String())
			if mode == "delegated" {
				// The delegated prefix is unknown when this is written, so every source reaching the listener is
				// allowed; applying a delegated segment requires the filter, which decides who reaches it.
				acl = append(acl, "\tinterface-action: "+own.Addr().String()+" allow")
			} else {
				// Every static subnet is advertised, so each one's clients may ask.
				for _, s := range r.d.Segments {
					if s.ID != id {
						continue
					}
					for _, raw := range slices.Sorted(slices.Values(s.Addressing.IPv6.Addresses)) {
						if p, err := netip.ParsePrefix(raw); err == nil && p.Addr().Is6() && !p.Addr().IsLinkLocalUnicast() {
							acl = append(acl, "\taccess-control: "+p.Masked().String()+" allow")
						}
					}
				}
			}
		}
	}
	// Hosts behind a downstream router are numbered from the segment's delegation pool.
	for _, e := range d.DHCP6 {
		if _, _, _, ok := r.servedV6(e.Segment); ok && slices.Contains(d.DNS.Segments, e.Segment) && e.PrefixDelegation != nil {
			acl = append(acl, "\taccess-control: "+e.PrefixDelegation.Prefix+" allow")
		}
	}
	lines = append(lines, acl...)
	lines = append(lines, fmt.Sprintf("\tlocal-zone: %q static", d.DNS.LocalDomain+"."))
	for _, n := range names {
		lines = append(lines, "\t"+n)
	}
	if d.DNS.Mode == "forward" {
		tls := d.DNS.Upstreams[0].Protocol == "dot"
		if tls {
			lines = append(lines, "\ttls-cert-bundle: /etc/ssl/certs/ca-certificates.crt")
		}
		lines = append(lines, "forward-zone:", `	name: "."`)
		for _, u := range d.DNS.Upstreams {
			addr := u.Address
			if ap, err := netip.ParseAddrPort(u.Address); err == nil {
				addr = fmt.Sprintf("%s@%d", ap.Addr(), ap.Port())
			}
			if tls {
				addr += "#" + u.TLSName
			}
			lines = append(lines, "\tforward-addr: "+addr)
		}
		lines = append(lines, "\tforward-tls-upstream: "+yesNo(tls))
	}
	r.file(unboundPath, "0644", strings.Join(lines, "\n")+"\n", []string{"/dhcp", "/dns"})

	// The firewall resolves through the DNS service; links stop taking servers from DHCP.
	resolve := []string{"DNS=127.0.0.1 ::1"}
	if len(d.DNS.SearchDomains) > 0 {
		resolve = append(resolve, "Domains="+strings.Join(d.DNS.SearchDomains, " "))
	}
	r.file(resolvedPath, "0644", ini(section{"Resolve", resolve}), []string{"/dns"})
}

// subnetID is the served segment's Kea subnet ID. Kea's leases name their subnet by it, so it must not change
// when other entries come or go; it is derived from the segment's identity.
// Two segments hashing alike take the next free ID in declaration order, recorded in used.
func subnetID(segment string, used map[uint32]bool) uint32 {
	h := fnv.New32a()
	h.Write([]byte(segment))
	id := h.Sum32()%4294967294 + 1 // Kea's range is 1 to 4294967294
	for used[id] {
		id = id%4294967294 + 1
	}
	if used != nil {
		used[id] = true
	}
	return id
}

// served6 is a usable segment's interface and the /64 DHCPv6 serves: its first static one that is not link-local.
func (r *renderer) served6(id string) (string, netip.Prefix, bool) {
	for _, s := range r.d.Segments {
		if s.ID != id || !r.usable[s.Link] || s.Addressing.IPv6.Mode != "static" {
			continue
		}
		for _, raw := range slices.Sorted(slices.Values(s.Addressing.IPv6.Addresses)) {
			if p, err := netip.ParsePrefix(raw); err == nil && p.Addr().Is6() && !p.Addr().IsLinkLocalUnicast() && p.Bits() == 64 {
				return r.name[s.Link], p, true
			}
		}
	}
	return "", netip.Prefix{}, false
}

// dhcp6 renders Kea's DHCPv6 configuration and returns the static lease names the DNS service publishes.
func (r *renderer) dhcp6(dnsServes func(string) bool, register bool) (names []string) {
	d := r.d
	if len(d.DHCP6) == 0 {
		return nil
	}
	if !r.in.Facts.Kea6Tool || r.in.Facts.KeaHooks == "" {
		r.find("error", "TOOL_MISSING", "/dhcp6", "The DHCPv6 server is not installed; install the kea-dhcp6-server package.")
	}
	var interfaces []string
	var subnets []map[string]any
	ids := map[uint32]bool{}
	for i, e := range d.DHCP6 {
		name, own, ok := r.served6(e.Segment)
		if !ok {
			continue
		}
		if len(e.Ranges) > 0 {
			if mode := r.raMode(e.Segment); mode != "managed" && mode != "assisted" {
				r.find("warning", "DHCP6_UNANNOUNCED", fmt.Sprintf("/dhcp6/%d", i), "The segment's router advertisements do not tell clients to ask for addresses, so only clients that ask on their own get one; with advertisements disabled, clients also get no default route.")
			}
		}
		interfaces = append(interfaces, name)
		servers := e.DNSServers
		if len(servers) == 0 && dnsServes(e.Segment) {
			if _, stable, _, ok := r.servedV6(e.Segment); ok {
				servers = []string{stable.Addr().String()}
			}
		}
		options := []map[string]any{} // Kea refuses null
		if len(servers) > 0 {
			options = append(options, map[string]any{"name": "dns-servers", "data": strings.Join(servers, ", ")})
		}
		domain := ""
		if d.DNS != nil {
			domain = d.DNS.LocalDomain
			options = append(options, map[string]any{"name": "domain-search", "data": strings.Join(append([]string{domain}, d.DNS.SearchDomains...), ", ")})
		}
		pools := []map[string]any{}
		for _, rg := range e.Ranges {
			pools = append(pools, map[string]any{"pool": rg.First + " - " + rg.Last})
		}
		var pdPools []map[string]any
		if pd := e.PrefixDelegation; pd != nil {
			pool, _ := netip.ParsePrefix(pd.Prefix)
			entry := map[string]any{"prefix": pool.Addr().String(), "prefix-len": pool.Bits(), "delegated-len": pd.DelegatedLength}
			if x, err := netip.ParsePrefix(pd.ExcludedPrefix); err == nil {
				entry["excluded-prefix"], entry["excluded-prefix-len"] = x.Addr().String(), x.Bits()
			}
			pdPools = append(pdPools, entry)
		}
		reservations := []map[string]any{}
		for _, l := range e.StaticLeases {
			res := map[string]any{"duid": l.DUID}
			if l.Address != "" {
				res["ip-addresses"] = []string{l.Address}
			}
			if l.Prefix != "" {
				res["prefixes"] = []string{l.Prefix}
			}
			if l.Hostname != "" {
				res["hostname"] = l.Hostname
				if l.Address != "" && dnsServes(e.Segment) {
					fqdn := l.Hostname + "." + domain + "."
					names = append(names, fmt.Sprintf("local-data: %q", fqdn+" 3600 IN AAAA "+l.Address), fmt.Sprintf("local-data-ptr: %q", l.Address+" 3600 "+fqdn))
				}
			}
			reservations = append(reservations, res)
		}
		subnet := map[string]any{
			"id": subnetID(e.Segment, ids), "subnet": own.Masked().String(), "interface": name, "pools": pools, "reservations": reservations,
			// Kea's default ratio: the preferred lifetime is five-eighths of the valid one.
			"valid-lifetime": e.LeaseTime, "preferred-lifetime": e.LeaseTime * 5 / 8, "option-data": options,
			"user-context": map[string]any{"andaru": map[string]any{"domain": domain, "register": register && dnsServes(e.Segment)}},
		}
		if pdPools != nil {
			subnet["pd-pools"] = pdPools
		}
		subnets = append(subnets, subnet)
	}
	if len(subnets) == 0 {
		return names
	}
	kea := map[string]any{"Dhcp6": map[string]any{
		// Kea never listens on an interface that was down when it started, unless it retries.
		"interfaces-config": map[string]any{"interfaces": interfaces, "service-sockets-max-retries": 30, "service-sockets-retry-wait-time": 2000},
		"control-socket":    map[string]any{"socket-type": "unix", "socket-name": "/run/kea/kea6-ctrl-socket"},
		"lease-database":    map[string]any{"type": "memfile", "persist": true, "lfc-interval": 3600},
		"hooks-libraries": []map[string]any{
			{"library": r.in.Facts.KeaHooks + "/libdhcp_lease_cmds.so"},
			{"library": r.in.Facts.KeaHooks + "/libdhcp_run_script.so", "parameters": map[string]any{"name": "/usr/share/kea/scripts/andaru-lease-event", "sync": false}},
		},
		"subnet6": subnets,
	}}
	raw, _ := json.MarshalIndent(kea, "", "  ")
	r.file(kea6Path, "0644", string(raw)+"\n", []string{"/dhcp6"})
	return names
}

// raMode is a segment's router advertisement mode.
func (r *renderer) raMode(id string) string {
	for _, s := range r.d.Segments {
		if s.ID == id {
			return s.Addressing.IPv6.RA.mode()
		}
	}
	return "disabled"
}

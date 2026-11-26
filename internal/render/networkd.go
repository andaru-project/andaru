package render

import (
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

const networkDir = "/run/systemd/network/50-andaru-"

// UnclassifiedPath keeps every en* link no declaration names down: it sorts after each 50-andaru- file and before
// the image's 99-andaru-factory.network, whose DHCP must never reach WAN once the box is set up.
const UnclassifiedPath = "/run/systemd/network/51-andaru-unclassified.network"

// Unclassified is UnclassifiedPath's content: no address of any kind, and the link stays down.
var Unclassified = ini(section{"Match", []string{"Name=en*"}}, section{"Link", []string{"ActivationPolicy=down", "RequiredForOnline=no"}},
	section{"Network", []string{"LinkLocalAddressing=no", "DHCP=no", "IPv6AcceptRA=no"}})

type section struct {
	name  string
	lines []string
}

func ini(sections ...section) string {
	var b strings.Builder
	for _, s := range sections {
		if len(s.lines) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("[" + s.name + "]\n")
		for _, line := range s.lines {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// offered says what a segment takes from DHCP and router advertisements. NTP, hostname and domains
// are later areas. DNS servers are taken unless the segment declares its own or the DNS service resolves for the host.
func offered(useDNS string) []string {
	return []string{"UseDNS=" + useDNS, "UseNTP=no", "UseHostname=no", "UseDomains=no"}
}

// addressing renders one link's segment. ppp marks a pppd-owned interface:
// IPv4 comes from pppd and DHCPv6 must not wait for a router advertisement.
func (r *renderer) addressing(id string, ppp bool) (sections []section, sources []string) {
	network := []string{}
	none := func() ([]section, []string) {
		return []section{{"Network", []string{"LinkLocalAddressing=no", "IPv6AcceptRA=no"}}}, sources
	}
	si, ok := r.segment[id]
	if !ok || !r.usable[id] {
		return none()
	}
	s := r.d.Segments[si]
	sources = append(sources, fmt.Sprintf("/segments/%d", si))
	v4, v6 := s.Addressing.IPv4, s.Addressing.IPv6
	base := fmt.Sprintf("/segments/%d/addressing/ipv6", si)
	if v6.Mode == "delegated" {
		up, found := r.segmentByID(v6.From)
		if !found || !r.usable[up.Link] {
			r.find("warning", "DEPENDENCY_UNAVAILABLE", base+"/from", "IPv6 left out: the upstream segment is unavailable.")
			v6 = ipv6{Mode: "none"}
		}
	}
	dhcp := map[[2]bool]string{{true, true}: "yes", {true, false}: "ipv4", {false, true}: "ipv6"}[[2]bool{v4.Mode == "dhcp", v6.Mode == "dhcp6"}]
	if dhcp != "" {
		network = append(network, "DHCP="+dhcp)
	}
	addresses := slices.Concat(v4.Addresses, v6.Addresses)
	slices.Sort(addresses)
	for _, a := range addresses {
		network = append(network, "Address="+a)
	}
	for _, g := range []string{v4.Gateway, v6.Gateway} {
		if g != "" {
			network = append(network, "Gateway="+g)
		}
	}
	useDNS := yesNo(r.d.DNS == nil)
	if dns := s.Addressing.DNS; dns != nil {
		if !r.in.Facts.ResolvedInCharge {
			r.find("warning", "DNS_NOT_APPLIED", fmt.Sprintf("/segments/%d/addressing/dns", si), "This host's /etc/resolv.conf is not managed by systemd-resolved, so these servers are saved but not used.")
		}
		// Declared order is the order servers are tried in.
		for _, server := range dns.Servers {
			network = append(network, "DNS="+server)
		}
		if len(dns.SearchDomains) > 0 {
			network = append(network, "Domains="+strings.Join(dns.SearchDomains, " "))
		}
		useDNS = "no"
	}
	var dhcp4, dhcp6, accept, delegation, send []string
	var prefixes []section
	if v4.Mode == "dhcp" {
		dhcp4 = append(dhcp4, offered(useDNS)...)
	}
	mode := v6.RA.mode()
	sendRA := mode != "disabled"
	// A stable /64 on a static or delegated segment is announced unless the mode announces no prefix.
	announce := func(addresses []string) bool {
		ok := true
		for _, a := range addresses {
			p, e := netip.ParsePrefix(a)
			if e == nil && p.Addr().IsLinkLocalUnicast() {
				continue // every link already has fe80::/64; announcing it is meaningless
			}
			if e == nil && p.Bits() == 64 {
				prefixes = append(prefixes, section{"IPv6Prefix", []string{"Prefix=" + p.Masked().String()}})
			} else {
				ok = false
			}
		}
		return ok
	}
	switch v6.Mode {
	case "none":
		network = append(network, "LinkLocalAddressing=no", "IPv6AcceptRA=no")
	case "static":
		network = append(network, "IPv6AcceptRA=no")
		if !announce(v6.Addresses) && sendRA && mode != "router" {
			r.find("warning", "RA_PREFIX_UNSUPPORTED", base+"/ra", "Only /64 subnets can be announced for address autoconfiguration.")
		}
	case "slaac", "dhcp6":
		network = append(network, "IPv6AcceptRA=yes")
		accept = []string{"UseDNS=" + useDNS, "UseDomains=no"}
		if v6.Mode == "dhcp6" {
			// networkd otherwise follows the advertisement's flags, and an information request brings no prefix.
			accept = append(accept, "DHCPv6Client=always")
		}
		if v6.Mode == "dhcp6" && (ppp || v6.WithoutRA) {
			// Some providers send no router advertisement on PPP and expect a bare solicit.
			dhcp6 = append(dhcp6, "WithoutRA=solicit")
		}
		if v6.Mode == "dhcp6" && v6.DUID != "" {
			raw, _ := hex.DecodeString(strings.ReplaceAll(v6.DUID, ":", ""))
			dhcp6 = append(dhcp6, fmt.Sprintf("DUIDType=%d", binary.BigEndian.Uint16(raw)), "DUIDRawData="+v6.DUID[6:])
		}
		// Also for SLAAC: a router advertisement with the managed or other flag starts DHCPv6 by itself.
		dhcp6 = append(dhcp6, offered(useDNS)...)
		if v6.Mode == "dhcp6" {
			if v6.RequestPrefixLength != nil {
				dhcp6 = append(dhcp6, fmt.Sprintf("PrefixDelegationHint=::/%d", *v6.RequestPrefixLength))
			}
			if v6.PrefixOnly {
				dhcp6 = append(dhcp6, "UseAddress=no")
			}
			// A release makes many providers hand out a new prefix, renumbering the LAN.
			dhcp6 = append(dhcp6, "SendRelease="+yesNo(v6.SendRelease))
		}
		if sendRA {
			r.find("warning", "RA_PREFIX_UNSUPPORTED", base+"/ra", "Router advertisements are not sent on an upstream-facing segment.")
			sendRA = false
		}
	case "delegated":
		up, _ := r.segmentByID(v6.From)
		network = append(network, "IPv6AcceptRA=no", "DHCPPrefixDelegation=yes")
		announce(v6.Addresses)
		delegation = []string{"UplinkInterface=" + r.name[up.Link], "SubnetId=0x" + v6.SubnetID, "Assign=yes", "Announce=" + yesNo(sendRA && mode != "router")}
	}
	if !sendRA || mode == "router" {
		prefixes = nil
	}
	if sendRA {
		ra := v6.RA
		network = append(network, "IPv6SendRA=yes")
		send = []string{
			"RouterPreference=" + cmp.Or(ra.Priority, "medium"),
			fmt.Sprintf("RouterLifetimeSec=%d", deref(ra.RouterLifetime, 1800)),
			"Managed=" + yesNo(mode == "managed" || mode == "assisted"),
			"OtherInformation=" + yesNo(mode == "assisted" || mode == "stateless"),
		}
		for i := range prefixes {
			if v6.Mode == "static" {
				prefixes[i].lines = append(prefixes[i].lines, fmt.Sprintf("ValidLifetimeSec=%d", deref(ra.ValidLifetime, 86400)), fmt.Sprintf("PreferredLifetimeSec=%d", deref(ra.PreferredLifetime, 14400)))
			}
			if mode == "managed" {
				prefixes[i].lines = append(prefixes[i].lines, "AddressAutoconfiguration=no")
			}
		}
		var servers, domains []string
		if ra.DNS != nil {
			servers, domains = ra.DNS.Servers, ra.DNS.SearchDomains
		} else if r.d.DNS != nil && slices.Contains(r.d.DNS.Segments, s.ID) {
			if _, own, _, ok := r.servedV6(s.ID); ok {
				servers = []string{own.Addr().String()}
			} else {
				r.find("warning", "RA_DNS_UNAVAILABLE", base+"/addresses", "Add a static unique local address so clients can reach the DNS service over IPv6.")
			}
			domains = append([]string{r.d.DNS.LocalDomain}, r.d.DNS.SearchDomains...)
		}
		send = append(send, "EmitDNS="+yesNo(len(servers) > 0))
		if len(servers) > 0 {
			send = append(send, "DNS="+strings.Join(servers, " "))
		}
		send = append(send, "EmitDomains="+yesNo(len(domains) > 0))
		if len(domains) > 0 {
			send = append(send, "Domains="+strings.Join(domains, " "))
		}
		slices.SortFunc(prefixes, func(a, b section) int { return strings.Compare(a.lines[0], b.lines[0]) })
		if ra.PREF64 != "" {
			prefixes = append(prefixes, section{"IPv6PREF64Prefix", []string{"Prefix=" + ra.PREF64}})
		}
	}
	sections = []section{{"Network", network}, {"DHCPv4", dhcp4}, {"DHCPv6", dhcp6}, {"IPv6AcceptRA", accept}, {"DHCPPrefixDelegation", delegation}, {"IPv6SendRA", send}}
	// A delegation pool's unleased parts are refused here, not sent out of the WAN by the default route.
	for _, e := range r.d.DHCP6 {
		if e.Segment == s.ID && e.PrefixDelegation != nil && e.PrefixDelegation.Prefix != "" {
			prefixes = append(prefixes, section{"Route", []string{"Destination=" + e.PrefixDelegation.Prefix, "Type=unreachable"}})
			sources = append(sources, "/dhcp6")
		}
	}
	return append(sections, prefixes...), sources
}

func (r *renderer) segmentByID(id string) (segment, bool) {
	for _, s := range r.d.Segments {
		if s.ID == id {
			return s, true
		}
	}
	return segment{}, false
}

func (r *renderer) file(path, mode, content string, sources []string) {
	slices.Sort(sources)
	r.result.Files = append(r.result.Files, File{Path: path, Mode: mode, Content: content, Sources: sources})
}

// networkd renders the .netdev and .network of one Ethernet, VLAN, bridge or bond link.
func (r *renderer) networkd(i int) {
	l := r.d.Links[i]
	name := r.name[l.ID]
	self := linkPath(i, "")
	match := "Name=" + name
	if l.Kind == "ethernet" {
		// Never match or rename by kernel name: it follows the slot, not the card.
		match = "PermanentMACAddress=" + l.PermanentMac
		// The declared name survives a reboot once whatever named the card before (netplan) is gone.
		r.file(networkDir+name+".link", "0644", ini(section{"Match", []string{match}}, section{"Link", []string{"Name=" + name}}), []string{self})
	} else {
		var kind []section
		switch l.Kind {
		case "vlan":
			kind = []section{{"VLAN", []string{fmt.Sprintf("Id=%d", *l.VLANID)}}}
		case "bridge":
			var bridge []string
			if l.STP != nil {
				bridge = append(bridge, "STP="+yesNo(*l.STP))
			}
			if l.STPPriority != nil {
				bridge = append(bridge, fmt.Sprintf("Priority=%d", *l.STPPriority))
			}
			if l.ForwardDelay != nil {
				bridge = append(bridge, fmt.Sprintf("ForwardDelaySec=%ds", *l.ForwardDelay))
			}
			if l.HelloTime != nil {
				bridge = append(bridge, fmt.Sprintf("HelloTimeSec=%ds", *l.HelloTime))
			}
			if l.MaxAge != nil {
				bridge = append(bridge, fmt.Sprintf("MaxAgeSec=%ds", *l.MaxAge))
			}
			if l.MulticastSnooping != nil {
				bridge = append(bridge, "MulticastSnooping="+yesNo(*l.MulticastSnooping))
			}
			if len(bridge) > 0 {
				kind = []section{{"Bridge", bridge}}
			}
		case "bond":
			bond := []string{"Mode=" + l.Mode}
			if l.TransmitHashPolicy != "" {
				bond = append(bond, "TransmitHashPolicy="+l.TransmitHashPolicy)
			}
			if l.LACPRate != "" {
				bond = append(bond, "LACPTransmitRate="+l.LACPRate)
			}
			if l.MIIMonitorMs != nil {
				if *l.MIIMonitorMs == 0 {
					bond = append(bond, "MIIMonitorSec=0")
				} else {
					bond = append(bond, fmt.Sprintf("MIIMonitorSec=%dms", *l.MIIMonitorMs))
				}
			}
			kind = []section{{"Bond", bond}}
		}
		r.file(networkDir+name+".netdev", "0644", ini(append([]section{{"NetDev", []string{"Name=" + name, "Kind=" + l.Kind}}}, kind...)...), []string{self})
	}
	settings := []string{"RequiredForOnline=no", fmt.Sprintf("MTUBytes=%d", l.MTU)}
	if !l.Enabled {
		settings = append(settings, "ActivationPolicy=always-down")
	}
	network := []string{"LLDP=no"}
	var vlans []string
	for _, child := range r.d.Links {
		if child.Kind == "vlan" && child.Parent == l.ID && r.rendered[child.ID] {
			vlans = append(vlans, "VLAN="+r.name[child.ID])
		}
	}
	slices.Sort(vlans)
	network = append(network, vlans...)
	if m, ok := r.master[l.ID]; ok && r.rendered[m] && r.usable[l.ID] {
		key := map[string]string{"bridge": "Bridge=", "bond": "Bond="}[r.d.Links[r.index[m]].Kind]
		network = append(network, key+r.name[m])
	}
	sections, sources := r.addressing(l.ID, false)
	if _, segmented := r.segment[l.ID]; segmented && r.d.filtered() {
		// Forwarding only together with the filter: nothing may route unfiltered.
		network = append(network, "IPv4Forwarding=yes", "IPv6Forwarding=yes")
	}
	sections[0].lines = append(network, sections[0].lines...)
	r.file(networkDir+name+".network", "0644", ini(append([]section{{"Match", []string{match}}, {"Link", settings}}, sections...)...), append(sources, self))
}

// deref is *p, or fallback when p is nil.
func deref(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}

// virtualAddresses renders the dummy device holding the virtual addresses: networkd owns it entirely, on any
// host kind. Without virtual addresses there are no files, and the apply deletes the device.
func (r *renderer) virtualAddresses() {
	if len(r.d.VirtualAddresses) == 0 {
		return
	}
	var addresses []string
	for _, v := range r.d.VirtualAddresses {
		a, err := netip.ParseAddr(v.Address)
		if err != nil {
			continue
		}
		addresses = append(addresses, "Address="+netip.PrefixFrom(a, a.BitLen()).String())
	}
	sources := []string{"/virtualAddresses"}
	r.file(networkDir+vipDevice+".netdev", "0644", ini(section{"NetDev", []string{"Name=" + vipDevice, "Kind=dummy"}}), sources)
	network := append([]string{"LinkLocalAddressing=no", "LLDP=no"}, addresses...)
	r.file(networkDir+vipDevice+".network", "0644", ini(section{"Match", []string{"Name=" + vipDevice}}, section{"Link", []string{"RequiredForOnline=no"}}, section{"Network", network}), sources)
}

// vipDevice is the dummy interface that holds the virtual addresses.
const vipDevice = "andaru-vip"

package render

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
)

const networkDir = "/run/systemd/network/50-andaru-"

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
// are later areas. DNS servers are taken unless the segment declares its own.
// shortcut: the host uses provider or declared DNS for its own lookups; revisit when the DNS area owns the resolver.
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
	useDNS := "yes"
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
	identity := r.identity(id)
	if v4.Mode == "dhcp" {
		if identity != nil {
			// Stopping networkd during a rollback must neither drop the address nor release the lease dhcpcd takes back.
			network = append(network, "KeepConfiguration=dynamic-on-stop")
			dhcp4 = append([]string{"ClientIdentifier=duid"}, identity...)
			dhcp4 = append(dhcp4, "SendRelease=no")
			if h := r.in.DHCPIdentity[r.mac(id)].Hostname; h != "" {
				dhcp4 = append(dhcp4, "Hostname="+h)
			}
		}
		dhcp4 = append(dhcp4, offered(useDNS)...)
	}
	sendRA := v6.RA != nil && v6.RA.Enabled
	switch v6.Mode {
	case "none":
		network = append(network, "LinkLocalAddressing=no", "IPv6AcceptRA=no")
	case "static":
		network = append(network, "IPv6AcceptRA=no")
		announceable := true
		for _, a := range v6.Addresses {
			p, e := netip.ParsePrefix(a)
			if e == nil && p.Addr().IsLinkLocalUnicast() {
				continue // every link already has fe80::/64; announcing it is meaningless
			}
			if e == nil && p.Bits() == 64 {
				prefixes = append(prefixes, section{"IPv6Prefix", []string{"Prefix=" + p.Masked().String()}})
			} else {
				announceable = false
			}
		}
		if sendRA && !announceable {
			r.find("warning", "RA_PREFIX_UNSUPPORTED", base+"/ra", "Only /64 subnets can be announced for address autoconfiguration.")
		}
	case "slaac", "dhcp6":
		network = append(network, "IPv6AcceptRA=yes")
		accept = []string{"UseDNS=" + useDNS, "UseDomains=no"}
		if v6.Mode == "dhcp6" && ppp {
			// Some providers send no router advertisement on PPP and expect a bare solicit.
			dhcp6 = append(dhcp6, "WithoutRA=solicit")
		}
		// Also for SLAAC: a router advertisement with the managed or other flag starts DHCPv6 by itself.
		dhcp6 = append(append(dhcp6, identity...), offered(useDNS)...)
		if v6.Mode == "dhcp6" {
			if v6.RequestPrefixLength != nil {
				dhcp6 = append(dhcp6, fmt.Sprintf("PrefixDelegationHint=::/%d", *v6.RequestPrefixLength))
			}
		}
		if sendRA {
			r.find("warning", "RA_PREFIX_UNSUPPORTED", base+"/ra", "Router advertisements are not sent on an upstream-facing segment.")
			sendRA = false
		}
	case "delegated":
		up, _ := r.segmentByID(v6.From)
		network = append(network, "IPv6AcceptRA=no", "DHCPPrefixDelegation=yes")
		delegation = []string{"UplinkInterface=" + r.name[up.Link], "SubnetId=0x" + v6.SubnetID, "Assign=yes"}
	}
	if !sendRA {
		prefixes = nil
	} else {
		network = append(network, "IPv6SendRA=yes")
		send = []string{"Managed=" + yesNo(v6.RA.Managed), "OtherInformation=" + yesNo(v6.RA.OtherInfo)}
		slices.SortFunc(prefixes, func(a, b section) int { return strings.Compare(a.lines[0], b.lines[0]) })
	}
	sections = []section{{"Network", network}, {"DHCPv4", dhcp4}, {"DHCPv6", dhcp6}, {"IPv6AcceptRA", accept}, {"DHCPPrefixDelegation", delegation}, {"IPv6SendRA", send}}
	return append(sections, prefixes...), sources
}

// mac is an Ethernet link's permanent MAC in lower case, "" for other kinds.
func (r *renderer) mac(id string) string {
	if l := r.d.Links[r.index[id]]; l.Kind == "ethernet" {
		return strings.ToLower(l.PermanentMac)
	}
	return ""
}

// identity returns the DHCP identity lines dhcpcd used for this link, nil when there is none to keep.
// The IAID is the MAC's last four bytes and the DUID splits into its type and the rest, as dhcpcd builds them.
func (r *renderer) identity(id string) []string {
	known, ok := r.in.DHCPIdentity[r.mac(id)]
	mac, err := net.ParseMAC(r.mac(id))
	if !ok || err != nil || len(mac) < 4 || len(known.DUID) < 3 {
		return nil
	}
	raw := make([]string, len(known.DUID)-2)
	for i, b := range known.DUID[2:] {
		raw[i] = fmt.Sprintf("%02x", b)
	}
	return []string{
		"IAID=0x" + hex.EncodeToString(mac[len(mac)-4:]),
		fmt.Sprintf("DUIDType=%d", binary.BigEndian.Uint16(known.DUID)),
		"DUIDRawData=" + strings.Join(raw, ":"),
	}
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
		case "bond":
			kind = []section{{"Bond", []string{"Mode=" + l.Mode}}}
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

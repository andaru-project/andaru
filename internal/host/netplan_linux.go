package host

import (
	"bufio"
	"cmp"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Addressing is one address family as detected: mode is dhcp, dhcp6, slaac, static or none.
type Addressing struct {
	Mode      string
	Addresses []string
	Gateway   string
}

// NetplanInterface is what netplan configures on one physical interface, read from the files it generated.
type NetplanInterface struct {
	File, Name, PermanentMAC string
	MTU                      int
	IPv4, IPv6               Addressing
	DNS, Domains             []string
	DefaultRoute             bool
}

// NetplanProblem is something netplan configures that a takeover cannot express.
type NetplanProblem struct{ File, Key, Message string }

const netplanRuntime = "/run/systemd/network/"

// Keys netplan writes that the declaration covers, with the only values accepted where a value matters;
// anything else is a problem rather than silently dropped.
var netplanKeys = map[string]map[string][]string{
	"Match":   {"PermanentMACAddress": nil, "MACAddress": nil, "Name": nil},
	"Link":    {"MTUBytes": nil, "Name": nil, "WakeOnLan": {"off"}},
	"Network": {"DHCP": nil, "Address": nil, "Gateway": nil, "DNS": nil, "Domains": nil, "LinkLocalAddressing": nil, "IPv6AcceptRA": nil},
	"Route":   {"Destination": nil, "Gateway": nil},
	// netplan's defaults for DHCP links; they change nothing Andaru renders differently.
	"DHCP": {"RouteMetric": {"100"}, "UseMTU": {"true"}}, "DHCPv4": {"RouteMetric": {"100"}, "UseMTU": {"true"}},
}

// checkKeys reports every key, or value, of u that netplanKeys does not cover.
func checkKeys(u unit, problem func(key, message string)) {
	for section, keys := range u {
		name, _, _ := strings.Cut(section, "#")
		for key, values := range keys {
			allowed, ok := netplanKeys[name][key]
			if !ok || (allowed != nil && slices.ContainsFunc(values, func(v string) bool { return !slices.Contains(allowed, v) })) {
				problem("["+name+"] "+key, "This setting cannot be expressed in Andaru's configuration yet.")
			}
		}
	}
}

type unit map[string]map[string][]string

func readUnit(path string) (unit, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	u, section := unit{}, ""
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && strings.HasSuffix(line, "]"):
			section = line[1 : len(line)-1]
			if section == "Route" {
				// Each [Route] is its own route; keep them apart.
				section = "Route#" + strconv.Itoa(len(u))
			}
			if u[section] == nil {
				u[section] = map[string][]string{}
			}
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok || section == "" {
				return nil, fmt.Errorf("%s: unreadable line %q", path, line)
			}
			u[section][k] = append(u[section][k], v)
		}
	}
	return u, scan.Err()
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// ReadNetplan maps every netplan-generated file under root/run/systemd/network to the interface
// networkd bound it to. defaultRoute names the interface carrying the IPv4 default route.
func ReadNetplan(root string, links []LinkStatus, managed []Managed, defaultRoute string) ([]NetplanInterface, []NetplanProblem) {
	var found []NetplanInterface
	var problems []NetplanProblem
	paths, _ := filepath.Glob(filepath.Join(root, netplanRuntime, "10-netplan-*"))
	slices.Sort(paths)
	mtus := map[string]int{}
	for _, path := range paths {
		file := netplanRuntime + filepath.Base(path)
		problem := func(key, message string) { problems = append(problems, NetplanProblem{file, key, message}) }
		switch filepath.Ext(path) {
		case ".netdev":
			problem(".netdev", "netplan created a virtual device; bridges, bonds and VLANs made by netplan cannot be taken over yet.")
		case ".link":
			u, err := readUnit(path)
			if err != nil {
				problem("", err.Error())
				continue
			}
			checkKeys(u, problem)
			if n, err := strconv.Atoi(first(u["Link"]["MTUBytes"])); err == nil {
				mtus[strings.ToLower(first(u["Match"]["PermanentMACAddress"]))] = n
			}
		}
	}
	for _, path := range paths {
		if filepath.Ext(path) != ".network" {
			continue
		}
		file := netplanRuntime + filepath.Base(path)
		problem := func(key, message string) { problems = append(problems, NetplanProblem{file, key, message}) }
		u, err := readUnit(path)
		if err != nil {
			problem("", err.Error())
			continue
		}
		checkKeys(u, problem)
		var bound []Managed
		for _, m := range managed {
			if m.NetworkFile == file {
				bound = append(bound, m)
			}
		}
		if len(bound) != 1 {
			if len(bound) == 0 {
				problem("[Match]", "This file configures no interface now.")
			} else {
				problem("[Match]", fmt.Sprintf("This file configures %d interfaces; each must have its own.", len(bound)))
			}
			continue
		}
		i := slices.IndexFunc(links, func(l LinkStatus) bool { return l.Name == bound[0].Name })
		if i < 0 {
			problem("[Match]", "The interface this file configures was not found.")
			continue
		}
		link := links[i]
		if !strings.EqualFold(link.LinkAddress, link.PermanentAddress) {
			problem("MACAddress", "The interface's MAC address was changed; Andaru matches the permanent one and does not change it.")
			continue
		}
		n := NetplanInterface{File: file, Name: link.Name, PermanentMAC: strings.ToLower(link.PermanentAddress), MTU: link.MTU, DefaultRoute: link.Name == defaultRoute}
		if mtu, ok := mtus[n.PermanentMAC]; ok {
			n.MTU = mtu
		}
		network := u["Network"]
		dhcp := first(network["DHCP"])
		var v4, v6 []string
		for _, a := range network["Address"] {
			switch p, err := netip.ParsePrefix(a); {
			case err != nil:
				problem("Address", "An address without a prefix length cannot be expressed: "+a)
			case p.Addr().Is4():
				v4 = append(v4, a)
			default:
				v6 = append(v6, a)
			}
		}
		gateways := slices.Clone(network["Gateway"])
		for section, keys := range u {
			if !strings.HasPrefix(section, "Route") {
				continue
			}
			dest := first(keys["Destination"])
			if dest != "0.0.0.0/0" && dest != "::/0" {
				problem("[Route]", "Routes other than the default one cannot be expressed yet: "+dest)
				continue
			}
			gateways = append(gateways, keys["Gateway"]...)
		}
		gateway := func(v6 bool) string {
			for _, g := range gateways {
				if a, err := netip.ParseAddr(g); err == nil && a.Is6() == v6 {
					return g
				}
			}
			return ""
		}
		switch {
		case (dhcp == "yes" || dhcp == "ipv4") && len(v4) > 0:
			problem("DHCP", "IPv4 is both DHCP and static here.")
		case dhcp == "yes" || dhcp == "ipv4":
			n.IPv4.Mode = "dhcp"
		case len(v4) > 0:
			n.IPv4 = Addressing{"static", v4, gateway(false)}
		default:
			n.IPv4.Mode = "none"
		}
		if n.IPv4.Mode != "static" && gateway(false) != "" {
			problem("Gateway", "An IPv4 gateway without static IPv4 addresses cannot be expressed: "+gateway(false))
		}
		ra := first(network["IPv6AcceptRA"])
		liveSLAAC := slices.ContainsFunc(link.Addresses, func(a AddressStatus) bool {
			return a.Family == "ipv6" && a.Scope == "global" && a.PrefixLength != 128
		})
		switch {
		case (dhcp == "yes" || dhcp == "ipv6") && len(v6) > 0:
			problem("DHCP", "IPv6 is both DHCPv6 and static here.")
		case dhcp == "yes" || dhcp == "ipv6":
			n.IPv6.Mode = "dhcp6"
		case len(v6) > 0:
			n.IPv6 = Addressing{"static", v6, gateway(true)}
		// Without IPv6AcceptRA= networkd accepts router advertisements, so a live SLAAC address means SLAAC is in use.
		case ra == "yes" || (ra == "" && first(network["LinkLocalAddressing"]) != "no" && liveSLAAC):
			n.IPv6.Mode = "slaac"
		default:
			n.IPv6.Mode = "none"
		}
		if n.IPv6.Mode != "static" && gateway(true) != "" {
			problem("Gateway", "An IPv6 gateway without static IPv6 addresses cannot be expressed: "+gateway(true))
		}
		n.DNS = network["DNS"]
		for _, d := range network["Domains"] {
			for _, name := range strings.Fields(d) {
				if strings.HasPrefix(name, "~") {
					problem("Domains", "Routing-only DNS domains cannot be expressed yet: "+name)
				}
				n.Domains = append(n.Domains, name)
			}
		}
		found = append(found, n)
	}
	var routes, dns []string
	for _, n := range found {
		if n.IPv4.Gateway != "" || n.DefaultRoute {
			routes = append(routes, n.Name)
		}
		if len(n.DNS) > 0 || len(n.Domains) > 0 {
			dns = append(dns, n.Name)
		}
	}
	if len(routes) > 1 {
		problems = append(problems, NetplanProblem{netplanRuntime, "Gateway", "More than one interface has an IPv4 default route (" + strings.Join(routes, ", ") + "); only one uplink can be taken over yet."})
	}
	if len(dns) > 1 {
		problems = append(problems, NetplanProblem{netplanRuntime, "DNS", "More than one interface sets DNS servers (" + strings.Join(dns, ", ") + "); Andaru takes the host's DNS servers from one."})
	}
	// Map iteration found them; report them in a stable order.
	slices.SortFunc(problems, func(a, b NetplanProblem) int {
		return cmp.Or(strings.Compare(a.File, b.File), strings.Compare(a.Key, b.Key), strings.Compare(a.Message, b.Message))
	})
	return found, problems
}

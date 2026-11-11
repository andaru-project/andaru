package host

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host/netplanfixture"
)

const labMAC = "bc:24:11:89:01:10"

func labLink(addresses ...AddressStatus) []LinkStatus {
	return []LinkStatus{{IfIndex: 2, Name: "eth0", LinkType: "ether", MTU: 1500, LinkAddress: labMAC, PermanentAddress: labMAC, Addresses: addresses}}
}

var (
	v4      = AddressStatus{Family: "ipv4", Address: "10.0.1.19", PrefixLength: 16, Scope: "global"}
	slaac   = AddressStatus{Family: "ipv6", Address: "2a02:a466:79a7:beef:be24:11ff:fe89:110", PrefixLength: 64, Scope: "global"}
	dhcp6   = AddressStatus{Family: "ipv6", Address: "2a02:a466:79a7:beef::10ca", PrefixLength: 128, Scope: "global"}
	labBind = []Managed{{Name: "eth0", NetworkFile: "/run/systemd/network/10-netplan-eth0.network", State: "configured"}}
)

func TestReadNetplanLabBases(t *testing.T) {
	got, problems := ReadNetplan(netplanfixture.Root(t, "static"), labLink(v4, slaac, dhcp6), labBind, "eth0")
	if len(problems) != 0 || len(got) != 1 {
		t.Fatalf("got %+v, problems %+v", got, problems)
	}
	s := got[0]
	if s.Name != "eth0" || s.PermanentMAC != labMAC || s.MTU != 1500 || !s.DefaultRoute ||
		s.IPv4.Mode != "static" || !slices.Equal(s.IPv4.Addresses, []string{"10.0.1.19/16"}) || s.IPv4.Gateway != "10.0.0.1" ||
		!slices.Equal(s.DNS, []string{"172.16.53.53", "fd00:53:53:53:53:53:53:53"}) || !slices.Equal(s.Domains, []string{"lab.example"}) {
		t.Fatalf("static = %+v", s)
	}
	// netplan wrote no IPv6AcceptRA=, so networkd accepts router advertisements: the live SLAAC address must survive.
	if s.IPv6.Mode != "slaac" {
		t.Fatalf("IPv6 = %+v", s.IPv6)
	}

	got, problems = ReadNetplan(netplanfixture.Root(t, "dhcp"), labLink(v4), labBind, "eth0")
	if len(problems) != 0 || len(got) != 1 || got[0].IPv4.Mode != "dhcp" || got[0].IPv6.Mode != "none" || got[0].DNS != nil {
		t.Fatalf("dhcp = %+v, %+v", got, problems)
	}
	if got, _ = ReadNetplan(netplanfixture.Root(t, "dhcp"), labLink(v4, slaac), labBind, "eth0"); got[0].IPv6.Mode != "slaac" {
		t.Fatalf("DHCPv4 with a live SLAAC address = %+v", got[0].IPv6)
	}
}

func TestReadNetplanProblems(t *testing.T) {
	base, _ := os.ReadFile(filepath.Join(netplanfixture.Root(t, "static"), "run/systemd/network/10-netplan-eth0.network"))
	for name, tc := range map[string]struct {
		files   map[string]string
		links   []LinkStatus
		managed []Managed
		key     string
	}{
		"glob match binds two": {nil, append(labLink(v4), LinkStatus{IfIndex: 3, Name: "eth1", LinkType: "ether", LinkAddress: "02:00:00:00:00:02", PermanentAddress: "02:00:00:00:00:02"}),
			append(labBind, Managed{Name: "eth1", NetworkFile: labBind[0].NetworkFile}), "[Match]"},
		"changed MAC":                {nil, []LinkStatus{{IfIndex: 2, Name: "eth0", LinkAddress: "02:00:00:00:00:09", PermanentAddress: labMAC}}, labBind, "MACAddress"},
		"DHCP and static IPv4":       {map[string]string{"10-netplan-eth0.network": strings.Replace(string(base), "[Network]\n", "[Network]\nDHCP=ipv4\n", 1)}, labLink(v4), labBind, "DHCP"},
		"extra route":                {map[string]string{"10-netplan-eth0.network": string(base) + "\n[Route]\nDestination=192.0.2.0/24\nGateway=10.0.0.2\n"}, labLink(v4), labBind, "[Route]"},
		"unknown key":                {map[string]string{"10-netplan-eth0.network": strings.Replace(string(base), "[Network]\n", "[Network]\nVLAN=v6\n", 1)}, labLink(v4), labBind, "VLAN"},
		"routing domain":             {map[string]string{"10-netplan-eth0.network": strings.Replace(string(base), "Domains=lab.example", "Domains=~corp", 1)}, labLink(v4), labBind, "Domains"},
		"netdev":                     {map[string]string{"10-netplan-br0.netdev": "[NetDev]\nName=br0\nKind=bridge\n"}, labLink(v4), labBind, ".netdev"},
		"configures nothing":         {nil, labLink(v4), nil, "configures no interface"},
		"link setting":               {map[string]string{"10-netplan-eth0.link": "[Match]\nPermanentMACAddress=" + labMAC + "\n\n[Link]\nName=eth0\nWakeOnLan=magic\n"}, labLink(v4), labBind, "WakeOnLan"},
		"default route under DHCP":   {map[string]string{"10-netplan-eth0.network": strings.Replace(string(base), "Address=10.0.1.19/16\n", "DHCP=ipv4\n", 1)}, labLink(v4), labBind, "Gateway"},
		"custom DHCP metric":         {map[string]string{"10-netplan-eth0.network": string(base) + "\n[DHCP]\nRouteMetric=500\n"}, labLink(v4), labBind, "RouteMetric"},
		"address without prefix":     {map[string]string{"10-netplan-eth0.network": strings.Replace(string(base), "Address=10.0.1.19/16", "Address=10.0.1.19", 1)}, labLink(v4), labBind, "Address"},
		"IPv4 link-local":            {map[string]string{"10-netplan-eth0.network": strings.Replace(string(base), "LinkLocalAddressing=ipv6", "LinkLocalAddressing=ipv4", 1)}, labLink(v4), labBind, "LinkLocalAddressing"},
		"two IPv4 gateways":          {map[string]string{"10-netplan-eth0.network": string(base) + "\n[Route]\nDestination=0.0.0.0/0\nGateway=10.0.0.2\n"}, labLink(v4), labBind, "gateways"},
		"static IPv6 with RA":        {map[string]string{"10-netplan-eth0.network": strings.Replace(string(base), "[Network]\n", "[Network]\nAddress=2001:db8::5/64\nIPv6AcceptRA=yes\n", 1)}, labLink(v4), labBind, "router advertisements"},
		"static IPv6, RA by default": {map[string]string{"10-netplan-eth0.network": strings.Replace(string(base), "[Network]\n", "[Network]\nAddress=2001:db8::5/64\n", 1)}, labLink(v4, slaac), labBind, "router advertisements"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "run/systemd/network")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"10-netplan-eth0.network": string(base)}
			for k, v := range tc.files {
				files[k] = v
			}
			for k, v := range files {
				if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, problems := ReadNetplan(root, tc.links, tc.managed, "eth0")
			if !slices.ContainsFunc(problems, func(p NetplanProblem) bool {
				return strings.HasPrefix(p.File, "/run/systemd/network/") && strings.Contains(p.Key+" "+p.Message, tc.key)
			}) {
				t.Fatalf("problems = %+v, want one about %q", problems, tc.key)
			}
		})
	}
}

func TestReadNetplanAcrossFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run/systemd/network")
	_ = os.MkdirAll(dir, 0o755)
	for name, mac := range map[string]string{"eth0": labMAC, "eth1": "02:00:00:00:00:02"} {
		body := "[Match]\nPermanentMACAddress=" + mac + "\nName=" + name + "\n\n[Network]\nAddress=10.0.1.19/16\nGateway=10.0.0.1\nDNS=192.0.2.53\n"
		_ = os.WriteFile(filepath.Join(dir, "10-netplan-"+name+".network"), []byte(body), 0o644)
	}
	links := append(labLink(v4), LinkStatus{IfIndex: 3, Name: "eth1", LinkAddress: "02:00:00:00:00:02", PermanentAddress: "02:00:00:00:00:02", Addresses: []AddressStatus{v4}})
	managed := []Managed{{Name: "eth0", NetworkFile: "/run/systemd/network/10-netplan-eth0.network"}, {Name: "eth1", NetworkFile: "/run/systemd/network/10-netplan-eth1.network"}}
	_, problems := ReadNetplan(root, links, managed, "eth0")
	for _, want := range []string{"default route", "DNS"} {
		if !slices.ContainsFunc(problems, func(p NetplanProblem) bool { return strings.Contains(p.Message, want) }) {
			t.Fatalf("problems = %+v, want one about %s", problems, want)
		}
	}
}

func TestReadNetplanAcceptsItsDefaults(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run/systemd/network")
	_ = os.MkdirAll(dir, 0o755)
	body := "[Match]\nPermanentMACAddress=" + labMAC + "\nName=eth0\n\n[Network]\nDHCP=yes\nLinkLocalAddressing=ipv6\n\n[DHCPv4]\nRouteMetric=100\nUseMTU=true\n"
	_ = os.WriteFile(filepath.Join(dir, "10-netplan-eth0.network"), []byte(body), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "10-netplan-eth0.link"), []byte("[Match]\nPermanentMACAddress="+labMAC+"\n\n[Link]\nName=eth0\nWakeOnLan=off\nMTUBytes=9000\n"), 0o644)
	got, problems := ReadNetplan(root, labLink(v4), labBind, "eth0")
	if len(problems) != 0 || len(got) != 1 || got[0].MTU != 9000 || got[0].IPv4.Mode != "dhcp" || got[0].IPv6.Mode != "dhcp6" {
		t.Fatalf("got %+v, problems %+v", got, problems)
	}
}

// Wired 802.1X: netplan writes an ordinary .network file and keeps the credentials beside it.
func TestReadNetplanRefusesWired8021X(t *testing.T) {
	root := netplanfixture.Root(t, "static")
	if err := os.MkdirAll(filepath.Join(root, "run/netplan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run/netplan/wpa-eth0.conf"), []byte("network={}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, problems := ReadNetplan(root, labLink(v4), labBind, "eth0")
	if !slices.ContainsFunc(problems, func(p NetplanProblem) bool { return p.File == "/run/netplan/wpa-eth0.conf" }) {
		t.Fatalf("problems = %+v", problems)
	}
}

package host

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/andaru-project/andaru/internal/host/fixture"
)

const ifupdownMAC = "bc:24:11:38:71:37"

func ifupdownLinks() []LinkStatus {
	return []LinkStatus{{IfIndex: 2, Name: "ens18", LinkType: "ether", MTU: 1500, LinkAddress: ifupdownMAC, PermanentAddress: ifupdownMAC,
		Addresses: []AddressStatus{{Family: "ipv4", Address: "10.0.1.13", PrefixLength: 16, Scope: "global"}}}}
}

var labDUID = []byte{0x00, 0x01, 0x00, 0x01, 0x32, 0x87, 0x03, 0x54, 0xbc, 0x24, 0x11, 0x38, 0x71, 0x37}

func TestReadIfupdownInstallerFiles(t *testing.T) {
	got, problems := ReadIfupdown(fixture.Root(t, "ifupdown-dhcp"), ifupdownLinks(), []string{"ens18"})
	if len(problems) != 0 || len(got) != 1 {
		t.Fatalf("dhcp: %+v, problems %+v", got, problems)
	}
	i := got[0]
	if i.Name != "ens18" || i.PermanentMAC != ifupdownMAC || i.MTU != 1500 || !i.DefaultRoute || i.File != "/etc/network/interfaces" ||
		i.IPv4.Mode != "dhcp" || i.IPv6.Mode != "slaac" || !i.Hotplug || !i.DHCP || i.Identity == nil || !bytes.Equal(i.Identity.DUID, labDUID) || i.Identity.Hostname != "" {
		t.Fatalf("dhcp = %+v identity %+v", i, i.Identity)
	}

	got, problems = ReadIfupdown(fixture.Root(t, "ifupdown-static"), ifupdownLinks(), []string{"ens18"})
	if len(problems) != 0 || len(got) != 1 {
		t.Fatalf("static: %+v, problems %+v", got, problems)
	}
	i = got[0]
	if i.IPv4.Mode != "static" || !slices.Equal(i.IPv4.Addresses, []string{"10.0.1.13/16"}) || i.IPv4.Gateway != "10.0.0.1" || i.IPv6.Mode != "none" || i.DHCP || i.Identity != nil {
		t.Fatalf("static = %+v", i)
	}
}

// edit returns a copy of the DHCP fixture with files replaced (empty content removes the file).
func edit(t *testing.T, base string, files map[string]string) string {
	t.Helper()
	root := fixture.Root(t, base)
	for name, content := range files {
		path := filepath.Join(root, name)
		if content == "" {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const dhcpStanza = "allow-hotplug ens18\niface ens18 inet dhcp\n"

func TestReadIfupdownRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		files      map[string]string
		links      []LinkStatus
		file, code string
	}{
		"pointopoint":   {map[string]string{"etc/network/interfaces": "auto ens18\niface ens18 inet static\n address 10.0.1.13/16\n pointopoint 10.0.0.1\n"}, nil, "/etc/network/interfaces", ""},
		"dns option":    {map[string]string{"etc/network/interfaces": dhcpStanza + " dns-nameservers 192.0.2.53\n"}, nil, "/etc/network/interfaces", ""},
		"mapping":       {map[string]string{"etc/network/interfaces": "mapping ens18\n script /bin/true\n" + dhcpStanza}, nil, "/etc/network/interfaces", ""},
		"pre-up":        {map[string]string{"etc/network/interfaces": dhcpStanza + " pre-up /bin/true\n"}, nil, "/etc/network/interfaces", ""},
		"manual bridge": {map[string]string{"etc/network/interfaces": dhcpStanza + "iface br0 inet manual\n"}, nil, "/etc/network/interfaces", ""},
		"changed MAC": {nil, []LinkStatus{{IfIndex: 2, Name: "ens18", LinkType: "ether", LinkAddress: "02:00:00:00:00:09", PermanentAddress: ifupdownMAC}},
			"/etc/network/interfaces", ""},
		"absent interface":             {map[string]string{"etc/network/interfaces": dhcpStanza + "iface ens19 inet dhcp\n"}, nil, "/etc/network/interfaces", ""},
		"no dhcpcd running":            {map[string]string{"run/dhcpcd/ens18.pid": ""}, nil, "/run/dhcpcd/ens18.pid", "DHCP_IDENTITY_UNKNOWN"},
		"stale pid":                    {map[string]string{"proc/602/comm": "sshd\n"}, nil, "/run/dhcpcd/ens18.pid", "DHCP_IDENTITY_UNKNOWN"},
		"clientid":                     {map[string]string{"etc/dhcpcd.conf": "duid\nclientid\n"}, nil, "/etc/dhcpcd.conf", "DHCP_IDENTITY_UNKNOWN"},
		"iaid":                         {map[string]string{"etc/dhcpcd.conf": "duid\niaid 1\n"}, nil, "/etc/dhcpcd.conf", "DHCP_IDENTITY_UNKNOWN"},
		"anonymous":                    {map[string]string{"etc/dhcpcd.conf": "duid\nanonymous\n"}, nil, "/etc/dhcpcd.conf", "DHCP_IDENTITY_UNKNOWN"},
		"interface block":              {map[string]string{"etc/dhcpcd.conf": "duid\ninterface ens18\n static ip_address=10.0.1.13/16\n"}, nil, "/etc/dhcpcd.conf", "DHCP_IDENTITY_UNKNOWN"},
		"no duid directive":            {map[string]string{"etc/dhcpcd.conf": "hostname\n"}, nil, "/etc/dhcpcd.conf", "DHCP_IDENTITY_UNKNOWN"},
		"no dhcpcd.conf":               {map[string]string{"etc/dhcpcd.conf": ""}, nil, "/etc/dhcpcd.conf", "DHCP_IDENTITY_UNKNOWN"},
		"short DUID":                   {map[string]string{"var/lib/dhcpcd/duid": "00:01\n"}, nil, "/var/lib/dhcpcd/duid", "DHCP_IDENTITY_UNKNOWN"},
		"DUID not hex":                 {map[string]string{"var/lib/dhcpcd/duid": "00:01:zz:01\n"}, nil, "/var/lib/dhcpcd/duid", "DHCP_IDENTITY_UNKNOWN"},
		"no DUID":                      {map[string]string{"var/lib/dhcpcd/duid": ""}, nil, "/var/lib/dhcpcd/duid", "DHCP_IDENTITY_UNKNOWN"},
		"cloud-init file":              {map[string]string{"etc/network/interfaces.d/50-cloud-init": "auto lo\n"}, nil, "/etc/network/interfaces.d/50-cloud-init", ""},
		"dhcpcd.service":               {map[string]string{"etc/systemd/system/multi-user.target.wants/dhcpcd.service": "x"}, nil, "/etc/systemd/system/multi-user.target.wants/dhcpcd.service", ""},
		"NetworkManager":               {map[string]string{"etc/systemd/system/multi-user.target.wants/NetworkManager.service": "x"}, nil, "/etc/systemd/system/multi-user.target.wants/NetworkManager.service", ""},
		"two inet stanzas":             {map[string]string{"etc/network/interfaces": "allow-hotplug ens18\niface ens18 inet static\n address 10.0.1.13/16\niface ens18 inet static\n address 10.0.2.13/16\n"}, nil, "/etc/network/interfaces", ""},
		"neither auto nor hotplug":     {map[string]string{"etc/network/interfaces": "iface ens18 inet dhcp\n"}, nil, "/etc/network/interfaces", ""},
		"hostname with a space":        {map[string]string{"etc/network/interfaces": dhcpStanza + " hostname a b\n"}, nil, "/etc/network/interfaces", ""},
		"hostname with a bad label":    {map[string]string{"etc/network/interfaces": dhcpStanza + " hostname -fw.example\n"}, nil, "/etc/network/interfaces", ""},
		"dhcpcd.conf hostname invalid": {map[string]string{"etc/dhcpcd.conf": "duid\nhostname fw_1\n"}, nil, "/etc/dhcpcd.conf", ""},
		"unknown inet6 method":         {map[string]string{"etc/network/interfaces": dhcpStanza + "iface ens18 inet6 dhcp\n"}, nil, "/etc/network/interfaces", ""},
	} {
		t.Run(name, func(t *testing.T) {
			links := tc.links
			if links == nil {
				links = ifupdownLinks()
			}
			_, problems := ReadIfupdown(edit(t, "ifupdown-dhcp", tc.files), links, nil)
			if !slices.ContainsFunc(problems, func(p Problem) bool { return p.File == tc.file && p.Code == tc.code }) {
				t.Fatalf("problems = %+v, want one at %s with code %q", problems, tc.file, tc.code)
			}
		})
	}
}

func TestReadIfupdownSources(t *testing.T) {
	ok := func(t *testing.T, root string) []IfupdownInterface {
		t.Helper()
		got, problems := ReadIfupdown(root, ifupdownLinks(), nil)
		if len(problems) != 0 {
			t.Fatalf("problems = %+v", problems)
		}
		return got
	}
	// A relative source, a source-directory, a glob matching nothing and a continuation line.
	got := ok(t, edit(t, "ifupdown-dhcp", map[string]string{
		"etc/network/interfaces":            "source interfaces.d/*\nsource-directory more\nsource /etc/network/none-*\n",
		"etc/network/interfaces.d/ens18":    "allow-hotplug \\\n ens18\n",
		"etc/network/more/ens18-v4":         "iface ens18 inet dhcp\n",
		"etc/network/more/skipped.dpkg-old": "iface ens19 inet dhcp\n",
	}))
	if len(got) != 1 || !got[0].Hotplug || got[0].File != "/etc/network/more/ens18-v4" {
		t.Fatalf("got %+v", got)
	}

	refused := func(name string, root string, file string) {
		t.Run(name, func(t *testing.T) {
			_, problems := ReadIfupdown(root, ifupdownLinks(), nil)
			if !slices.ContainsFunc(problems, func(p Problem) bool { return p.File == file }) {
				t.Fatalf("problems = %+v, want one at %s", problems, file)
			}
		})
	}
	root := edit(t, "ifupdown-dhcp", map[string]string{"etc/network/interfaces": "source loop\n", "etc/network/loop": "source interfaces\n"})
	refused("loop", root, "/etc/network/interfaces")

	root = edit(t, "ifupdown-dhcp", map[string]string{"etc/network/interfaces": "source linked\n" + dhcpStanza, "etc/network/real": "auto lo\n"})
	if err := os.Symlink("real", filepath.Join(root, "etc/network/linked")); err != nil {
		t.Fatal(err)
	}
	refused("symlink", root, "/etc/network/linked")

	files := map[string]string{"etc/network/interfaces": "source d1\n" + dhcpStanza}
	for i := 1; i <= 9; i++ {
		files["etc/network/d"+string(rune('0'+i))] = "source d" + string(rune('0'+i+1)) + "\n"
	}
	files["etc/network/d9"] = "auto lo\n"
	refused("nine levels", edit(t, "ifupdown-dhcp", files), "/etc/network/d9")
}

func TestDhcpcdRunning(t *testing.T) {
	root := fixture.Root(t, "ifupdown-dhcp")
	if !DhcpcdRunning(root, "ens18") || DhcpcdRunning(root, "ens19") {
		t.Fatal("pid file reading")
	}
	if err := os.WriteFile(filepath.Join(root, "run/dhcpcd/ens18.pid"), []byte("../602\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if DhcpcdRunning(root, "ens18") {
		t.Fatal("a pid that is no number was followed")
	}
}

// networking.service raises auto interfaces, udev's ifup@ hotplug ones; the hand-back needs to know which.
func TestReadIfupdownAutoIsNotHotplug(t *testing.T) {
	got, problems := ReadIfupdown(edit(t, "ifupdown-dhcp", map[string]string{"etc/network/interfaces": "auto ens18\niface ens18 inet dhcp\n hostname fw\n"}), ifupdownLinks(), nil)
	if len(problems) != 0 || len(got) != 1 || got[0].Hotplug || got[0].Identity.Hostname != "fw" {
		t.Fatalf("got %+v, problems %+v", got, problems)
	}
}

// Without an inet6 stanza ifupdown leaves router advertisements on, so the kernel takes a SLAAC address;
// proposing "none" would silently drop it (found on lab VM 117).
func TestReadIfupdownKeepsKernelSLAAC(t *testing.T) {
	links := ifupdownLinks()
	links[0].Addresses = append(links[0].Addresses, AddressStatus{Family: "ipv6", Address: "2a02:a466:79a7:beef:be24:11ff:fe38:7137", PrefixLength: 64, Scope: "global"})
	got, problems := ReadIfupdown(fixture.Root(t, "ifupdown-static"), links, nil)
	if len(problems) != 0 || len(got) != 1 || got[0].IPv6.Mode != "slaac" {
		t.Fatalf("got %+v, problems %+v", got, problems)
	}
	// A DHCPv6 /128 alone is no SLAAC address.
	links[0].Addresses[1].PrefixLength = 128
	if got, _ := ReadIfupdown(fixture.Root(t, "ifupdown-static"), links, nil); got[0].IPv6.Mode != "none" {
		t.Fatalf("a /128 read as SLAAC: %+v", got[0].IPv6)
	}
}

// Relative sources resolve from the directory of the file naming them, and source-directory paths may be globs.
func TestReadIfupdownSourcePaths(t *testing.T) {
	got, problems := ReadIfupdown(edit(t, "ifupdown-dhcp", map[string]string{
		"etc/network/interfaces":         "source interfaces.d/*\nsource-directory /etc/network/site*\n",
		"etc/network/interfaces.d/ens18": "source extra\n",
		"etc/network/interfaces.d/extra": "allow-hotplug ens18\n",
		"etc/network/sites/ens18-v4":     "iface ens18 inet dhcp\n",
	}), ifupdownLinks(), nil)
	if len(problems) != 0 || len(got) != 1 || !got[0].Hotplug || got[0].File != "/etc/network/sites/ens18-v4" {
		t.Fatalf("got %+v, problems %+v", got, problems)
	}
}

// dhcpcd sends the name dhcpcd.conf gives; networkd must send the same.
func TestReadIfupdownDhcpcdHostname(t *testing.T) {
	got, problems := ReadIfupdown(edit(t, "ifupdown-dhcp", map[string]string{"etc/dhcpcd.conf": "duid\nhostname gateway.example\n"}), ifupdownLinks(), nil)
	if len(problems) != 0 || got[0].Identity == nil || got[0].Identity.Hostname != "gateway.example" {
		t.Fatalf("got %+v, problems %+v", got, problems)
	}
	// The stanza's own hostname wins, as ifup passes it to dhcpcd.
	got, _ = ReadIfupdown(edit(t, "ifupdown-dhcp", map[string]string{"etc/dhcpcd.conf": "duid\nhostname gateway.example\n",
		"etc/network/interfaces": "allow-hotplug ens18\niface ens18 inet dhcp\n hostname fw\n"}), ifupdownLinks(), nil)
	if got[0].Identity.Hostname != "fw" {
		t.Fatalf("got %+v", got[0].Identity)
	}
}

// The stanza's hostname overrides dhcpcd.conf's, so an unusable global one never reaches networkd.
func TestReadIfupdownIgnoresAnOverriddenDhcpcdHostname(t *testing.T) {
	got, problems := ReadIfupdown(edit(t, "ifupdown-dhcp", map[string]string{"etc/dhcpcd.conf": "duid\nhostname fw_1\n",
		"etc/network/interfaces": "allow-hotplug ens18\niface ens18 inet dhcp\n hostname fw\n"}), ifupdownLinks(), nil)
	if len(problems) != 0 || got[0].Identity.Hostname != "fw" {
		t.Fatalf("got %+v, problems %+v", got, problems)
	}
}

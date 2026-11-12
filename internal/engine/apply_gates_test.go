package engine

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

const (
	uplinkFile  = "/run/systemd/network/50-andaru-eth0.network"
	netplanFile = "/run/systemd/network/10-netplan-eth0.network"
	vlanNetdev  = "/run/systemd/network/50-andaru-vl000000020000.netdev"
)

func global(name, kind string) host.LinkStatus {
	return host.LinkStatus{Name: name, Kind: kind, Addresses: []host.AddressStatus{{Family: "ipv4", Address: "10.0.1.19", PrefixLength: 20, Scope: "global"}}}
}

func foreign(paths ...string) []render.Finding {
	var out []render.Finding
	for _, p := range paths {
		out = append(out, render.Finding{Severity: "error", Code: "FOREIGN_CONFIGURATION", Message: "A network configuration file not written by Andaru would override or conflict with it: " + p})
	}
	return out
}

// lab is the cloud image being taken over with its one NIC declared as a DHCP uplink.
func lab() gateInput {
	netplan := []string{"/run/systemd/network/10-netplan-eth0.link", netplanFile}
	return gateInput{
		Takeover:    true,
		Declaration: []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[{"addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"none"}}}]}`),
		Result:      render.Result{Files: []render.File{{Path: uplinkFile, Content: "[Match]\n"}}, Findings: foreign(netplan...)},
		Links:       []host.LinkStatus{global("eth0", "")},
		Managed:     []host.Managed{{Name: "eth0", NetworkFile: netplanFile, State: "configured"}},
		Facts:       host.RealizerFacts{ForeignFiles: netplan},
		Baseline:    host.Baseline{Netplan: true, BootUnit: true},
	}
}

// taken is the same host after a confirmed takeover.
func taken() gateInput {
	in := lab()
	in.Takeover, in.Facts.ForeignFiles, in.Result.Findings = false, nil, nil
	in.Managed[0].NetworkFile = uplinkFile
	in.Live = &generation.Generation{}
	in.LiveFiles = map[string][]byte{uplinkFile: []byte("[Match]\n")}
	return in
}

func TestApplyFindings(t *testing.T) {
	type want struct{ code, path string }
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPIDocument, &contract); err != nil {
		t.Fatal(err)
	}
	for name, row := range map[string]struct {
		in   func() gateInput
		want []want
	}{
		"lab takeover":         {lab, nil},
		"apply after takeover": {taken, nil},
		"empty declaration with takeover": {func() gateInput {
			in := lab()
			in.Declaration, in.Result.Files = []byte(`{"links":[],"segments":[]}`), nil
			return in
		}, []want{{"WOULD_UNCONFIGURE", "eth0"}}},
		"address on IPv6 only": {func() gateInput {
			in := lab()
			in.Result.Files = nil
			in.Links[0].Addresses = []host.AddressStatus{{Family: "ipv6", Address: "2001:db8::1", PrefixLength: 64, Scope: "global"}}
			return in
		}, []want{{"WOULD_UNCONFIGURE", "eth0"}}},
		"link-local only is not connectivity": {func() gateInput {
			in := lab()
			in.Result.Files = nil
			in.Links[0].Addresses = []host.AddressStatus{{Family: "ipv6", Address: "fe80::1", PrefixLength: 64, Scope: "link"}}
			return in
		}, nil},
		"container bridge networkd does not manage": {func() gateInput {
			in := lab()
			in.Links = append(in.Links, global("podman0", "bridge"), global("tailscale0", ""))
			return in
		}, nil},
		"VLAN of the live generation removed": {func() gateInput {
			in := taken()
			in.Links = append(in.Links, global("vl000000020000", "vlan"))
			in.Managed = append(in.Managed, host.Managed{Name: "vl000000020000", NetworkFile: "/run/systemd/network/50-andaru-vl000000020000.network"})
			in.LiveFiles[vlanNetdev] = []byte("[NetDev]\nKind=vlan\n\n[VLAN]\nId=6\n")
			return in
		}, nil},
		"renderer error passes through": {func() gateInput {
			in := lab()
			in.Result.Findings = append(in.Result.Findings, render.Finding{Severity: "error", Code: "HARDWARE_MISSING", Path: "/links/0/permanentMac", Message: "No interface has this address."}, render.Finding{Severity: "warning", Code: "RA_PREFIX_UNSUPPORTED", Path: "/segments/0", Message: "ignored"})
			return in
		}, []want{{"HARDWARE_MISSING", "/links/0/permanentMac"}}},
		"takeover with nothing to take over": {func() gateInput {
			in := taken()
			in.Takeover = true
			return in
		}, []want{{"TAKEOVER_MISMATCH", ""}}},
		"netplan still in place without takeover": {func() gateInput {
			in := lab()
			in.Takeover = false
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_MISMATCH", ""}}},
		"foreign file in /etc": {func() gateInput {
			in := lab()
			in.Facts.ForeignFiles = append(in.Facts.ForeignFiles, "/etc/systemd/network/x.network")
			in.Result.Findings = foreign(in.Facts.ForeignFiles...)
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", "/etc/systemd/network/x.network"}}},
		"drop-in on a netplan file": {func() gateInput {
			in := lab()
			in.Facts.ForeignFiles = append(in.Facts.ForeignFiles, netplanFile+".d/override.conf")
			in.Result.Findings = foreign(in.Facts.ForeignFiles...)
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", netplanFile + ".d/override.conf"}}},
		"no netplan": {func() gateInput {
			in := lab()
			in.Baseline.Netplan = false
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", "/usr/sbin/netplan"}}},
		"ifupdown with takeover": {func() gateInput {
			in := lab()
			in.Baseline.Ifupdown = true
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", "/sbin/ifup"}}},
		"ifupdown without takeover": {func() gateInput {
			in := taken()
			in.Baseline.Ifupdown = true
			return in
		}, []want{{"TAKEOVER_UNSUPPORTED_BASELINE", "/sbin/ifup"}}},
		"stray netplan YAML": {func() gateInput {
			in := lab()
			in.Baseline.StrayYAML = []string{"/run/netplan/x.yaml"}
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", "/run/netplan/x.yaml"}}},
		"static DNS declared for the interface netplan's file configures": {func() gateInput {
			return staticDNS(map[string]string{"eth0": "DNS=192.0.2.53\n"}, "eth0")
		}, nil},
		"static DNS declared on another interface only": {func() gateInput {
			return staticDNS(map[string]string{"eth0": "", "eth1": "DNS=192.0.2.53\n"}, "eth0")
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", netplanWAN}}},
		"static DNS file configures two interfaces, one declared": {func() gateInput {
			return staticDNS(map[string]string{"eth0": "DNS=192.0.2.53\n", "eth1": ""}, "eth0", "eth1")
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", netplanWAN}}},
		"static DNS file configures no interface": {func() gateInput {
			return staticDNS(map[string]string{"eth0": "DNS=192.0.2.53\n"})
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", netplanWAN}}},
		"only UseDNS mentions DNS": {func() gateInput {
			return staticDNS(map[string]string{"eth0": "[DHCPv4]\nUseDNS=no\n"}, "eth0")
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", netplanWAN}}},
		"static DNS in the baseline": {func() gateInput {
			in := lab()
			in.Baseline.StaticDNS = []string{netplanFile}
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", netplanFile}}},
		"netplan created a device": {func() gateInput {
			in := lab()
			in.Facts.ForeignFiles = append(in.Facts.ForeignFiles, "/run/systemd/network/10-netplan-br0.netdev")
			in.Result.Findings = foreign(in.Facts.ForeignFiles...)
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", "/run/systemd/network/10-netplan-br0.netdev"}}},
		"boot unit not enabled": {func() gateInput {
			in := taken()
			in.Baseline.BootUnit = false
			return in
		}, []want{{"BOOT_UNIT_DISABLED", "andaru-network-boot.service"}}},
		"forwarding and PPP": {func() gateInput {
			in := taken()
			in.Declaration = []byte(`{"links":[{"kind":"ethernet","enabled":true},{"kind":"pppoe","enabled":true},{"kind":"pppoe","enabled":false}],
				"segments":[{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"delegated"}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"enabled":true}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"enabled":false}}}}]}`)
			return in
		}, []want{{"APPLY_UNSUPPORTED", "/links/1"}, {"APPLY_UNSUPPORTED", "/segments/0/addressing/ipv6/mode"}, {"APPLY_UNSUPPORTED", "/segments/1/addressing/ipv6/ra/enabled"}}},
		"existing device would change": {func() gateInput {
			in := taken()
			in.LiveFiles[vlanNetdev] = []byte("[NetDev]\nKind=vlan\n\n[VLAN]\nId=6\n")
			in.Result.Files = append(in.Result.Files, render.File{Path: vlanNetdev, Content: "[NetDev]\nKind=vlan\n\n[VLAN]\nId=7\n"})
			return in
		}, []want{{"NETDEV_CHANGE_UNSUPPORTED", vlanNetdev}}},
		"existing device unchanged, network file changed": {func() gateInput {
			in := taken()
			in.LiveFiles[vlanNetdev] = []byte("[NetDev]\nKind=vlan\n")
			in.Result.Files = []render.File{{Path: uplinkFile, Content: "[Match]\nchanged\n"}, {Path: vlanNetdev, Content: "[NetDev]\nKind=vlan\n"}}
			return in
		}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var got []want
			for _, f := range applyFindings(row.in()) {
				if f.Severity != "error" || f.Message == "" {
					t.Fatalf("finding %+v must be an explained error", f)
				}
				// A client validating against the served contract must not reject a refused apply.
				if !slices.Contains(contract.Components.Schemas["ApplyFinding"].Properties["code"].Enum, f.Code) {
					t.Fatalf("%s is not a code the contract's ApplyFinding admits", f.Code)
				}
				got = append(got, want{f.Code, f.Path})
			}
			if !slices.Equal(got, row.want) {
				t.Fatalf("findings = %v\nwant %v", got, row.want)
			}
		})
	}
}

// netplanWAN is named after netplan's own id for the interface, which need not be the kernel's name.
const netplanWAN = "/run/systemd/network/10-netplan-wan.network"

// staticDNS is a netplan host whose one generated file sets DNS servers. rendered gives the content of
// Andaru's file per interface; bound lists the interfaces networkd has bound to netplan's file.
func staticDNS(rendered map[string]string, bound ...string) gateInput {
	in := lab()
	in.Facts.ForeignFiles = []string{"/run/systemd/network/10-netplan-wan.link", netplanWAN}
	in.Result.Findings = foreign(in.Facts.ForeignFiles...)
	in.Baseline.StaticDNS = []string{netplanWAN}
	in.Result.Files, in.Managed, in.Links = nil, nil, nil
	for _, name := range slices.Sorted(maps.Keys(rendered)) {
		in.Result.Files = append(in.Result.Files, render.File{Path: networkDir + "50-andaru-" + name + ".network", Content: "[Match]\n\n[Network]\n" + rendered[name]})
	}
	for _, name := range bound {
		in.Managed = append(in.Managed, host.Managed{Name: name, NetworkFile: netplanWAN, State: "configured"})
		in.Links = append(in.Links, global(name, ""))
	}
	return in
}

func TestStaticDNSRefusalSaysWhatToDeclare(t *testing.T) {
	findings := applyFindings(staticDNS(map[string]string{"eth0": ""}, "eth0"))
	if !slices.ContainsFunc(findings, func(f render.Finding) bool {
		return f.Code == "TAKEOVER_UNSUPPORTED_BASELINE" && strings.Contains(f.Message, `"dns"`) && strings.Contains(f.Message, "eth0")
	}) {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestStaticDNSGateLooksOnlyAtTheFilesInterfaces(t *testing.T) {
	in := staticDNS(map[string]string{"eth0": "DNS=192.0.2.53\n", "eth1": ""}, "eth0")
	// eth1 is configured by another netplan file that sets no DNS; it owes nothing.
	in.Managed = append(in.Managed, host.Managed{Name: "eth1", NetworkFile: "/run/systemd/network/10-netplan-lan.network", State: "configured"})
	in.Links = append(in.Links, global("eth1", ""))
	if findings := applyFindings(in); len(findings) != 0 {
		t.Fatalf("findings = %+v", findings)
	}
	// One file for several interfaces cannot be satisfied while only one segment may declare DNS: say so.
	findings := applyFindings(staticDNS(map[string]string{"eth0": "DNS=192.0.2.53\n", "eth1": ""}, "eth0", "eth1"))
	if !slices.ContainsFunc(findings, func(f render.Finding) bool {
		return f.Code == "TAKEOVER_UNSUPPORTED_BASELINE" && strings.Contains(f.Message, "several interfaces") && !strings.Contains(f.Message, "Declare")
	}) {
		t.Fatalf("findings = %+v", findings)
	}
}

const debianFile = "/run/systemd/network/50-andaru-ens18.network"

// debian is a host the Debian installer set up (ifupdown, dhcpcd) being taken over with its one NIC as a DHCP uplink.
func debian() gateInput {
	return gateInput{
		Takeover:    true,
		Declaration: []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[{"addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"slaac"}}}]}`),
		Result:      render.Result{Files: []render.File{{Path: debianFile, Content: "[Match]\n"}}},
		Links:       []host.LinkStatus{global("ens18", "")},
		Managed:     []host.Managed{},
		Facts:       host.RealizerFacts{ForeignFiles: []string{}, ResolvedInCharge: true},
		Baseline:    host.Baseline{Ifupdown: true, BootUnit: true},
		Ifupdown:    []host.IfupdownInterface{{Detected: host.Detected{Name: "ens18", IPv4: host.Addressing{Mode: "dhcp"}, IPv6: host.Addressing{Mode: "slaac"}}, Hotplug: true, DHCP: true}},
	}
}

func TestIfupdownGates(t *testing.T) {
	type want struct{ code, path string }
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPIDocument, &contract); err != nil {
		t.Fatal(err)
	}
	static := func(dns bool) func() gateInput {
		return func() gateInput {
			in := debian()
			in.Declaration = []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[{"addressing":{"ipv4":{"mode":"static"},"ipv6":{"mode":"none"}}}]}`)
			if dns {
				in.Declaration = []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[{"addressing":{"ipv4":{"mode":"static"},"ipv6":{"mode":"none"},"dns":{"servers":["192.0.2.53"]}}}]}`)
			}
			in.CurrentDNS = []string{"192.0.2.53"}
			return in
		}
	}
	for name, row := range map[string]struct {
		in   func() gateInput
		want []want
	}{
		"recognised": {debian, nil},
		// The DNS service resolves for the host, so it declares the host's DNS.
		"static with the DNS service": {func() gateInput {
			in := static(false)()
			in.Declaration = []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[{"addressing":{"ipv4":{"mode":"static"},"ipv6":{"mode":"none"}}}],"dns":{"mode":"recursive","segments":[]}}`)
			return in
		}, nil},
		"reader refusal": {func() gateInput {
			in := debian()
			in.IfupdownProblems = []host.Problem{{File: "/etc/network/interfaces", Key: "pre-up", Message: "no"}}
			return in
		}, []want{{"TAKEOVER_UNSUPPORTED_BASELINE", "/etc/network/interfaces"}}},
		"identity unknown": {func() gateInput {
			in := debian()
			in.IfupdownProblems = []host.Problem{{File: "/etc/dhcpcd.conf", Code: "DHCP_IDENTITY_UNKNOWN", Message: "no"}}
			return in
		}, []want{{"DHCP_IDENTITY_UNKNOWN", "/etc/dhcpcd.conf"}}},
		"resolved not in charge": {func() gateInput {
			in := debian()
			in.Facts.ResolvedInCharge = false
			return in
		}, []want{{"RESOLVED_NOT_IN_CHARGE", "/etc/resolv.conf"}}},
		"networkd configures a link": {func() gateInput {
			in := debian()
			in.Managed = []host.Managed{{Name: "ens19", NetworkFile: "/usr/lib/systemd/network/80-container.network", State: "configured"}}
			return in
		}, []want{{"TAKEOVER_UNSUPPORTED_BASELINE", "/usr/lib/systemd/network/80-container.network"}}},
		"stanza interface down and undeclared": {func() gateInput {
			in := debian()
			in.Ifupdown = append(in.Ifupdown, host.IfupdownInterface{Detected: host.Detected{Name: "ens19", IPv4: host.Addressing{Mode: "dhcp"}, IPv6: host.Addressing{Mode: "none"}}, DHCP: true})
			return in
		}, []want{{"WOULD_UNCONFIGURE", "ens19"}}},
		"all static without dns": {static(false), []want{{"DNS_REQUIRED", ""}}},
		// Servers offered by DHCP alone are enough; the recognised row has router advertisements as well.
		"dns from dhcp": {func() gateInput {
			in := static(false)()
			in.Declaration = []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[{"addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"none"}}}]}`)
			return in
		}, nil},
		"dns from dhcp6": {func() gateInput {
			in := static(false)()
			in.Declaration = []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[{"addressing":{"ipv4":{"mode":"static"},"ipv6":{"mode":"dhcp6"}}}]}`)
			return in
		}, nil},
		"leftover netplan file": {func() gateInput {
			in := debian()
			in.Facts.ForeignFiles = []string{"/run/systemd/network/10-netplan-ens18.network"}
			in.Result.Findings = foreign(in.Facts.ForeignFiles...)
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}, {"TAKEOVER_UNSUPPORTED_BASELINE", "/run/systemd/network/10-netplan-ens18.network"}}},
		"dhcp only on a disabled link": {func() gateInput {
			in := static(false)()
			in.Declaration = []byte(`{"links":[{"id":"a","kind":"ethernet","enabled":true},{"id":"b","kind":"ethernet","enabled":false}],"segments":[{"link":"a","addressing":{"ipv4":{"mode":"static"},"ipv6":{"mode":"none"}}},{"link":"b","addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"none"}}}]}`)
			return in
		}, []want{{"DNS_REQUIRED", ""}}},
		"generation already live": {func() gateInput {
			// A confirmation that failed before masking ifupdown leaves it unmasked; Andaru already runs the host.
			in := debian()
			in.Live = &generation.Generation{}
			in.Managed = []host.Managed{{Name: "ens18", NetworkFile: debianFile, State: "configured"}}
			return in
		}, []want{{"TAKEOVER_MISMATCH", ""}}},
		"all static with dns": {static(true), nil},
		"without takeover": {func() gateInput {
			in := debian()
			in.Takeover = false
			return in
		}, []want{{"TAKEOVER_MISMATCH", ""}}},
		"after a confirmed takeover": {func() gateInput {
			in := debian()
			in.Takeover, in.Baseline.IfupdownMasked, in.Ifupdown = false, true, nil
			in.Managed = []host.Managed{{Name: "ens18", NetworkFile: debianFile, State: "configured"}}
			return in
		}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var got []want
			for _, f := range applyFindings(row.in()) {
				if !slices.Contains(contract.Components.Schemas["ApplyFinding"].Properties["code"].Enum, f.Code) {
					t.Fatalf("%s is not a code the contract's ApplyFinding admits", f.Code)
				}
				got = append(got, want{f.Code, f.Path})
			}
			if !slices.Equal(got, row.want) {
				t.Fatalf("findings = %v\nwant %v", got, row.want)
			}
		})
	}
	findings := applyFindings(static(false)())
	if !strings.Contains(findings[0].Message, "192.0.2.53") {
		t.Fatalf("DNS_REQUIRED does not list the servers in use: %s", findings[0].Message)
	}
	if findings := applyFindings(func() gateInput { in := debian(); in.Takeover = false; return in }()); !strings.Contains(findings[0].Message, "ifupdown still configures") {
		t.Fatalf("mismatch message: %s", findings[0].Message)
	}
}

// With the DNS service the host resolves through it, so netplan's DNS servers owe no segment a declaration.
func TestStaticDNSSatisfiedByTheDNSService(t *testing.T) {
	in := staticDNS(map[string]string{"eth0": ""}, "eth0")
	in.Result.Files = append(in.Result.Files, render.File{Path: "/run/systemd/resolved.conf.d/50-andaru.conf", Content: "[Resolve]\nDNS=127.0.0.1 ::1\n"})
	if findings := applyFindings(in); len(findings) != 0 {
		t.Fatalf("findings = %+v", findings)
	}
}

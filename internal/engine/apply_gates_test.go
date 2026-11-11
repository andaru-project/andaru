package engine

import (
	"slices"
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
				got = append(got, want{f.Code, f.Path})
			}
			if !slices.Equal(got, row.want) {
				t.Fatalf("findings = %v\nwant %v", got, row.want)
			}
		})
	}
}

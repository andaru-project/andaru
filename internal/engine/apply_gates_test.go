package engine

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

type openAPIStringEnum []string

func (e *openAPIStringEnum) UnmarshalJSON(raw []byte) error {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		*e = nil
		return nil
	}
	*e = values
	return nil
}

const (
	uplinkFile = "/run/systemd/network/50-andaru-eth0.network"
	vlanNetdev = "/run/systemd/network/50-andaru-vl000000020000.netdev"
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

// plain is the image's first apply: its one NIC, which the factory file configures now, declared as a DHCP uplink.
func plain() gateInput {
	return gateInput{
		Declaration: []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[{"addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"none"}}}]}`),
		Result:      render.Result{Files: []render.File{{Path: uplinkFile, Content: "[Match]\n"}}},
		Links:       []host.LinkStatus{global("eth0", "")},
		Managed:     []host.Managed{{Name: "eth0", NetworkFile: factoryFile, State: "configured"}},
		BootUnit:    true,
	}
}

// taken is the same host after a confirmed apply.
func taken() gateInput {
	in := plain()
	in.Managed[0].NetworkFile = uplinkFile
	in.LiveFiles = map[string][]byte{uplinkFile: []byte("[Match]\n")}
	return in
}

func TestApplyFindings(t *testing.T) {
	type want struct{ code, path string }
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum openAPIStringEnum `json:"enum"`
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
		"first apply":                 {plain, nil},
		"apply after a confirmed one": {taken, nil},
		"empty declaration": {func() gateInput {
			in := plain()
			in.Declaration, in.Result.Files = []byte(`{"links":[],"segments":[]}`), nil
			return in
		}, []want{{"WOULD_UNCONFIGURE", "eth0"}}},
		"address on IPv6 only": {func() gateInput {
			in := plain()
			in.Result.Files = nil
			in.Links[0].Addresses = []host.AddressStatus{{Family: "ipv6", Address: "2001:db8::1", PrefixLength: 64, Scope: "global"}}
			return in
		}, []want{{"WOULD_UNCONFIGURE", "eth0"}}},
		// The image's first apply: the catch-all would down a link the factory file configures and the declaration omits.
		"factory link left out": {func() gateInput {
			in := taken()
			in.LiveFiles, in.Result.Files = nil, nil
			in.Managed[0].NetworkFile = factoryFile
			return in
		}, []want{{"WOULD_UNCONFIGURE", "eth0"}}},
		"factory link declared": {func() gateInput {
			in := taken()
			in.LiveFiles = nil
			in.Managed[0].NetworkFile = factoryFile
			return in
		}, nil},
		"link-local only is not connectivity": {func() gateInput {
			in := plain()
			in.Result.Files = nil
			in.Links[0].Addresses = []host.AddressStatus{{Family: "ipv6", Address: "fe80::1", PrefixLength: 64, Scope: "link"}}
			return in
		}, nil},
		"container bridge networkd does not manage": {func() gateInput {
			in := plain()
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
			in := plain()
			in.Result.Findings = append(in.Result.Findings, render.Finding{Severity: "error", Code: "HARDWARE_MISSING", Path: "/links/0/permanentMac", Message: "No interface has this address."}, render.Finding{Severity: "warning", Code: "RA_PREFIX_UNSUPPORTED", Path: "/segments/0", Message: "ignored"})
			return in
		}, []want{{"HARDWARE_MISSING", "/links/0/permanentMac"}}},
		"foreign configuration": {func() gateInput {
			in := plain()
			in.Result.Findings = foreign("/etc/systemd/network/x.network")
			return in
		}, []want{{"FOREIGN_CONFIGURATION", ""}}},
		"boot unit not enabled": {func() gateInput {
			in := taken()
			in.BootUnit = false
			return in
		}, []want{{"BOOT_UNIT_DISABLED", "andaru-network-boot.service"}}},
		"forwarding and PPP": {func() gateInput {
			in := taken()
			in.Declaration = []byte(`{"management":{"segment":"seg"},"links":[{"kind":"ethernet","enabled":true},{"kind":"pppoe","enabled":true},{"kind":"pppoe","enabled":false}],
				"segments":[{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"delegated","ra":{"mode":"unmanaged"}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"enabled":true,"managed":false,"otherInfo":false}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"enabled":false,"managed":true,"otherInfo":true}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"mode":"router"}}}}]}`)
			return in
		}, nil},
		// Without the filter IPv6 forwarding stays off, so an advertised default router would drop everything.
		"advertisements without the filter": {func() gateInput {
			in := taken()
			in.Declaration = []byte(`{"links":[{"kind":"ethernet","enabled":true}],"segments":[
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"mode":"unmanaged"}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"delegated"}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"mode":"disabled"}}}}]}`)
			return in
		}, []want{{"APPLY_UNSUPPORTED", "/segments/0/addressing/ipv6/ra/mode"}, {"APPLY_UNSUPPORTED", "/segments/1/addressing/ipv6/mode"}}},
		"advertisement modes needing the DHCPv6 server": {func() gateInput {
			in := taken()
			in.Declaration = []byte(`{"management":{"segment":"seg"},"links":[{"kind":"ethernet","enabled":true}],"segments":[
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"mode":"managed"}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"mode":"assisted"}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"delegated","ra":{"mode":"stateless"}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"enabled":true,"managed":true,"otherInfo":false}}}},
				{"addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"enabled":true,"managed":false,"otherInfo":true}}}}]}`)
			return in
		}, []want{{"APPLY_UNSUPPORTED", "/segments/0/addressing/ipv6/ra/mode"}, {"APPLY_UNSUPPORTED", "/segments/1/addressing/ipv6/ra/mode"}, {"APPLY_UNSUPPORTED", "/segments/2/addressing/ipv6/ra/mode"}, {"APPLY_UNSUPPORTED", "/segments/3/addressing/ipv6/ra/mode"}, {"APPLY_UNSUPPORTED", "/segments/4/addressing/ipv6/ra/mode"}}},
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

func TestDHCPv6Gates(t *testing.T) {
	gate := func(declaration string) []string {
		in := taken()
		in.Declaration = []byte(declaration)
		var got []string
		for _, f := range applyFindings(in) {
			got = append(got, f.Code+" "+f.Path)
		}
		return got
	}
	segs := `"segments":[{"id":"seg_a","addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static","ra":{"mode":"managed"}}}}]`
	if got := gate(`{"management":{"segment":"seg_a"},"links":[{"kind":"ethernet","enabled":true}],` + segs + `,"dhcp6":[{"segment":"seg_a"}]}`); len(got) != 0 {
		t.Fatalf("managed with a DHCPv6 entry: %v", got)
	}
	if got := gate(`{"management":{"segment":"seg_a"},"links":[{"kind":"ethernet","enabled":true}],` + segs + `}`); !slices.Equal(got, []string{"APPLY_UNSUPPORTED /segments/0/addressing/ipv6/ra/mode"}) {
		t.Fatalf("managed without a DHCPv6 entry: %v", got)
	}
	plain := `"segments":[{"id":"seg_a","addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"static"}}}]`
	if got := gate(`{"links":[{"kind":"ethernet","enabled":true}],` + plain + `,"dhcp6":[{"segment":"seg_a","prefixDelegation":{"prefix":"fd00:beef::/48","delegatedLength":56}}]}`); !slices.Equal(got, []string{"APPLY_UNSUPPORTED /dhcp6/0/prefixDelegation"}) {
		t.Fatalf("delegation without the filter: %v", got)
	}
}

func TestVirtualDNSGate(t *testing.T) {
	gate := func(declaration string) []render.Finding {
		in := taken()
		in.Declaration = []byte(declaration)
		return applyFindings(in)
	}
	plain := `"links":[{"kind":"ethernet","enabled":true}],"segments":[{"id":"seg_a","addressing":{"ipv4":{"mode":"none"},"ipv6":{"mode":"none"}}}]`
	dns := `"dns":{"mode":"recursive","segments":[],"virtualAddresses":["vip_a"]}`
	got := gate(`{` + plain + `,` + dns + `}`)
	if len(got) != 1 || got[0].Path != "/dns/virtualAddresses" || got[0].Message != "The DNS service on virtual addresses needs the packet filter, which needs a management segment. Set management.segment." {
		t.Fatalf("without the filter: %+v", got)
	}
	if got := gate(`{"management":{"segment":"seg_a"},` + plain + `,` + dns + `}`); len(got) != 0 {
		t.Fatalf("with the filter: %+v", got)
	}
}

func TestProxyARPGate(t *testing.T) {
	gate := func(declaration string) []render.Finding {
		in := taken()
		in.Declaration = []byte(declaration)
		return applyFindings(in)
	}
	plain := `"links":[{"kind":"ethernet","enabled":true}],"segments":[{"id":"seg_a","addressing":{"ipv4":{"mode":"static","addresses":["203.0.113.2/24"]},"ipv6":{"mode":"none"}}}]`
	vips := `"virtualAddresses":[{"address":"172.16.53.53"},{"kind":"proxyArp","segment":"seg_a","address":"203.0.113.50"}]`
	got := gate(`{` + plain + `,` + vips + `}`)
	if len(got) != 1 || got[0].Code != "APPLY_UNSUPPORTED" || got[0].Path != "/virtualAddresses/1/kind" || got[0].Message != "Proxy ARP entries need the packet filter, which needs a management segment. Set management.segment." {
		t.Fatalf("without the filter: %+v", got)
	}
	if got := gate(`{"management":{"segment":"seg_a"},` + plain + `,` + vips + `}`); len(got) != 0 {
		t.Fatalf("with the filter: %+v", got)
	}
}

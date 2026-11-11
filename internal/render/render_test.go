package render

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

//go:embed testdata
var testdata embed.FS

var update = flag.Bool("update", false, "rewrite golden files")

var allFacts = host.RealizerFacts{PPPDaemon: true, PPPoEPlugin: true, PPPDevice: true, ResolvedInCharge: true}

type observed struct{ Name, Kind, MAC string }

func links(rows ...observed) []host.LinkStatus {
	out := make([]host.LinkStatus, len(rows))
	for i, row := range rows {
		out[i] = host.LinkStatus{IfIndex: i + 2, Name: row.Name, LinkType: "ether", Kind: row.Kind, LinkAddress: row.MAC, PermanentAddress: row.MAC}
	}
	return out
}

func fixture(t *testing.T, name string) Input {
	t.Helper()
	declaration, err := testdata.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := testdata.ReadFile("testdata/" + name + ".links.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []observed
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return Input{Declaration: declaration, Links: links(rows...), Facts: allFacts, Secrets: map[string]string{"WAN_PASSWORD": "kpn"}}
}

func render(t *testing.T, in Input) Result {
	t.Helper()
	result, err := Network(in)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// golden is everything a reviewer must read: real file bodies, resolutions and findings.
func golden(result Result) string {
	var b strings.Builder
	for _, f := range result.Files {
		fmt.Fprintf(&b, "### %s %s secret=%v sources=%s\n%s\n", f.Path, f.Mode, f.Secret, strings.Join(f.Sources, ","), f.Real())
		if f.Secret {
			fmt.Fprintf(&b, "### %s (as shown)\n%s\n", f.Path, f.Content)
		}
	}
	for _, r := range result.Resolved {
		fmt.Fprintf(&b, "### resolved %s -> %s\n", r.Link, r.Interface)
	}
	for _, f := range result.Findings {
		fmt.Fprintf(&b, "### %s %s %q %s\n", f.Severity, f.Code, f.Path, f.Message)
	}
	fmt.Fprintf(&b, "### realizable=%v\n", result.Realizable)
	return b.String()
}

func TestNetworkGolden(t *testing.T) {
	names, err := fs.Glob(testdata, "testdata/*.links.json")
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, path := range names {
		name := strings.TrimSuffix(filepath.Base(path), ".links.json")
		t.Run(name, func(t *testing.T) {
			got := golden(render(t, fixture(t, name)))
			file := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := testdata.ReadFile("testdata/" + name + ".golden")
			if err != nil || got != string(want) {
				t.Fatalf("golden mismatch (%v); run go test -update and review the diff.\n%s", err, got)
			}
		})
	}
}

func change(t *testing.T, in Input, edit func(d map[string]any)) Input {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(in.Declaration, &d); err != nil {
		t.Fatal(err)
	}
	edit(d)
	in.Declaration, _ = json.Marshal(d)
	return in
}
func item(d map[string]any, collection string, i int) map[string]any {
	return d[collection].([]any)[i].(map[string]any)
}

func TestNetworkIsDeterministic(t *testing.T) {
	for _, name := range []string{"ethernet-uplink", "bridge", "bond"} {
		in := fixture(t, name)
		bodies := func(r Result) (out []string) {
			for _, f := range r.Files {
				out = append(out, f.Path+"\n"+f.Real())
			}
			for _, f := range r.Findings {
				out = append(out, f.Severity+f.Code+f.Message)
			}
			slices.Sort(out)
			return
		}
		first := render(t, in)
		again, _ := json.Marshal(render(t, in))
		if once, _ := json.Marshal(first); string(once) != string(again) {
			t.Fatal("same input rendered differently")
		}
		for range 20 {
			shuffled := change(t, in, func(d map[string]any) {
				for _, key := range []string{"links", "segments", "zones"} {
					list := d[key].([]any)
					rand.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
				}
			})
			if got := bodies(render(t, shuffled)); !slices.Equal(got, bodies(first)) {
				t.Fatalf("%s: declaration order changed the output", name)
			}
		}
	}
}

func TestResolveIgnoresVirtualLinksSharingTheMAC(t *testing.T) {
	in := fixture(t, "ethernet-uplink")
	in.Links = append(in.Links, links(observed{"vl6", "vlan", "52:54:00:aa:bb:01"}, observed{"br0", "bridge", "52:54:00:aa:bb:01"})...)
	result := render(t, in)
	if len(result.Findings) != 0 || result.Resolved[0].Interface != "ens18" {
		t.Fatalf("a VLAN and bridge inheriting the NIC's MAC confused resolution: %+v %+v", result.Findings, result.Resolved)
	}
}

func hasFinding(r Result, severity, code, path string) bool {
	return slices.ContainsFunc(r.Findings, func(f Finding) bool { return f.Severity == severity && f.Code == code && f.Path == path })
}
func hasFile(r Result, suffix string) bool {
	return slices.ContainsFunc(r.Files, func(f File) bool { return strings.HasSuffix(f.Path, suffix) })
}

func TestFindings(t *testing.T) {
	base := fixture(t, "bridge")
	cases := []struct {
		name, severity, code, path string
		in                         func() Input
	}{
		{"missing NIC", "error", "HARDWARE_MISSING", "/links/0/permanentMac", func() Input { in := base; in.Links = in.Links[1:]; return in }},
		{"two NICs with one MAC", "error", "HARDWARE_AMBIGUOUS", "/links/0/permanentMac", func() Input {
			in := base
			in.Links = append(slices.Clone(in.Links), links(observed{"ens20", "", "52:54:00:aa:bb:01"})...)
			return in
		}},
		{"foreign interface holds the derived name", "error", "NAME_COLLISION", "/links/3", func() Input {
			in := base
			in.Links = append(slices.Clone(in.Links), links(observed{"br000000040000", "dummy", "02:00:00:00:00:09"})...)
			return in
		}},
		{"foreign file", "error", "FOREIGN_CONFIGURATION", "", func() Input {
			in := base
			in.Facts.ForeignFiles = []string{"/run/systemd/network/10-netplan-eth0.network"}
			return in
		}},
		{"VLAN on a missing NIC", "warning", "DEPENDENCY_UNAVAILABLE", "/links/2/parent", func() Input { in := base; in.Links = in.Links[:1]; return in }},
		{"bridge member missing", "warning", "DEPENDENCY_UNAVAILABLE", "/links/3/members/0", func() Input { in := base; in.Links = in.Links[1:]; return in }},
		{"segment on a disabled link", "warning", "DEPENDENCY_UNAVAILABLE", "/segments/0/link", func() Input { return fixture(t, "disabled") }},
		{"delegation from an unavailable uplink", "warning", "DEPENDENCY_UNAVAILABLE", "/segments/1/addressing/ipv6/from", func() Input {
			in := fixture(t, "ethernet-uplink")
			in.Links = in.Links[1:]
			return in
		}},
		{"RA on a /56", "warning", "RA_PREFIX_UNSUPPORTED", "/segments/0/addressing/ipv6/ra", func() Input {
			return change(t, base, func(d map[string]any) {
				item(d, "segments", 0)["addressing"].(map[string]any)["ipv6"].(map[string]any)["addresses"] = []any{"2001:db8::1/56"}
			})
		}},
		{"RA on an uplink", "warning", "RA_PREFIX_UNSUPPORTED", "/segments/0/addressing/ipv6/ra", func() Input {
			return change(t, fixture(t, "bond"), func(d map[string]any) {
				item(d, "segments", 0)["addressing"].(map[string]any)["ipv6"].(map[string]any)["ra"] = map[string]any{"enabled": true, "managed": false, "otherInfo": false}
			})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := render(t, tc.in())
			if !hasFinding(result, tc.severity, tc.code, tc.path) || (tc.severity == "error" && result.Realizable) {
				t.Fatalf("want %s %s at %q; realizable=%v findings=%+v", tc.severity, tc.code, tc.path, result.Realizable, result.Findings)
			}
		})
	}
	own := base
	own.Links = append(slices.Clone(own.Links), links(observed{"br000000040000", "bridge", "02:00:00:00:00:09"})...)
	if result := render(t, own); len(result.Findings) != 0 {
		t.Fatalf("Andaru's own bridge from an earlier apply was treated as a collision: %+v", result.Findings)
	}
}

func TestMissingMemberKeepsMaster(t *testing.T) {
	in := fixture(t, "bond")
	in.Links = in.Links[:1]
	result := render(t, in)
	if !hasFile(result, "bo000000030000.netdev") || !hasFile(result, "ens18.network") || hasFile(result, "ens19.network") {
		t.Fatalf("one dead member took the bond down: %s", golden(result))
	}
	if !hasFinding(result, "warning", "DEPENDENCY_UNAVAILABLE", "/links/2/members/1") {
		t.Fatalf("missing member not reported: %+v", result.Findings)
	}
}

func TestUnrelatedLinksRenderWhenOneNICIsMissing(t *testing.T) {
	in := fixture(t, "ethernet-uplink")
	in.Links = in.Links[:1]
	result := render(t, in)
	if !hasFile(result, "ens18.network") || result.Realizable {
		t.Fatalf("WAN must still render while the LAN NIC is missing: %s", golden(result))
	}
}

func TestUserTextNeverRendered(t *testing.T) {
	in := change(t, fixture(t, "bridge"), func(d map[string]any) {
		for _, key := range []string{"links", "segments", "zones"} {
			for i := range d[key].([]any) {
				item(d, key, i)["name"] = fmt.Sprintf("X]\n[Network]\nDHCP=yes %d", i)
				item(d, key, i)["description"] = "X]\n[Network]\nDHCP=yes"
			}
		}
	})
	for _, f := range render(t, in).Files {
		if strings.Contains(f.Real(), "X]") || strings.Contains(f.Path, "X]") {
			t.Fatalf("user text reached %s", f.Path)
		}
	}
}

func TestEmptyDeclaration(t *testing.T) {
	result := render(t, Input{Declaration: []byte(`{"schemaVersion":1,"links":[],"segments":[],"zones":[]}`)})
	body, _ := json.Marshal(result)
	if string(body) != `{"realizable":true,"resolved":[],"files":[],"findings":[]}` {
		t.Fatalf("empty declaration: %s", body)
	}
	if _, err := Network(Input{Declaration: []byte(`{`)}); err == nil {
		t.Fatal("undecodable declaration rendered")
	}
}

func TestUpstreamOffersIgnoredOnSLAAC(t *testing.T) {
	// A router advertisement with the managed flag starts DHCPv6 even on a SLAAC segment.
	network := fileEnding(t, render(t, fixture(t, "bond")), "bo000000030000.network").Content
	if !strings.Contains(network, "[DHCPv6]\nUseDNS=yes\nUseNTP=no\nUseHostname=no\nUseDomains=no\n") || strings.Contains(network, "WithoutRA") {
		t.Fatalf("SLAAC uplink would adopt provider NTP/hostname through RA-triggered DHCPv6:\n%s", network)
	}
}

func TestHostKeepsOfferedDNS(t *testing.T) {
	for _, f := range render(t, fixture(t, "ethernet-uplink")).Files {
		if strings.Contains(f.Content, "UseDNS=no") {
			t.Fatalf("%s would leave the host without a resolver:\n%s", f.Path, f.Content)
		}
		for _, name := range []string{"DHCPv4", "DHCPv6", "IPv6AcceptRA"} {
			_, body, found := strings.Cut(f.Content, "["+name+"]\n")
			if !found {
				continue
			}
			body, _, _ = strings.Cut(body, "\n[")
			if !strings.Contains(body, "UseDNS=yes\n") || !strings.Contains(body, "UseDomains=no") {
				t.Fatalf("%s [%s] must keep offered DNS and ignore domains:\n%s", f.Path, name, body)
			}
			if name != "IPv6AcceptRA" && (!strings.Contains(body, "UseNTP=no") || !strings.Contains(body, "UseHostname=no")) {
				t.Fatalf("%s [%s] adopts NTP or hostname:\n%s", f.Path, name, body)
			}
		}
	}
}

func TestOutputOrderIsCanonical(t *testing.T) {
	in := change(t, fixture(t, "pppoe-1492"), func(d map[string]any) {
		slices.Reverse(d["links"].([]any))
		v := item(d, "segments", 1)["addressing"].(map[string]any)
		v["ipv4"] = map[string]any{"mode": "static", "addresses": []any{"192.0.2.9/24", "192.0.2.1/24"}}
		v["ipv6"] = map[string]any{"mode": "static", "addresses": []any{"2001:db8:0:9::1/64", "2001:db8:0:1::1/64"}, "ra": map[string]any{"enabled": true, "managed": false, "otherInfo": false}}
	})
	in.Links = append(in.Links, links(observed{"ens19", "", "52:54:00:aa:bb:02"})...)
	in.Facts.ForeignFiles = []string{"/etc/systemd/network/b.network", "/etc/systemd/network/a.network"}
	in = change(t, in, func(d map[string]any) {
		d["links"] = append(d["links"].([]any),
			map[string]any{"id": "lnk_00000009-0000-4000-8000-000000000009", "name": "gone", "kind": "ethernet", "mtu": 1500, "enabled": true, "permanentMac": "52:54:00:aa:bb:99"},
			map[string]any{"id": "lnk_00000000-0000-4000-8000-000000000000", "name": "spare", "kind": "ethernet", "mtu": 1500, "enabled": true, "permanentMac": "52:54:00:aa:bb:02"})
	})
	result := render(t, in)
	parent := fileEnding(t, result, "ens18.network").Content
	bridge := fileEnding(t, result, "br000000050000.network").Content
	for content, ordered := range map[string][]string{
		parent: {"VLAN=vl000000020000", "VLAN=vl000000040000"},
		bridge: {"Address=192.0.2.1/24", "Address=192.0.2.9/24", "Address=2001:db8:0:1::1/64", "Address=2001:db8:0:9::1/64", "Prefix=2001:db8:0:1::/64", "Prefix=2001:db8:0:9::/64"},
	} {
		last := -1
		for _, line := range ordered {
			at := strings.Index(content, line+"\n")
			if at <= last {
				t.Fatalf("%q out of order in:\n%s", line, content)
			}
			last = at
		}
	}
	if len(result.Resolved) != 2 || result.Resolved[0].Interface != "ens19" || result.Resolved[1].Interface != "ens18" {
		t.Fatalf("resolutions not ordered by link ID: %+v", result.Resolved)
	}
	if len(result.Findings) != 3 || result.Findings[0].Message >= result.Findings[1].Message || result.Findings[1].Path != "" || result.Findings[2].Code != "HARDWARE_MISSING" {
		t.Fatalf("findings not ordered by path, code, message: %+v", result.Findings)
	}
}

func TestDeclaredLinksDerivingOneNameCollide(t *testing.T) {
	in := change(t, fixture(t, "bridge"), func(d map[string]any) {
		d["links"] = append(d["links"].([]any), map[string]any{"id": "lnk_00000003-0000-4000-9000-000000000009", "name": "twin", "kind": "vlan", "mtu": 1500, "enabled": true, "parent": item(d, "links", 1)["id"], "vlanId": 11})
	})
	result := render(t, in)
	if !hasFinding(result, "error", "NAME_COLLISION", "/links/2") || !hasFinding(result, "error", "NAME_COLLISION", "/links/4") || hasFile(result, "vl000000030000.netdev") {
		t.Fatalf("two links deriving vl000000030000 must both be refused: %s", golden(result))
	}
}

func TestLinkLocalAddressIsNotAnnounced(t *testing.T) {
	in := change(t, fixture(t, "bridge"), func(d map[string]any) {
		item(d, "segments", 0)["addressing"].(map[string]any)["ipv6"].(map[string]any)["addresses"] = []any{"fe80::1/64", "2001:db8:0:1::1/64"}
	})
	network := fileEnding(t, render(t, in), "br000000040000.network").Content
	if strings.Contains(network, "fe80::/64") || !strings.Contains(network, "Prefix=2001:db8:0:1::/64") || !strings.Contains(network, "Address=fe80::1/64") {
		t.Fatalf("a link-local prefix must not be advertised:\n%s", network)
	}
}

func TestDeclaredDNSReplacesOffered(t *testing.T) {
	result := render(t, fixture(t, "dhcp-dns"))
	declaring, other := fileEnding(t, result, "ens18.network").Content, fileEnding(t, result, "ens19.network").Content
	if !strings.Contains(declaring, "\nDNS=192.0.2.53\n") || strings.Contains(declaring, "\nDomains=") || strings.Contains(declaring, "UseDNS=yes") || strings.Count(declaring, "UseDNS=no") != 3 {
		t.Fatalf("declared servers must replace offered ones in every section:\n%s", declaring)
	}
	// A second uplink keeps contributing what its DHCP server offers.
	if strings.Contains(other, "DNS=192") || !strings.Contains(other, "UseDNS=yes") || strings.Contains(other, "UseDNS=no") {
		t.Fatalf("a segment without declared DNS changed:\n%s", other)
	}
}

func TestDNSOrderIsDeclared(t *testing.T) {
	network := fileEnding(t, render(t, fixture(t, "static-dns")), "ens18.network").Content
	want := "Address=192.0.2.10/24\nGateway=192.0.2.1\nDNS=192.0.2.53\nDNS=2001:db8::53\nDomains=b.example a.example\n"
	if !strings.Contains(network, want) {
		t.Fatalf("want, in this order:\n%s\ngot:\n%s", want, network)
	}
}

func TestDNSOnUnrealisableLink(t *testing.T) {
	in := fixture(t, "static-dns")
	in.Links = nil
	for _, f := range render(t, in).Files {
		if strings.Contains(f.Content, "DNS=") {
			t.Fatalf("%s names DNS servers for an interface that does not exist:\n%s", f.Path, f.Content)
		}
	}
}

func TestDNSNotAppliedFinding(t *testing.T) {
	in := fixture(t, "static-dns")
	in.Facts.ResolvedInCharge = false
	result := render(t, in)
	want := Finding{"warning", "DNS_NOT_APPLIED", "/segments/0/addressing/dns", "This host's /etc/resolv.conf is not managed by systemd-resolved, so these servers are saved but not used."}
	if !slices.Equal(result.Findings, []Finding{want}) || !result.Realizable {
		t.Fatalf("findings = %+v, realizable = %v", result.Findings, result.Realizable)
	}
	if got := render(t, fixture(t, "static-dns")).Findings; len(got) != 0 {
		t.Fatalf("resolved is in charge, yet: %+v", got)
	}
	in = fixture(t, "ethernet-uplink")
	in.Facts.ResolvedInCharge = false
	for _, f := range render(t, in).Findings {
		if f.Code == "DNS_NOT_APPLIED" {
			t.Fatal("a configuration that declares no DNS was warned about it")
		}
	}
}

func TestRenderDHCPIdentity(t *testing.T) {
	in := fixture(t, "ethernet-uplink")
	without := render(t, in)
	duid := []byte{0x00, 0x01, 0x00, 0x01, 0x32, 0x87, 0x03, 0x54, 0xbc, 0x24, 0x11, 0x38, 0x71, 0x37}
	// The WAN takes DHCP and DHCPv6 and was taken from ifupdown; the LAN is static and has an identity it must not use.
	in.DHCPIdentity = map[string]host.DHCPIdentity{"52:54:00:aa:bb:01": {DUID: duid, Hostname: "fw"}, "52:54:00:aa:bb:02": {DUID: duid}}
	with := render(t, in)
	wan := fileEnding(t, with, "50-andaru-ens18.network").Content
	for _, want := range []string{
		"KeepConfiguration=dynamic-on-stop\n",
		"[DHCPv4]\nClientIdentifier=duid\nIAID=0x00aabb01\nDUIDType=1\nDUIDRawData=00:01:32:87:03:54:bc:24:11:38:71:37\nSendRelease=no\nHostname=fw\n",
		"[DHCPv6]\nIAID=0x00aabb01\nDUIDType=1\nDUIDRawData=00:01:32:87:03:54:bc:24:11:38:71:37\n",
	} {
		if !strings.Contains(wan, want) {
			t.Fatalf("missing %q in:\n%s", want, wan)
		}
	}
	if lan := fileEnding(t, with, "50-andaru-ens19.network").Content; lan != fileEnding(t, without, "50-andaru-ens19.network").Content {
		t.Fatalf("a static segment changed:\n%s", lan)
	}
	if golden(render(t, fixture(t, "ethernet-uplink"))) != golden(without) {
		t.Fatal("rendering without identities is not stable")
	}
}

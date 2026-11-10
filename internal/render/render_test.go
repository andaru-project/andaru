package render

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

var update = flag.Bool("update", false, "rewrite golden files")

var allFacts = host.RealizerFacts{PPPDaemon: true, PPPoEPlugin: true, PPPDevice: true}

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
	declaration, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", name+".links.json"))
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
	names, err := filepath.Glob("testdata/*.links.json")
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
			}
			want, err := os.ReadFile(file)
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

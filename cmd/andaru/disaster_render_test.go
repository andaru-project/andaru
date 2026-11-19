package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
)

// disasterKept is render's filter-two-zones fixture: a kept LAN with a management segment.
//
//go:embed testdata/disaster-kept.json
var disasterKept []byte

// A kept LAN with a management segment comes up behind its own filter, loaded before its files, and the filter is
// never written out as a file.
func TestRenderDisasterLoadsTheKeptFilter(t *testing.T) {
	body := disasterKept
	oldHost, oldLoad := disasterHost, loadDisasterFilter
	t.Cleanup(func() { disasterHost, loadDisasterFilter = oldHost, oldLoad })
	disasterHost = func(context.Context) ([]host.LinkStatus, host.RealizerFacts, error) {
		return []host.LinkStatus{
				{Name: "ens18", LinkType: "ether", LinkAddress: "52:54:00:aa:bb:01", PermanentAddress: "52:54:00:aa:bb:01"},
				{Name: "ens19", LinkType: "ether", LinkAddress: "52:54:00:aa:bb:02", PermanentAddress: "52:54:00:aa:bb:02"},
			},
			host.RealizerFacts{NftTool: true}, nil
	}
	root := t.TempDir()
	var script string
	loadDisasterFilter = func(s []byte) error {
		if entries, _ := os.ReadDir(filepath.Join(root, "run/systemd/network")); len(entries) > 0 {
			t.Error("the kept LAN's files were placed before its filter loaded")
		}
		script = string(s)
		return nil
	}
	if err := renderDisaster(root)(body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "table inet andaru") {
		t.Fatalf("no filter loaded: %q", script)
	}
	if _, err := os.Stat(filepath.Join(root, "nft:inet/andaru")); !os.IsNotExist(err) {
		t.Fatal("the filter was written out as a file")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "run/systemd/network")); len(entries) == 0 {
		t.Fatal("the kept LAN's network files were not placed")
	}
}

// Without its filter the kept LAN does not come up.
func TestRenderDisasterRefusesWithoutAFilter(t *testing.T) {
	body := disasterKept
	oldHost := disasterHost
	t.Cleanup(func() { disasterHost = oldHost })
	disasterHost = func(context.Context) ([]host.LinkStatus, host.RealizerFacts, error) {
		return nil, host.RealizerFacts{}, nil
	}
	root := t.TempDir()
	if err := renderDisaster(root)(body); err == nil {
		t.Fatal("brought up a kept LAN that renders no filter")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "run")); len(entries) > 0 {
		t.Fatal("files placed for a refused kept LAN")
	}
}

// A lan-closure names its allowed sources' addresses at the top level; the kept filter must still admit them.
func TestRenderDisasterKeepsAllowedSources(t *testing.T) {
	// The two-zone kept LAN, with SSH allowed only from one host and one network.
	var decl map[string]any
	if err := json.Unmarshal(disasterKept, &decl); err != nil {
		t.Fatal(err)
	}
	decl["management"].(map[string]any)["allowedSources"] = []string{"adr_00000009-0000-4000-8000-000000000001", "adr_00000009-0000-4000-8000-000000000002"}
	objects, _ := decl["objects"].(map[string]any)
	if objects == nil {
		objects = map[string]any{}
		decl["objects"] = objects
	}
	objects["addresses"] = append(slicesOf(objects["addresses"]),
		map[string]any{"id": "adr_00000009-0000-4000-8000-000000000001", "name": "admin", "kind": "host", "address": "192.0.2.10"},
		map[string]any{"id": "adr_00000009-0000-4000-8000-000000000002", "name": "admin6", "kind": "network", "prefix": "2001:db8::/64"})
	raw, err := json.Marshal(decl)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := config.LANClosure(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldHost, oldLoad := disasterHost, loadDisasterFilter
	t.Cleanup(func() { disasterHost, loadDisasterFilter = oldHost, oldLoad })
	disasterHost = func(context.Context) ([]host.LinkStatus, host.RealizerFacts, error) {
		return []host.LinkStatus{
				{Name: "ens18", LinkType: "ether", LinkAddress: "52:54:00:aa:bb:01", PermanentAddress: "52:54:00:aa:bb:01"},
				{Name: "ens19", LinkType: "ether", LinkAddress: "52:54:00:aa:bb:02", PermanentAddress: "52:54:00:aa:bb:02"},
			},
			host.RealizerFacts{NftTool: true}, nil
	}
	var script string
	loadDisasterFilter = func(s []byte) error { script = string(s); return nil }
	if err := renderDisaster(t.TempDir())(closure); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "192.0.2.10") || !strings.Contains(script, "2001:db8::/64") {
		t.Fatalf("the kept filter lost its allowed sources:\n%s", script)
	}
}

func slicesOf(v any) []any {
	out, _ := v.([]any)
	return out
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

// A kept LAN with a management segment comes up behind its own filter, loaded before its files, and the filter is
// never written out as a file.
func TestRenderDisasterLoadsTheKeptFilter(t *testing.T) {
	body, err := os.ReadFile("../../internal/render/testdata/filter-two-zones.json")
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
	body, err := os.ReadFile("../../internal/render/testdata/filter-two-zones.json")
	if err != nil {
		t.Fatal(err)
	}
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

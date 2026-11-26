package render

import (
	"slices"
	"strings"
	"testing"
)

func TestLLDPRender(t *testing.T) {
	in := change(t, fixture(t, "bridge"), func(d map[string]any) {
		eth0 := item(d, "links", 0)
		eth0["lldp"] = "receive"
		eth0["emitLldp"] = "nearest-bridge"

		eth1 := item(d, "links", 1)
		eth1["lldp"] = "routers-only"
		eth1["emitLldp"] = "off"
	})
	res := render(t, in)
	if !res.Realizable {
		t.Fatalf("unexpected findings: %+v", res.Findings)
	}

	fileOf := func(suffix string) string {
		t.Helper()
		idx := slices.IndexFunc(res.Files, func(f File) bool { return strings.HasSuffix(f.Path, suffix) })
		if idx < 0 {
			t.Fatalf("missing rendered file %s", suffix)
		}
		return res.Files[idx].Content
	}

	eth0 := fileOf("ens18.network")
	if !strings.Contains(eth0, "LLDP=yes\n") || !strings.Contains(eth0, "EmitLLDP=nearest-bridge\n") {
		t.Fatalf("unexpected eth0 LLDP render:\n%s", eth0)
	}
	// Explicit emitLldp:"off" renders canonically identical to omitted (no EmitLLDP= line).
	eth1 := fileOf("ens19.network")
	if !strings.Contains(eth1, "LLDP=routers-only\n") || strings.Contains(eth1, "EmitLLDP=") {
		t.Fatalf("unexpected eth1 LLDP render:\n%s", eth1)
	}
	// Bridge keeps LLDP=no and never emits EmitLLDP=.
	br := fileOf("br000000040000.network")
	if !strings.Contains(br, "LLDP=no\n") || strings.Contains(br, "EmitLLDP=") {
		t.Fatalf("unexpected bridge LLDP render:\n%s", br)
	}

	// Unvalidated / non-ethernet LLDP values are safely ignored by the allowlist.
	inHostile := change(t, fixture(t, "bridge"), func(d map[string]any) {
		eth0 := item(d, "links", 0)
		eth0["emitLldp"] = "nearest-bridge\nInjected=yes"
		brLink := item(d, "links", 2)
		brLink["lldp"] = "receive"
		brLink["emitLldp"] = "nearest-bridge"
	})
	resHostile := render(t, inHostile)
	for _, f := range resHostile.Files {
		if strings.Contains(f.Content, "Injected=") || strings.Contains(f.Content, "EmitLLDP=") {
			t.Fatalf("unvalidated or non-ethernet LLDP leaked into %s:\n%s", f.Path, f.Content)
		}
	}
}

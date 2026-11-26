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

	check := func(suffix string, wantLines ...string) {
		t.Helper()
		idx := slices.IndexFunc(res.Files, func(f File) bool { return strings.HasSuffix(f.Path, suffix) })
		if idx < 0 {
			t.Fatalf("missing rendered file %s", suffix)
		}
		for _, line := range wantLines {
			if !strings.Contains(res.Files[idx].Content, line+"\n") {
				t.Fatalf("%s missing %q:\n%s", suffix, line, res.Files[idx].Content)
			}
		}
	}

	check("ens18.network", "LLDP=yes", "EmitLLDP=nearest-bridge")
	check("ens19.network", "LLDP=routers-only", "EmitLLDP=no")
	// Bridge without explicit LLDP keeps LLDP=no and omits EmitLLDP=.
	idx := slices.IndexFunc(res.Files, func(f File) bool { return strings.HasSuffix(f.Path, "br000000040000.network") })
	if idx < 0 || !strings.Contains(res.Files[idx].Content, "LLDP=no\n") || strings.Contains(res.Files[idx].Content, "EmitLLDP=") {
		t.Fatalf("unexpected default LLDP render:\n%s", res.Files[idx].Content)
	}
}

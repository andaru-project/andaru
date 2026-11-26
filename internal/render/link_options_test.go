package render

import (
	"slices"
	"strings"
	"testing"
)

func TestBridgeSTPOptionsRender(t *testing.T) {
	in := change(t, fixture(t, "bridge"), func(d map[string]any) {
		br := item(d, "links", 3)
		br["stp"] = true
		br["stpPriority"] = 4096
		br["forwardDelay"] = 12
		br["helloTime"] = 2
		br["maxAge"] = 18
		br["multicastSnooping"] = false
		br["vlanFiltering"] = true
	})
	res := render(t, in)
	if !res.Realizable {
		t.Fatalf("unexpected findings: %+v", res.Findings)
	}
	idx := slices.IndexFunc(res.Files, func(f File) bool { return strings.HasSuffix(f.Path, "br000000040000.netdev") })
	if idx < 0 {
		t.Fatal("missing bridge .netdev")
	}
	want := `[NetDev]
Name=br000000040000
Kind=bridge

[Bridge]
STP=yes
Priority=4096
ForwardDelaySec=12s
HelloTimeSec=2s
MaxAgeSec=18s
MulticastSnooping=no
VLANFiltering=yes
`
	if res.Files[idx].Content != want {
		t.Fatalf("bridge .netdev mismatch:\ngot:\n%s\nwant:\n%s", res.Files[idx].Content, want)
	}
}

func TestBondL2OptionsRender(t *testing.T) {
	in := change(t, fixture(t, "bond"), func(d map[string]any) {
		bo := item(d, "links", 2)
		bo["transmitHashPolicy"] = "layer3+4"
		bo["lacpRate"] = "fast"
		bo["miiMonitorMs"] = 100
	})
	res := render(t, in)
	if !res.Realizable {
		t.Fatalf("unexpected findings: %+v", res.Findings)
	}
	idx := slices.IndexFunc(res.Files, func(f File) bool { return strings.HasSuffix(f.Path, "bo000000030000.netdev") })
	if idx < 0 {
		t.Fatal("missing bond .netdev")
	}
	want := `[NetDev]
Name=bo000000030000
Kind=bond

[Bond]
Mode=802.3ad
TransmitHashPolicy=layer3+4
LACPTransmitRate=fast
MIIMonitorSec=100ms
`
	if res.Files[idx].Content != want {
		t.Fatalf("bond .netdev mismatch:\ngot:\n%s\nwant:\n%s", res.Files[idx].Content, want)
	}

	// Zero MII monitor interval renders MIIMonitorSec=0.
	inZero := change(t, fixture(t, "bond"), func(d map[string]any) {
		bo := item(d, "links", 2)
		bo["miiMonitorMs"] = 0
	})
	resZero := render(t, inZero)
	idxZero := slices.IndexFunc(resZero.Files, func(f File) bool { return strings.HasSuffix(f.Path, "bo000000030000.netdev") })
	if !strings.Contains(resZero.Files[idxZero].Content, "MIIMonitorSec=0\n") {
		t.Fatalf("expected MIIMonitorSec=0, got:\n%s", resZero.Files[idxZero].Content)
	}
}

package render

import (
	"slices"
	"strings"
	"testing"
)

func TestOffloadsRenderIntoTheLinkFile(t *testing.T) {
	in := change(t, fixture(t, "bridge"), func(d map[string]any) {
		item(d, "links", 0)["offloads"] = map[string]any{"largeReceive": false, "genericReceive": true, "unknown": false}
		item(d, "links", 2)["offloads"] = map[string]any{"largeReceive": false} // a bridge: never rendered
	})
	res := render(t, in)
	if !res.Realizable {
		t.Fatalf("unexpected findings: %+v", res.Findings)
	}
	i := slices.IndexFunc(res.Files, func(f File) bool { return strings.HasSuffix(f.Path, "ens18.link") })
	if i < 0 {
		t.Fatal("missing ens18.link")
	}
	// In table order, whatever the saved order; a key Andaru does not manage is dropped.
	if want := "[Link]\nName=ens18\nGenericReceiveOffload=yes\nLargeReceiveOffload=no\n"; !strings.Contains(res.Files[i].Content, want) {
		t.Fatalf("ens18.link:\n%s\nwant it to hold:\n%s", res.Files[i].Content, want)
	}
	for _, f := range res.Files {
		if f.Path != res.Files[i].Path && strings.Contains(f.Content, "Offload=") {
			t.Fatalf("offloads leaked into %s:\n%s", f.Path, f.Content)
		}
	}
}

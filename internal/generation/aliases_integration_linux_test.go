//go:build integration

package generation

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/aliasfeed"
)

// The rendered filter with its aliases' fill loads in one transaction, and the kernel holds the members.
func TestAliasFillLoadsInNft(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("a network namespace needs root")
	}
	rendered, err := os.ReadFile("../render/testdata/filter-dynamic-aliases.golden")
	if err != nil {
		t.Fatal(err)
	}
	// The golden file holds every rendered file, each after its own ### line; the filter is one of them.
	_, filter, _ := strings.Cut(string(rendered), "### "+FilterEntry)
	_, filter, _ = strings.Cut(filter, "\n")
	filter, _, _ = strings.Cut(filter, "\n### ")
	golden := []byte(filter)
	state := t.TempDir()
	data, _ := json.Marshal(aliasfeed.State{Members: []string{"10.0.0.0/8", "10.1.0.0/16", "192.0.2.1", "2001:db8::/32"}})
	writeFile(t, aliasfeed.Path(state, "adr_00000001-0000-4000-8000-000000000001"), string(data))
	path := filepath.Join(t.TempDir(), "filter.nft")
	writeFile(t, path, string(golden)+string(aliasFill(state, golden)))
	out, err := exec.Command("unshare", "-n", "sh", "-c", `nft -f "$1" && nft list set inet andaru adr_00000001_0000_4000_8000_000000000001_4 &&
		nft list set inet andaru adr_00000001_0000_4000_8000_000000000001_6 && nft list set inet andaru adr_00000002_0000_4000_8000_000000000002_4`, "sh", path).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	// Overlapping networks are merged; the never-fetched alias stays empty.
	for _, want := range []string{"elements = { 10.0.0.0/8, 192.0.2.1 }", "elements = { 2001:db8::/32 }"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("no %q in\n%s", want, out)
		}
	}
	if strings.Count(string(out), "elements") != 2 {
		t.Fatalf("an empty set has elements:\n%s", out)
	}
}

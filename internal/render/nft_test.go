package render

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func nftFile(t *testing.T, r Result) (File, bool) {
	t.Helper()
	for _, f := range r.Files {
		if f.Path == nftPath {
			return f, true
		}
	}
	return File{}, false
}

// Every rendered filter must be a script nft accepts; checking needs root and nft, which CI's root suites have.
func TestNftScriptIsValid(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("nft -c needs root")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	names, _ := fs.Glob(testdata, "testdata/filter-*.links.json")
	if len(names) == 0 {
		t.Fatal("no filter fixtures")
	}
	for _, path := range names {
		name := strings.TrimSuffix(filepath.Base(path), ".links.json")
		f, ok := nftFile(t, render(t, fixture(t, name)))
		if !ok {
			t.Fatalf("%s rendered no filter", name)
		}
		script := filepath.Join(t.TempDir(), "andaru.nft")
		if err := os.WriteFile(script, []byte(f.Content), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("nft", "-c", "-f", script).CombinedOutput(); err != nil {
			t.Fatalf("%s: nft refused the script: %s\n%s", name, out, f.Content)
		}
	}
}

// Without a management segment the filter is off: the files are exactly today's and nothing forwards.
func TestNoFilterWithoutManagement(t *testing.T) {
	plain := render(t, fixture(t, "ethernet-uplink"))
	in := change(t, fixture(t, "filter-two-zones"), func(d map[string]any) {
		delete(d, "management")
		delete(d, "policy")
		delete(d, "nat")
		delete(d, "objects")
	})
	got := render(t, in)
	if golden(got) != golden(plain) {
		t.Fatalf("output changed without a filter:\n%s", golden(got))
	}
	for _, f := range got.Files {
		if strings.Contains(f.Content, "Forwarding=") {
			t.Fatalf("%s forwards without a filter", f.Path)
		}
	}
	// Rules without a management segment are refused, not silently dropped.
	refused := render(t, change(t, fixture(t, "filter-two-zones"), func(d map[string]any) { delete(d, "management") }))
	if refused.Realizable || !hasFinding(refused, "error", "MANAGEMENT_REQUIRED", "/management") {
		t.Fatalf("findings %+v", refused.Findings)
	}
}

// After a takeover the kernel name changes (eth0 becomes ens18); the rules follow the link, not the old name.
func TestManagementFollowsTheLinkName(t *testing.T) {
	in := fixture(t, "filter-two-zones")
	for i := range in.Links {
		in.Links[i].Name = strings.Replace(in.Links[i].Name, "ens", "enp0s", 1)
	}
	f, _ := nftFile(t, render(t, in))
	if !strings.Contains(f.Content, `iifname "enp0s19" tcp dport 22 accept`) {
		t.Fatalf("management rule does not use the current name:\n%s", f.Content)
	}
}

func TestFilterFindings(t *testing.T) {
	in := fixture(t, "filter-two-zones")
	in.Facts.NftTool = false
	if r := render(t, in); !hasFinding(r, "error", "TOOL_MISSING", "") || r.Realizable {
		t.Fatalf("missing nft: %+v", r.Findings)
	}
	in = fixture(t, "filter-two-zones")
	in.Facts.FilterUnits = []string{"nftables.service"}
	if r := render(t, in); !hasFinding(r, "error", "FOREIGN_FILTER", "nftables.service") {
		t.Fatalf("foreign filter: %+v", r.Findings)
	}
	// Forwarding is switched on together with the filter, on every segment's file.
	for _, f := range render(t, fixture(t, "filter-two-zones")).Files {
		if strings.HasSuffix(f.Path, ".network") && !strings.Contains(f.Content, "IPv4Forwarding=yes\nIPv6Forwarding=yes\n") {
			t.Fatalf("%s does not forward:\n%s", f.Path, f.Content)
		}
	}
}

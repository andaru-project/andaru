package generation

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeardownClearsLive(t *testing.T) {
	b := newBench(t)
	b.net.devices["vl0"] = true
	files := served(`{"Dhcp4":{}}`)
	maps.Copy(files, two)
	files[filterEntry] = "table inet andaru {\n\tcomment \"generation 1\"\n}\n"
	id := b.gen(false, files)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name := range b.net.netFiles() {
		if strings.HasPrefix(name, "50-andaru-") {
			t.Fatalf("%s left in /run", name)
		}
	}
	for _, path := range []string{keaFile, unboundFile, resolvedFile} {
		if b.exists(path[1:]) {
			t.Fatalf("%s left in /run", path)
		}
	}
	for _, c := range []string{"networkctl reload", "networkctl delete vl0", "systemctl stop kea-dhcp4.service", "systemctl restart unbound.service"} {
		if b.index(c) < 0 {
			t.Fatalf("%q not run: %q", c, b.net.argv)
		}
	}
	if b.net.table != denyAllComment || !strings.Contains(b.net.script, "policy drop") || !strings.Contains(b.net.script, `iif "lo" accept`) {
		t.Fatalf("filter %q: %s", b.net.table, b.net.script)
	}
	if b.net.bound["eth0"] != "" {
		t.Fatalf("eth0 still managed after the teardown: %q", b.net.bound)
	}
	// A teardown that already ran, or a crash after it, may run it again.
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The next apply verifies against what is left: no old uplink to keep.
	next := b.gen(false, map[string]string{"50-andaru-eth1.network": "[Match]\nName=eth1\n\n[Network]\nAddress=192.0.2.1/24\n"})
	b.net.devices["eth1"] = true
	if err := b.r.Activate(t.Context(), next); err != nil {
		t.Fatalf("the next apply kept the old uplink in its check: %v", err)
	}
}

func TestTeardownWithoutLiveTouchesNothing(t *testing.T) {
	b := cloud(t)
	before := b.net.netFiles()
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.net.argv) != 0 || !maps.Equal(before, b.net.netFiles()) {
		t.Fatalf("a host Andaru never ran was touched: %q", b.net.argv)
	}
}

func TestTeardownBaselineAfterTakeover(t *testing.T) {
	b := cloud(t)
	if err := b.r.captureBaseline(Pointers{}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{Takeover: true}); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.net.netFiles()) != 0 {
		t.Fatalf("baseline files left: %v", b.net.netFiles())
	}
	if b.net.table != denyAllComment {
		t.Fatalf("filter = %q", b.net.table)
	}
}

func TestTeardownFencesFirst(t *testing.T) {
	b := newBench(t)
	b.live(one)
	b.net.fail = "networkctl reload"
	if err := b.r.Teardown(t.Context()); err == nil {
		t.Fatal("teardown hid the failed reload")
	}
	if b.net.table != denyAllComment {
		t.Fatalf("a half-done teardown left filter %q", b.net.table)
	}
}

func TestTeardownDamagedLive(t *testing.T) {
	b := newBench(t)
	b.live(one)
	// The stored copies are gone; the paths in the manifest still say what is in /run.
	stored, _ := filepath.Glob(filepath.Join(b.dir, "1", "*"))
	for _, path := range stored {
		if base := filepath.Base(path); base != "manifest.json" && base != "state.json" {
			os.RemoveAll(path)
		}
	}
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.net.netFiles()) != 0 {
		t.Fatalf("left %v", b.net.netFiles())
	}
}

func TestUnreadableManifestCountsAsReset(t *testing.T) {
	b := newBench(t)
	if err := os.MkdirAll(filepath.Join(b.dir, "baseline", "manifest.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !b.r.resetTree() {
		t.Fatal("an unreadable manifest was taken for an ordinary tree")
	}
}

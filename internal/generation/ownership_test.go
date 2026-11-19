package generation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/kept"
)

// fresh is the same host after a wipe: an empty generations directory.
// setUp gives the box a kept copy, as a box set up before has: no generation running then means deny-all.
func (b *bench) setUp() {
	b.t.Helper()
	if err := os.MkdirAll(filepath.Join(b.net.root, kept.Root, "first"), 0o700); err != nil {
		b.t.Fatal(err)
	}
}

func fresh(t *testing.T, b *bench) *bench {
	t.Helper()
	if err := os.RemoveAll(b.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOwnershipRoundTrip(t *testing.T) {
	b := newBench(t)
	b.live(one)
	o, err := b.r.ReadOwnership()
	if err != nil || !o.Live {
		t.Fatalf("ownership = %+v, %v", o, err)
	}
	fresh(t, b)
	if err = b.r.WriteOwnership(o); err != nil {
		t.Fatal(err)
	}
	if p := b.pointers(); p.Confirmed != nil || p.Live != nil {
		t.Fatalf("pointers = %+v", p)
	}
	if data, err := os.ReadFile(filepath.Join(b.dir, "baseline", "manifest.json")); err != nil || string(data) != "{\"reset\":true}\n" || !b.r.resetTree() {
		t.Fatalf("baseline manifest = %q, %v", data, err)
	}
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestOwnershipOfATreeNeverApplied(t *testing.T) {
	b := newBench(t)
	o, err := b.r.ReadOwnership()
	if err != nil || o.Live {
		t.Fatalf("ownership = %+v, %v", o, err)
	}
	if err = b.r.WriteOwnership(o); err != nil {
		t.Fatal(err)
	}
	if dirExists(filepath.Join(b.dir, "baseline")) {
		t.Fatal("a box whose network Andaru never ran got a reset baseline")
	}
}

// Boot fences a box whose confirmed generation no longer loads and clears Live; Andaru still ran its network, so a
// reset must bring one up and keep the fresh tree fenced.
func TestOwnershipOfAFencedConfirmedBox(t *testing.T) {
	one := 1
	for name, p := range map[string]Pointers{"confirmed": {Confirmed: &one}, "lastKnownGood": {LastKnownGood: &one}} {
		b := newBench(t)
		if err := writeJSON(filepath.Join(b.dir, "pointers.json"), p); err != nil {
			t.Fatal(err)
		}
		if o, err := b.r.ReadOwnership(); err != nil || !o.Live {
			t.Errorf("%s: ownership = %+v, %v", name, o, err)
		}
	}
}

func TestOwnershipRefusesNonEmpty(t *testing.T) {
	b := newBench(t)
	b.live(one)
	o, err := b.r.ReadOwnership()
	if err != nil {
		t.Fatal(err)
	}
	if err = b.r.WriteOwnership(o); err == nil {
		t.Fatal("ownership was written over existing generations")
	}
}

func TestResetTreeIsConsoleRequired(t *testing.T) {
	for _, withGeneration := range []bool{false, true} {
		b := newBench(t)
		b.setUp()
		b.live(one)
		o, err := b.r.ReadOwnership()
		if err != nil {
			t.Fatal(err)
		}
		fresh(t, b)
		if err = b.r.WriteOwnership(o); err != nil {
			t.Fatal(err)
		}
		if withGeneration {
			// A confirmed generation that cannot be read: no candidate is usable.
			id := b.gen(one)
			if err := os.RemoveAll(filepath.Join(b.dir, itoa(id))); err != nil {
				t.Fatal(err)
			}
			p := b.pointers()
			p.Confirmed = &id
			if err := writeJSON(filepath.Join(b.dir, "pointers.json"), p); err != nil {
				t.Fatal(err)
			}
		}
		b.reboot()
		err = b.r.Boot(t.Context())
		if err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") {
			t.Fatalf("withGeneration=%v: boot = %v", withGeneration, err)
		}
		if b.net.table != denyAllComment || !strings.Contains(b.net.script, "policy drop") || !b.exists(catchAllPath[1:]) {
			t.Fatalf("withGeneration=%v: filter %q, catch-all %v: %s", withGeneration, b.net.table, b.exists(catchAllPath[1:]), b.net.script)
		}
	}
}

// resetBench is a reset host: the ownership record only, deny-all loaded, eth1 present.
func resetBench(t *testing.T) *bench {
	t.Helper()
	b := newBench(t)
	b.setUp()
	b.live(one)
	o, err := b.r.ReadOwnership()
	if err != nil {
		t.Fatal(err)
	}
	fresh(t, b)
	if err = b.r.WriteOwnership(o); err != nil {
		t.Fatal(err)
	}
	if err = b.r.DenyAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.net.devices["eth1"] = true
	return b
}

const lanFilter = "table inet andaru {\n\tcomment \"generation lan\"\n}\n"

func TestFailedApplyOnResetHostKeepsDenyAll(t *testing.T) {
	b := resetBench(t)
	// Its link is missing: verification fails and the files go back to none.
	id := b.gen(map[string]string{"50-andaru-eth9.network": "[Match]\nName=eth9\n\n[Network]\nAddress=192.0.2.1/24\n", filterEntry: lanFilter})
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activated a generation whose link is missing")
	}
	if b.net.table != denyAllComment {
		t.Fatalf("filter after the failed apply = %q", b.net.table)
	}
}

func TestRollbackOnResetHostKeepsDenyAll(t *testing.T) {
	b := resetBench(t)
	id := b.gen(map[string]string{"50-andaru-eth1.network": "[Match]\nName=eth1\n\n[Network]\nAddress=192.0.2.1/24\n", filterEntry: lanFilter})
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	if b.net.table != denyAllComment {
		t.Fatalf("filter after the rollback = %q", b.net.table)
	}
	if b.exists("run/systemd/network/50-andaru-eth1.network") {
		t.Fatal("the rolled-back files stayed")
	}
}

// A generation ran: the fresh tree is a reset tree, fenced at every boot.
func TestLiveTreeStaysFenced(t *testing.T) {
	b := newBench(t)
	b.setUp()
	b.live(one)
	o, err := b.r.ReadOwnership()
	if err != nil || !o.Live {
		t.Fatalf("ownership = %+v, %v", o, err)
	}
	fresh(t, b)
	if err = b.r.WriteOwnership(o); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Boot(t.Context()); err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") {
		t.Fatalf("boot = %v", err)
	}
	if b.net.table != denyAllComment {
		t.Fatalf("filter = %q", b.net.table)
	}
}

// A second reset of a host whose first reset left nothing live still writes a fenced tree.
func TestResetTreeReadAgainStaysFenced(t *testing.T) {
	b := newBench(t)
	b.setUp()
	b.live(one)
	for range 2 {
		o, err := b.r.ReadOwnership()
		if err != nil || !o.Live {
			t.Fatalf("ownership = %+v, %v", o, err)
		}
		fresh(t, b)
		if err = b.r.WriteOwnership(o); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.r.Boot(t.Context()); err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") || b.net.table != denyAllComment {
		t.Fatalf("boot = %v, filter %q", err, b.net.table)
	}
}

// On a reset tree with no usable generation to go back to, a rollback restores the fenced bare set, as its baseline.
func TestRollbackOnResetHostWithNothingUsableRestoresTheBareSet(t *testing.T) {
	b := resetBench(t)
	eth1 := map[string]string{"50-andaru-eth1.network": "[Match]\nName=eth1\n\n[Network]\nAddress=192.0.2.1/24\n", filterEntry: lanFilter}
	first := b.gen(eth1)
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := b.gen(map[string]string{"50-andaru-eth1.network": "[Match]\nName=eth1\n\n[Network]\nAddress=192.0.2.2/24\n", filterEntry: lanFilter})
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, itoa(first), "files", "50-andaru-eth1.network"), "evil")
	if err := b.timerFires(second); err != nil {
		t.Fatal(err)
	}
	if g := b.read(second); g.State.State != RolledBack || !strings.HasSuffix(g.Reason, "(restored baseline)") {
		t.Fatalf("state = %+v", g.State)
	}
	if b.pointers().Live != nil || b.net.table != denyAllComment {
		t.Fatalf("live %v, filter %q", b.pointers().Live, b.net.table)
	}
	if b.exists(netDir+"50-andaru-eth1.network") || !b.exists(catchAllPath[1:]) {
		t.Fatalf("/run/systemd/network = %v", b.net.netFiles())
	}
}

package generation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/kept"
	"github.com/andaru-project/andaru/internal/render"
)

// withCatchAll is a generation's files plus the catch-all every rendered generation carries.
func withCatchAll(files map[string]string) map[string]string {
	out := map[string]string{filepath.Base(catchAllPath): string(catchAll.data)}
	for k, v := range files {
		out[k] = v
	}
	return out
}

// wantBare checks the filter and the catch-all of a host on which no generation runs.
func (b *bench) wantBare(setUp bool) {
	b.t.Helper()
	want := map[bool]string{false: "andaru factory", true: denyAllComment}[setUp]
	if b.net.table != want || b.exists(catchAllPath[1:]) != setUp {
		b.t.Fatalf("filter %q, catch-all %v; want %q, %v", b.net.table, b.exists(catchAllPath[1:]), want, setUp)
	}
}

func TestCatchAllIsTheRendered(t *testing.T) {
	if catchAllPath != render.UnclassifiedPath || string(catchAll.data) != render.Unclassified {
		t.Fatalf("boot's catch-all %s differs from the rendered %s:\n%s", catchAllPath, render.UnclassifiedPath, catchAll.data)
	}
}

func TestSetUpReadsKeptCopies(t *testing.T) {
	copy := func(name string) func(string) error {
		return func(root string) error { return os.MkdirAll(filepath.Join(root, name), 0o700) }
	}
	for _, row := range []struct {
		name string
		make func(root string) error
		want bool
	}{
		{"absent", func(string) error { return nil }, false},
		{"empty", copy(""), false},
		{"recovery", copy("recovery"), true},
		{"last-lan", copy("last-lan"), true},
		{"first", copy("first"), true},
		// Neither present nor absent: fail closed. A file in its place fails even for root, whom modes do not stop.
		{"unreadable", func(root string) error { return os.WriteFile(root, nil, 0o600) }, true},
	} {
		root := filepath.Join(t.TempDir(), "kept")
		if err := row.make(root); err != nil {
			t.Fatal(err)
		}
		if got := (Realizer{KeptRoot: root}).setUp(); got != row.want {
			t.Errorf("%s: set up = %v", row.name, got)
		}
	}
	if os.Geteuid() != 0 {
		root := filepath.Join(t.TempDir(), "kept")
		if err := os.Mkdir(root, 0); err != nil {
			t.Fatal(err)
		}
		if !(Realizer{KeptRoot: root}).setUp() {
			t.Error("an unreadable kept directory counted as never set up")
		}
	}
	// Production reads the kept copies under the realizer's root.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, kept.Root, "first"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !(Realizer{Root: root}).setUp() {
		t.Error("the kept copy under the root was not found")
	}
}

func TestFactoryFilter(t *testing.T) {
	b := newBench(t)
	if err := b.r.factoryFilter(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"policy drop", `iif "lo" accept`, "ct state established,related accept", "udp sport 67 udp dport 68 accept",
		"udp sport 547 udp dport 546 accept", "icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept"} {
		if !strings.Contains(b.net.script, rule) {
			t.Fatalf("no %q:\n%s", rule, b.net.script)
		}
	}
	if b.net.table != "andaru factory" || b.forwarding() != "0\n" {
		t.Fatalf("table %q, IPv6 forwarding %q", b.net.table, b.forwarding())
	}
	if os.Geteuid() != 0 {
		t.Skip("nft -c needs root")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		if os.Getenv("ANDARU_TEST_REQUIRE_TOOLS") == "1" {
			t.Fatal("nft not installed")
		}
		t.Skip("nft not installed")
	}
	script := filepath.Join(t.TempDir(), "factory.nft")
	writeFile(t, script, b.net.script)
	if out, err := exec.Command("nft", "-c", "-f", script).CombinedOutput(); err != nil {
		t.Fatalf("nft refused the factory filter: %s\n%s", out, b.net.script)
	}
}

// The catch-all binds no link when every link is declared; verification must not wait for it.
func TestActivateWithoutUnclassifiedLink(t *testing.T) {
	b := newBench(t)
	id := b.gen(withCatchAll(one))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if b.read(id).State.State != Awaiting {
		t.Fatalf("state %+v", b.read(id).State)
	}
}

func TestDisasterBootNeverSetUp(t *testing.T) {
	b := newBench(t)
	b.r.StateDir = filepath.Join(t.TempDir(), "missing")
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantBare(false)
}

func TestDisasterBootSetUp(t *testing.T) {
	b := newBench(t)
	b.r.StateDir = filepath.Join(t.TempDir(), "missing")
	b.r.KeptRoot = filepath.Join(b.net.root, kept.Root)
	writeCopy(t, b.r.KeptRoot, "last-lan", "last")
	// The kept LAN comes up with the catch-all and no filter, as before.
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.table != "" || !b.exists(catchAllPath[1:]) {
		t.Fatalf("filter %q, catch-all %v", b.net.table, b.exists(catchAllPath[1:]))
	}
	// No kept LAN renders: nothing came up.
	b.reboot()
	b.r.RenderDisaster = func([]byte) error { return os.ErrInvalid }
	if err := b.r.Boot(t.Context()); err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") {
		t.Fatalf("boot = %v", err)
	}
	b.wantBare(true)
}

func TestBootWithoutConfirmed(t *testing.T) {
	for _, reset := range []bool{false, true} {
		for _, setUp := range []bool{false, true} {
			b := newBench(t)
			if setUp {
				b.setUp()
			}
			if reset {
				if err := b.r.WriteOwnership(Ownership{Live: true}); err != nil {
					t.Fatal(err)
				}
			} else if err := writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{}); err != nil {
				t.Fatal(err)
			}
			if err := b.r.Boot(t.Context()); (err != nil) != reset {
				t.Fatalf("reset=%v setUp=%v: boot = %v", reset, setUp, err)
			}
			// A tree a factory reset wrote is set up, kept copies or not.
			b.wantBare(setUp || reset)
		}
	}
}

// Every candidate fails on a tree with no baseline to go back to, as on the image. A confirmed generation means the
// box was set up even without kept copies (uplink management keeps none), so it is fenced either way.
func TestBootWithEveryCandidateFailing(t *testing.T) {
	for _, setUp := range []bool{false, true} {
		b := newBench(t)
		if setUp {
			b.setUp()
		}
		b.live(filtered(withCatchAll(one), "aa"))
		b.reboot()
		b.net.fail = "nft -f " + b.dir
		if err := b.r.Boot(t.Context()); err == nil || !strings.Contains(err.Error(), "no generation is usable") {
			t.Fatalf("setUp=%v: boot = %v", setUp, err)
		}
		b.wantBare(true)
	}
}

func TestRollbackOfFirstApply(t *testing.T) {
	for _, setUp := range []bool{false, true} {
		b := newBench(t)
		if setUp {
			b.setUp()
		}
		id := b.gen(filtered(withCatchAll(one), "aa"))
		if err := b.r.Activate(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		b.net.argv = nil
		if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
			t.Fatal(err)
		}
		b.wantBare(setUp)
		if b.exists(netDir + "50-andaru-eth0.network") {
			t.Fatalf("setUp=%v: the rolled-back files stayed", setUp)
		}
		// Tightened first: the bare filter is in before networkd drops the generation's files.
		if load, reload := b.index("nft -f"), b.index("networkctl reload"); load < 0 || load > reload {
			t.Fatalf("setUp=%v: the bare filter came after the files: %q", setUp, b.net.argv)
		}
	}
}

func TestTowardNothing(t *testing.T) {
	for _, setUp := range []bool{false, true} {
		b := newBench(t)
		if setUp {
			b.setUp()
		}
		placed := ""
		if err := b.r.toward(t.Context(), nil, func() error { placed = b.net.table; return nil }); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]string{false: "andaru factory", true: denyAllComment}[setUp]; placed != want || b.net.table != want {
			t.Fatalf("setUp=%v: filter %q while placing, %q after", setUp, placed, b.net.table)
		}
	}
}

// A previous generation without a filter still runs without one: only no generation at all gets the bare filter.
func TestRollbackToUnfilteredGenerationKeepsNoTable(t *testing.T) {
	b := newBench(t)
	b.setUp()
	b.live(one)
	id := b.gen(filtered(two, "bb"))
	b.net.devices["vl0"] = true
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Rollback(context.Background(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if b.net.table != "" {
		t.Fatalf("filter %q under an unfiltered generation", b.net.table)
	}
}

// Boot behind a blocked reset: deny-all where the journal says so, and the catch-all once the box was set up.
func TestFence(t *testing.T) {
	for _, row := range []struct {
		denyAll, setUp bool
		filter         string
	}{{false, false, "andaru factory"}, {true, false, denyAllComment}, {false, true, denyAllComment}, {true, true, denyAllComment}} {
		b := newBench(t)
		if row.setUp {
			b.setUp()
		}
		if err := b.r.Fence(t.Context(), row.denyAll); err != nil {
			t.Fatal(err)
		}
		if b.net.table != row.filter || b.exists(catchAllPath[1:]) != (row.setUp || row.denyAll) {
			t.Fatalf("%+v: filter %q, catch-all %v", row, b.net.table, b.exists(catchAllPath[1:]))
		}
	}
}

// Kept copies are best-effort and never kept for uplink management: a confirmed generation, a last known good one,
// a reset tree or an unreadable commit point also mean set up.
func TestSetUpWithoutKeptCopies(t *testing.T) {
	one := 1
	for name, prepare := range map[string]func(b *bench) error{
		"confirmed": func(b *bench) error {
			return writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{Confirmed: &one})
		},
		"lastKnownGood": func(b *bench) error {
			return writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{LastKnownGood: &one})
		},
		"reset tree": func(b *bench) error { return b.r.WriteOwnership(Ownership{Live: true}) },
		"unreadable": func(b *bench) error { return os.WriteFile(filepath.Join(b.dir, "pointers.json"), []byte("{"), 0o600) },
	} {
		b := newBench(t)
		if err := prepare(b); err != nil {
			t.Fatal(err)
		}
		if !b.r.setUp() {
			t.Errorf("%s: not set up", name)
		}
	}
	if b := newBench(t); b.r.setUp() {
		t.Error("a fresh box counts as set up")
	}
}

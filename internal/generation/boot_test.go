package generation

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// reboot empties /run as a boot does.
func (b *bench) reboot() {
	b.t.Helper()
	if err := os.RemoveAll(filepath.Join(b.net.root, "run")); err != nil {
		b.t.Fatal(err)
	}
	b.net.argv = nil
}

// first activates a generation of one on a host where none ran before.
func (b *bench) first() int {
	b.t.Helper()
	id := b.gen(one)
	if err := b.r.Activate(b.t.Context(), id); err != nil {
		b.t.Fatal(err)
	}
	return id
}

func (b *bench) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(b.net.root, rel))
	return err == nil
}

func TestConfirmMovesPointers(t *testing.T) {
	b := newBench(t)
	id := b.first()
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.wantPointers(1, 0, 0, 1)
	if g := b.read(id); g.State.State != Confirmed || g.Reason != "" {
		t.Fatalf("state = %+v", g.State)
	}
	// A later apply confirmed on top keeps the first as last-known-good.
	second := b.gen(two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	b.wantPointers(2, 1, 0, 2)
}

func TestConfirmRefusals(t *testing.T) {
	b := newBench(t)
	id := b.first()
	if err := b.r.Confirm(t.Context(), 99); !errors.Is(err, ErrWrongState) {
		t.Fatalf("unknown id: %v", err)
	}
	b.now = b.now.Add(121 * time.Second)
	if err := b.r.Confirm(t.Context(), id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("after the deadline: %v", err)
	}
	b.wantPointers(0, 0, 1, 1)
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("after a rollback: %v", err)
	}
	b.wantPointers(0, 0, 0, 0)

	b = newBench(t)
	id = b.first()
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("twice: %v", err)
	}
	b.wantPointers(1, 0, 0, 1)
}

func TestRollbackAfterConfirmDoesNothing(t *testing.T) {
	b := newBench(t)
	id := b.first()
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	before := len(b.net.argv)
	if err := b.timerFires(id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("a late timer rolled back a confirmed generation: %v", err)
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
	if len(b.net.argv) != before {
		t.Fatalf("argv = %q", b.net.argv[before:])
	}
}

func TestRollbackQueuedBehindConfirm(t *testing.T) {
	b := newBench(t)
	id := b.first()
	held, err := os.OpenFile(filepath.Join(b.dir, "lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.timerFires(id) }()
	time.Sleep(150 * time.Millisecond) // the timer is now waiting on the lock
	if err := writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{Confirmed: &id, Live: &id}); err != nil {
		t.Fatal(err)
	}
	_ = held.Close()
	if err := <-done; !errors.Is(err, ErrWrongState) {
		t.Fatalf("rollback queued behind a confirmation: %v", err)
	}
	b.wantDisk(one)
}

func TestConfirmKilledBetweenWrites(t *testing.T) {
	b := newBench(t)
	id := b.first()
	killed := errors.New("killed")
	b.r.failAt = func(step string) error {
		if step == "confirm:state" {
			return killed
		}
		return nil
	}
	if err := b.r.Confirm(t.Context(), id); !errors.Is(err, killed) {
		t.Fatal(err)
	}
	b.r.failAt = nil
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
	if g := b.read(id); g.State.State != Confirmed {
		t.Fatalf("a confirmed apply was undone by the reboot: %+v", g.State)
	}
}

func TestBootRenamesAdaptersBeforeNetworkd(t *testing.T) {
	b := newBench(t)
	link := "50-andaru-eth0.link"
	id := b.gen(map[string]string{"50-andaru-eth0.network": uplinkDHCP, link: "[Match]\nPermanentMACAddress=02:00:00:00:00:01\n\n[Link]\nName=eth0\n"})
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	// udev named the adapters at boot before Andaru's .link files existed: it must apply them before networkd starts.
	renamed := false
	b.net.onRun = func(command string) {
		if command == "udevadm trigger --settle --action=add --subsystem-match=net" {
			// Named before anything else is placed, so what follows sees the final names.
			renamed = b.exists(netDir+link) && !b.exists(netDir+"50-andaru-eth0.network")
		}
	}
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	// udev reads the new .link files only after the reload, so the trigger must come after it.
	reload := slices.Index(b.net.argv, "udevadm control --reload")
	trigger := slices.Index(b.net.argv, "udevadm trigger --settle --action=add --subsystem-match=net")
	if !renamed || b.net.ran("udevadm control --reload") != 1 || reload < 0 || trigger < reload {
		t.Fatalf("adapters not renamed after the .link files were placed: %q", b.net.argv)
	}
}

func TestBootContinuesWhenSystemctlFails(t *testing.T) {
	b := newBench(t)
	if err := b.r.Confirm(t.Context(), b.first()); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.fail = "systemctl"
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	if b.net.ran("networkctl") != 0 {
		t.Fatalf("boot talked to a networkd that has not started: %q", b.net.argv)
	}
}

func TestBootBeforeAnyConfirmation(t *testing.T) {
	b := newBench(t)
	id := b.first()
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(map[string]string{})
	b.wantPointers(0, 0, 0, 0)
	if g := b.read(id); g.State.State != RolledBack || g.Reason != "reboot" {
		t.Fatalf("state = %+v", g.State)
	}
}

func TestBootFallsBack(t *testing.T) {
	b := newBench(t)
	if err := b.r.Confirm(t.Context(), b.first()); err != nil {
		t.Fatal(err)
	}
	second := b.gen(two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	corrupt := func(id int) {
		writeFile(t, filepath.Join(b.dir, itoa(id), "files", storedName(netDir+"50-andaru-eth0.network")), "evil")
	}

	corrupt(2)
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	b.wantPointers(2, 1, 0, 1) // live differs from confirmed: that is the reported fallback

	corrupt(1)
	b.reboot()
	if err := b.r.Boot(t.Context()); err == nil {
		t.Fatal("boot reported success with nothing usable")
	}
	b.wantPointers(2, 1, 0, 0)
	// Only the catch-all, which a box set up always keeps.
	if got := b.net.netFiles(); len(got) != 1 || got["51-andaru-unclassified.network"] == "" {
		t.Fatalf("boot wrote unverified files: %v", slices.Collect(maps.Keys(got)))
	}
}

func TestDeadlineRollbackWaitsForTheDeadline(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(two)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	// The timer was armed before activation; if it fires early the operator must still get the window they were shown.
	if err := b.r.Rollback(t.Context(), id, "deadline"); err == nil || errors.Is(err, ErrWrongState) {
		t.Fatalf("the timer rolled back before the deadline: %v", err)
	}
	b.wantDisk(two)
	b.wantPointers(1, 0, id, id)
	if err := b.r.Rollback(t.Context(), id, "requested"); err != nil {
		t.Fatalf("an operator's rollback must not wait: %v", err)
	}
}

func TestConfirmAfterFallbackKeepsTheUsableGeneration(t *testing.T) {
	b := newBench(t)
	b.live(one)
	second := b.gen(two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, itoa(second), "files", storedName(netDir+"50-andaru-eth0.network")), "evil")
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.net.devices = map[string]bool{"eth0": true}
	b.net.apply()
	b.wantPointers(second, 1, 0, 1) // boot fell back to generation 1
	third := b.gen(map[string]string{"50-andaru-eth0.network": uplinkStatic})
	if err := b.r.Activate(t.Context(), third); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), third); err != nil {
		t.Fatal(err)
	}
	// Last-known-good must be what actually ran before, not the corrupt generation the confirmed pointer still named.
	b.wantPointers(third, 1, 0, third)
}

func TestBootFallbackAdvancesAuthorizationWithoutIdentityDatabase(t *testing.T) {
	b := newBench(t)
	b.live(one)
	live := b.read(1).AuthorizationDigest
	initial := initializeAuthorization(t, b.r.StateDir, live)
	changed := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	second := create(t, b.dir, Manifest{SavedVersion: "8", AuthorizationDigest: changed, ConfirmTimeoutSeconds: 120}, two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, itoa(second), "files", storedName(netDir+"50-andaru-eth0.network")), "corrupt")
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, b.r.StateDir)
	if state.LiveDigest != live || state.AuthorizationEpoch != initial.AuthorizationEpoch+2 {
		t.Fatalf("boot fallback authorization state = %+v", state)
	}
	b.wantPointers(second, 1, 0, 1)
}

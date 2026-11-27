package generation

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSafeModeBootConsumesNextFlagAndHoldsAddons(t *testing.T) {
	b := newBench(t)
	if err := ScheduleSafeMode(b.r.StateDir); err != nil {
		t.Fatal(err)
	}
	if got := ReadSafeMode(b.net.root, b.r.StateDir); !got.Active || !slices.Equal(got.Reasons, []string{"boot"}) {
		t.Fatalf("before boot: %+v", got)
	}

	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !b.exists(SafeModeBootFile) {
		t.Fatal("boot did not create /run/andaru/safe-mode")
	}
	if !b.exists(AddonsHeldFile) {
		t.Fatal("boot did not create /run/andaru/addons-held")
	}
	if _, err := os.Stat(filepath.Join(b.r.StateDir, SafeModeNextFile)); !os.IsNotExist(err) {
		t.Fatalf("safe-mode-next was not removed after moving to /run: %v", err)
	}
	if got := ReadSafeMode(b.net.root, b.r.StateDir); !got.Active || !slices.Equal(got.Reasons, []string{"boot", "settingsHold"}) {
		t.Fatalf("after boot: %+v", got)
	}

	// Releasing the settings hold clears settingsHold while keeping boot safe mode active.
	if err := b.r.ReleaseAddonsHold(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := ReadSafeMode(b.net.root, b.r.StateDir); !got.Active || !slices.Equal(got.Reasons, []string{"boot"}) {
		t.Fatalf("after release: %+v", got)
	}
	// Idempotent second release.
	if err := b.r.ReleaseAddonsHold(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Next boot has /run cleared and safe-mode-next already consumed, so boot safe mode is gone.
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.exists(SafeModeBootFile) {
		t.Fatal("one-boot safe mode persisted across a second boot")
	}
	if got := ReadSafeMode(b.net.root, b.r.StateDir); !got.Active || !slices.Equal(got.Reasons, []string{"settingsHold"}) {
		t.Fatalf("second boot before release: %+v", got)
	}
	if err := b.r.ReleaseAddonsHold(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := ReadSafeMode(b.net.root, b.r.StateDir); got.Active || len(got.Reasons) != 0 || got.Reasons == nil {
		t.Fatalf("second boot after release: %+v", got)
	}
}

func TestSafeModeKernelArgExactTokenOnly(t *testing.T) {
	b := newBench(t)
	proc := filepath.Join(b.net.root, "proc")
	if err := os.MkdirAll(proc, 0o755); err != nil {
		t.Fatal(err)
	}
	// Near-matches must not activate safe mode.
	if err := os.WriteFile(filepath.Join(proc, "cmdline"), []byte("quiet andaru.safe=10 notandaru.safe=1 andaru.safe=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadSafeMode(b.net.root, b.r.StateDir); got.Active {
		t.Fatalf("near-match activated safe mode: %+v", got)
	}

	if err := os.WriteFile(filepath.Join(proc, "cmdline"), []byte("quiet andaru.safe=1 console=ttyS0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !b.exists(SafeModeBootFile) {
		t.Fatal("kernel arg boot did not write /run/andaru/safe-mode")
	}
	if got := ReadSafeMode(b.net.root, b.r.StateDir); !got.Active || !slices.Equal(got.Reasons, []string{"kernelArg", "settingsHold"}) {
		t.Fatalf("kernel arg reasons: %+v", got)
	}
}

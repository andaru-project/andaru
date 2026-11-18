package reset

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

// A bind mount of the same filesystem inside the tree is found before anything is unlinked.
// It runs in a private mount namespace, so the host's mounts never change.
func TestWipeRefusesBindMount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	runtime.LockOSThread() // the namespace belongs to this thread; it ends with it
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		t.Skipf("no private mount namespace here: %v", err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Skipf("cannot make mounts private: %v", err)
	}
	base := t.TempDir()
	state, outside := filepath.Join(base, "andaru"), filepath.Join(base, "outside")
	write(t, filepath.Join(state, "a", "file"), "x")
	write(t, filepath.Join(outside, "keep"), "outside data")
	if err := os.Mkdir(filepath.Join(state, "a", "mounted"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(outside, filepath.Join(state, "a", "mounted"), "", unix.MS_BIND, ""); err != nil {
		t.Skipf("bind mount: %v", err)
	}
	defer unix.Unmount(filepath.Join(state, "a", "mounted"), unix.MNT_DETACH)
	if err := wipe(state, MarkerName, nil); err == nil {
		t.Fatal("wiped across a bind mount")
	}
	for _, path := range []string{filepath.Join(state, "a", "file"), filepath.Join(outside, "keep")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed: %v", path, err)
		}
	}
}

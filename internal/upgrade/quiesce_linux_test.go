package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withDpkg(t *testing.T, path string) {
	t.Helper()
	old := dpkgBinary
	dpkgBinary = path
	t.Cleanup(func() { dpkgBinary = old })
}

// The appliance image has no dpkg: there is no package database to audit, and nothing runs.
func TestDpkgConsistentWithoutDpkg(t *testing.T) {
	withDpkg(t, filepath.Join(t.TempDir(), "dpkg"))
	run := func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ran a command without dpkg")
		return nil, nil
	}
	if err := DpkgConsistent(t.Context(), run); err != nil {
		t.Fatal(err)
	}
}

func TestDpkgConsistentAuditsAnInstalledDpkg(t *testing.T) {
	withDpkg(t, os.Args[0])
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "dpkg" || strings.Join(args, " ") != "--audit" {
			t.Fatalf("ran %s %v", name, args)
		}
		return []byte("The following packages are only half configured\n"), nil
	}
	if err := DpkgConsistent(t.Context(), run); err == nil {
		t.Fatal("accepted incomplete packages")
	}
}

// Skipping the audit must not skip the rest: a queued realize job still refuses.
func TestCheckIdleWithoutDpkgStillChecksRealizeJobs(t *testing.T) {
	withDpkg(t, filepath.Join(t.TempDir(), "dpkg"))
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && args[0] == "list-jobs" {
			return []byte("12 andaru-realize-apply.service start waiting\n"), nil
		}
		return nil, nil
	}
	if err := CheckIdle(t.Context(), run, t.TempDir()); err == nil {
		t.Fatal("accepted a queued realize job")
	}
}

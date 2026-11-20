package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// seedBench stages a seed where the installer leaves it and returns the staged path.
func seedBench(t *testing.T, body string) string {
	t.Helper()
	staged := filepath.Join(t.TempDir(), "install-seed.json")
	if err := os.WriteFile(staged, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := stagedSeed
	stagedSeed = staged
	t.Cleanup(func() { stagedSeed = old })
	return staged
}

func bootWith(t *testing.T, stateDir string) (string, []string) {
	t.Helper()
	verbs := &fakeVerbs{}
	var stderr bytes.Buffer
	if code := realizeCommand(context.Background(), []string{"--state-dir", stateDir, "boot"}, &stderr, func(string) (realizeVerbs, error) { return verbs, nil }); code != 0 {
		t.Fatalf("boot exited %d: %s", code, stderr.String())
	}
	return stderr.String(), verbs.calls
}

func TestBootMovesTheSeed(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		staged := seedBench(t, `{"version": 1}`)
		stateDir := filepath.Join(t.TempDir(), "andaru")
		if !fresh {
			if err := os.Mkdir(stateDir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		stderr, calls := bootWith(t, stateDir)
		target := filepath.Join(stateDir, "install-seed.json")
		body, err := os.ReadFile(target)
		if err != nil || string(body) != `{"version": 1}` || stderr != "" || !slices.Equal(calls, []string{"boot"}) {
			t.Fatalf("fresh=%v: seed %q %v, stderr %q, calls %v", fresh, body, err, stderr, calls)
		}
		file, _ := os.Stat(target)
		dir, _ := os.Stat(stateDir)
		if file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
			t.Fatalf("fresh=%v: seed %v, state directory %v", fresh, file.Mode(), dir.Mode())
		}
		if _, err := os.Lstat(staged); !os.IsNotExist(err) {
			t.Fatalf("fresh=%v: the staged copy stayed: %v", fresh, err)
		}
		if _, err := os.Lstat(target + ".andaru-tmp"); !os.IsNotExist(err) {
			t.Fatalf("fresh=%v: the temporary file stayed: %v", fresh, err)
		}
	}
}

func TestBootDiscardsAStaleStagedSeed(t *testing.T) {
	staged := seedBench(t, `{"version": 1, "stale": true}`)
	stateDir := t.TempDir()
	target := filepath.Join(stateDir, "install-seed.json")
	if err := os.WriteFile(target, []byte(`{"version": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, _ := bootWith(t, stateDir)
	if body, _ := os.ReadFile(target); string(body) != `{"version": 1}` || stderr != "" {
		t.Fatalf("the target changed to %q, stderr %q", body, stderr)
	}
	if _, err := os.Lstat(staged); !os.IsNotExist(err) {
		t.Fatalf("the stale staged copy stayed: %v", err)
	}
}

func TestBootSurvivesAFailedSeedMove(t *testing.T) {
	staged := seedBench(t, `{"version": 1}`)
	// A state directory under a file cannot be created.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, calls := bootWith(t, filepath.Join(blocker, "andaru"))
	if !strings.Contains(stderr, "install seed not moved") || !slices.Equal(calls, []string{"boot"}) {
		t.Fatalf("stderr %q, calls %v", stderr, calls)
	}
	if body, err := os.ReadFile(staged); err != nil || string(body) != `{"version": 1}` {
		t.Fatalf("the staged copy was lost: %q %v", body, err)
	}
}

func TestOnlyBootMovesTheSeed(t *testing.T) {
	staged := seedBench(t, `{"version": 1}`)
	stateDir := t.TempDir()
	realizeCommand(context.Background(), []string{"--state-dir", stateDir, "leases"}, &bytes.Buffer{}, func(string) (realizeVerbs, error) { return &fakeVerbs{}, nil })
	if _, err := os.Lstat(staged); err != nil {
		t.Fatalf("leases moved the seed: %v", err)
	}
}

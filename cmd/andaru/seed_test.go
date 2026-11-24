package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/reset"
)

// No test may move a seed staged on the host running it.
func init() { stagedSeed = "/nonexistent/andaru-install/install-seed.json" }

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

func realizeWith(t *testing.T, stateDir, verb string) (string, []string) {
	t.Helper()
	verbs := &fakeVerbs{}
	var stderr bytes.Buffer
	if code := realizeCommand(context.Background(), []string{"--state-dir", stateDir, verb}, nil, &stderr, func(string, string) (realizeVerbs, error) { return verbs, nil }); code != 0 {
		t.Fatalf("%s exited %d: %s", verb, code, stderr.String())
	}
	return stderr.String(), verbs.calls
}

func TestBootMovesTheSeed(t *testing.T) {
	for name, fresh := range map[string]bool{"fresh": true, "existing": false} {
		t.Run(name, func(t *testing.T) {
			staged := seedBench(t, `{"version": 1}`)
			stateDir := filepath.Join(t.TempDir(), "andaru")
			if !fresh {
				if err := os.Mkdir(stateDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(stateDir, "install-seed.json")
			if !fresh {
				// Left by a power cut mid-move.
				if err := os.WriteFile(target+".andaru-tmp", []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			stderr, calls := realizeWith(t, stateDir, "boot")
			body, err := os.ReadFile(target)
			if err != nil || string(body) != `{"version": 1}` || stderr != "" || !slices.Equal(calls, []string{"boot"}) {
				t.Fatalf("seed %q %v, stderr %q, calls %v", body, err, stderr, calls)
			}
			file, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			dir, err := os.Stat(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
				t.Fatalf("seed %v, state directory %v", file.Mode(), dir.Mode())
			}
			if _, err := os.Lstat(staged); !os.IsNotExist(err) {
				t.Fatalf("the staged copy stayed: %v", err)
			}
			if _, err := os.Lstat(target + ".andaru-tmp"); !os.IsNotExist(err) {
				t.Fatalf("the temporary file stayed: %v", err)
			}
		})
	}
}

func TestBootWithNoSeedIsQuiet(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "andaru")
	if stderr, calls := realizeWith(t, stateDir, "boot"); stderr != "" || !slices.Equal(calls, []string{"boot"}) {
		t.Fatalf("stderr %q, calls %v", stderr, calls)
	}
	if _, err := os.Lstat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("a boot with no seed created the state directory: %v", err)
	}
}

func TestBootDiscardsAStaleStagedSeed(t *testing.T) {
	staged := seedBench(t, `{"version": 1, "stale": true}`)
	stateDir := t.TempDir()
	target := filepath.Join(stateDir, "install-seed.json")
	if err := os.WriteFile(target, []byte(`{"version": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, _ := realizeWith(t, stateDir, "boot")
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
	stderr, calls := realizeWith(t, filepath.Join(blocker, "andaru"), "boot")
	if !strings.Contains(stderr, "install seed not moved") || !slices.Equal(calls, []string{"boot"}) {
		t.Fatalf("stderr %q, calls %v", stderr, calls)
	}
	if body, err := os.ReadFile(staged); err != nil || string(body) != `{"version": 1}` {
		t.Fatalf("the staged copy was lost: %q %v", body, err)
	}
}

func TestOnlyBootMovesTheSeed(t *testing.T) {
	staged := seedBench(t, `{"version": 1}`)
	realizeWith(t, t.TempDir(), "leases")
	if _, err := os.Lstat(staged); err != nil {
		t.Fatalf("leases moved the seed: %v", err)
	}
}

func TestFencedBootMovesTheSeed(t *testing.T) {
	b := newResetBench(t)
	b.plan(t, reset.TornDown, true)
	seedBench(t, `{"version": 1}`)
	stateDir := t.TempDir()
	if _, calls := realizeWith(t, stateDir, "boot"); !slices.Equal(calls, []string{"fence true"}) {
		t.Fatalf("calls %v", calls)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "install-seed.json")); err != nil {
		t.Fatalf("the fenced boot left the seed: %v", err)
	}
}

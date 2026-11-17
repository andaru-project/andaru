package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/generation"
)

func TestCommitRenderedLeavesDestinationOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ens18.network")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".andaru-tmp"
	err := commitRendered(path, []byte("new-partial"), 0o644)
	if err == nil {
		// The happy path replaces the file. Force the failure path by occupying the temp name as a directory.
		_ = os.WriteFile(path, []byte("old"), 0o644)
	}
	if err = os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(tmp, "stuck"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = commitRendered(path, []byte("new"), 0o644)
	if !errors.Is(err, generation.ErrRenderCleanup) {
		t.Fatalf("%v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "old" {
		t.Fatalf("destination %q %v", got, readErr)
	}
}

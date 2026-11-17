package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCommitRenderedLeavesDestinationOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ens18.network")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := writeRenderedBody
	t.Cleanup(func() { writeRenderedBody = old })
	writeRenderedBody = func(f *os.File, body []byte) error {
		if _, err := f.Write(body[:1]); err != nil {
			t.Fatal(err)
		}
		return errors.New("short write")
	}
	if err := commitRendered(path, []byte("new-partial"), 0o644); err == nil {
		t.Fatal("short write succeeded")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old" {
		t.Fatalf("destination %q %v", got, err)
	}
	if _, err = os.Lstat(path + ".andaru-tmp"); !os.IsNotExist(err) {
		t.Fatal("partial temp remains")
	}
}

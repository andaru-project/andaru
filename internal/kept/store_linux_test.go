package kept

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestKeptRefusesNesting(t *testing.T) {
	_, beforeErr := os.Lstat(Root)
	for _, stateDir := range []string{Root, filepath.Join(Root, "child"), "/var/lib", "/"} {
		if _, err := Open(stateDir); err == nil {
			t.Fatalf("Open(%s) succeeded", stateDir)
		}
	}
	_, afterErr := os.Lstat(Root)
	if os.IsNotExist(beforeErr) && !os.IsNotExist(afterErr) {
		t.Fatal("nesting check created the kept root")
	}
}

func TestOpenRejectsSymlinkRoot(t *testing.T) {
	state := t.TempDir()
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "kept")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenIn(link, state); err == nil {
		t.Fatal("symlink root opened")
	}
}

func TestPutRefusesSymlinkedCopyDir(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(t.TempDir(), "kept")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "last-lan")); err != nil {
		t.Fatal(err)
	}
	s, err := OpenIn(root, state)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Put(context.Background(), "last-lan", []byte("x"), true); err == nil {
		t.Fatal("symlink copy directory accepted")
	}
	if _, err = os.Lstat(filepath.Join(outside, "last-lan.json")); !os.IsNotExist(err) {
		t.Fatal("write followed the symlink")
	}
}

func TestKeptModes(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(t.TempDir(), "kept")
	s, err := OpenIn(root, state)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.Put(ctx, "last-lan", []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "last-lan", "last-lan.json")
	assertPrivate(t, root, 0o700)
	assertPrivate(t, filepath.Dir(file), 0o700)
	assertPrivate(t, file, 0o600)
	if err = s.Put(ctx, "last-lan", []byte("two"), true); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("last-lan")
	if err != nil || string(got) != "two" {
		t.Fatalf("read %q %v", got, err)
	}
	tmp := filepath.Join(root, "first", "first.json.tmp")
	if err = os.MkdirAll(filepath.Dir(tmp), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(tmp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = s.Put(ctx, "first", []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if err = s.Put(ctx, "first", []byte("again"), false); err == nil {
		t.Fatal("second first write succeeded")
	}
}

func TestMissingStateIsUnusable(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing")
	if LiveTreeUsable(state) {
		t.Fatal("missing state was usable")
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatalf("LiveTreeUsable created %s: %v", state, err)
	}
}

func assertPrivate(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is a symlink", path)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode %o, want %o", path, info.Mode().Perm(), mode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 {
		t.Fatalf("%s is not owned by the writer", path)
	}
	if info.Mode().IsRegular() && stat.Nlink != 1 {
		t.Fatalf("%s link count %d", path, stat.Nlink)
	}
}

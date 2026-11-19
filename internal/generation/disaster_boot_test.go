package generation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootLeavesAUsableTree(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "configuration"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "generations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "generations", "pointers.json"), []byte(`{"confirmed":null,"live":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Realizer{StateDir: state, Root: t.TempDir(), Now: timeNow}
	r.Run = (&fakeNetworkd{root: r.Root}).run
	if err := r.Boot(t.Context()); err != nil && strings.Contains(err.Error(), "unsafe kept copy") {
		t.Fatal(err)
	}
}

func TestDisasterBootOrder(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing")
	root := t.TempDir()
	keptRoot := filepath.Join(t.TempDir(), "kept")
	writeCopy(t, keptRoot, "last-lan", "last")
	writeCopy(t, keptRoot, "first", "first")
	writeCopy(t, keptRoot, "recovery", "recovery")
	r := Realizer{StateDir: state, Root: root, KeptRoot: keptRoot, Now: timeNow, Run: (&fakeNetworkd{root: root}).run}
	if err := r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := marker(t, root); got != "last-lan" {
		t.Fatalf("selected %s", got)
	}
	_ = os.RemoveAll(filepath.Join(keptRoot, "last-lan"))
	if err := r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := marker(t, root); got != "first" {
		t.Fatalf("selected %s", got)
	}
	_ = os.RemoveAll(filepath.Join(keptRoot, "first"))
	if err := r.Boot(t.Context()); err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") {
		t.Fatalf("both missing: %v", err)
	}
	writeCopy(t, keptRoot, "recovery", "recovery")
	if err := r.Boot(t.Context()); err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") {
		t.Fatal("recovery was selected")
	}
}

func TestDisasterBootStopsWhenRenderCleanupFails(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing")
	root := t.TempDir()
	keptRoot := filepath.Join(t.TempDir(), "kept")
	writeCopy(t, keptRoot, "last-lan", "last")
	writeCopy(t, keptRoot, "first", "first")
	calls := 0
	r := Realizer{StateDir: state, Root: root, KeptRoot: keptRoot, Now: timeNow, Run: (&fakeNetworkd{root: root}).run, RenderDisaster: func([]byte) error {
		calls++
		return ErrRenderCleanup
	}}
	if err := r.Boot(t.Context()); !errors.Is(err, ErrRenderCleanup) {
		t.Fatalf("%v", err)
	}
	if calls != 1 {
		t.Fatalf("render attempts %d", calls)
	}
	if _, err := os.Lstat(filepath.Join(root, "run", "andaru", "disaster-source")); !os.IsNotExist(err) {
		t.Fatal("boot continued after cleanup failure")
	}
}

func TestBootUsesKeptLANWhenCommitPointIsMissing(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	keptRoot := filepath.Join(t.TempDir(), "kept")
	writeCopy(t, keptRoot, "last-lan", "last")
	r := Realizer{StateDir: state, Root: root, KeptRoot: keptRoot, Now: timeNow, Run: (&fakeNetworkd{root: root}).run}
	if err := r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := marker(t, root); got != "last-lan" {
		t.Fatalf("selected %s", got)
	}
	if _, err := os.Lstat(filepath.Join(state, "generations", "pointers.json")); !os.IsNotExist(err) {
		t.Fatal("missing commit point was created")
	}
}

func TestDisasterBootDoesNotCreateState(t *testing.T) {
	parent := t.TempDir()
	state := filepath.Join(parent, "missing")
	upgrades := filepath.Join(parent, "upgrades", "keep")
	if err := os.MkdirAll(upgrades, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upgrades, "note"), []byte("stay"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	keptRoot := filepath.Join(t.TempDir(), "kept")
	writeCopy(t, keptRoot, "last-lan", "last")
	r := Realizer{StateDir: state, Root: root, KeptRoot: keptRoot, Now: timeNow, Run: (&fakeNetworkd{root: root}).run}
	if err := r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("disaster boot created the state directory")
	}
	if raw, err := os.ReadFile(filepath.Join(upgrades, "note")); err != nil || string(raw) != "stay" {
		t.Fatalf("upgrades changed: %q %v", raw, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "run", "andaru", "disaster-source")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(state, "run")); !os.IsNotExist(err) {
		t.Fatal("rendered into the state directory")
	}
}

func writeCopy(t *testing.T, root, name, token string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"kind":"lan-closure","schemaVersion":1,"token":"` + token + `","management":{"segment":"seg"},"links":[],"segments":[],"zones":[],"secrets":{}}`)
	if err := os.WriteFile(filepath.Join(dir, name+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func marker(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "run", "andaru", "disaster-source"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func timeNow() time.Time { return time.Unix(1_700_000_000, 0) }

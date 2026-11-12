package generation

import (
	"strings"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const netDir = "/run/systemd/network/"

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// create stores a generation whose files are named by basename under /run/systemd/network.
func create(t *testing.T, dir string, m Manifest, files map[string]string) int {
	t.Helper()
	contents := map[string][]byte{}
	m.Files = nil
	for name, body := range files {
		path := netDir + name
		if strings.HasPrefix(name, "/") {
			path = name // a service file outside networkd's directory
		}
		if name == filterEntry {
			path = name // the filter is a generation entry, not a file under /run
		}
		m.Files = append(m.Files, FileEntry{Path: path, Mode: "0644"})
		contents[path] = []byte(body)
	}
	id, err := Create(dir, m, contents)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func numbered(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestCreateNumbersAndReadsBack(t *testing.T) {
	dir := Dir(t.TempDir())
	first := create(t, dir, Manifest{SavedVersion: "7", ConfirmTimeoutSeconds: 120}, map[string]string{"50-andaru-a.network": "[Match]\n"})
	second := create(t, dir, Manifest{SavedVersion: "8"}, nil)
	if first != 1 || second != 2 {
		t.Fatalf("ids = %d, %d", first, second)
	}
	g, err := Read(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("[Match]\n"))
	if g.ID != 1 || g.SavedVersion != "7" || len(g.Files) != 1 || g.Files[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("manifest = %+v", g.Manifest)
	}
	if g.State.State != Failed || g.Reason != "never started" {
		t.Fatalf("state = %+v", g.State)
	}
	if err := g.verify(dir); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRefusesForeignPath(t *testing.T) {
	for _, path := range []string{
		"/etc/passwd",
		"/run/systemd/network/10-netplan-eth0.network",
		"/run/systemd/network/50-andaru-x/../../etc/y.network",
		"/run/systemd/system/andaru-pppoe-x.service",
		"/run/systemd/network/50-andaru-a.conf",
	} {
		dir := Dir(t.TempDir())
		_, err := Create(dir, Manifest{Files: []FileEntry{{Path: path, Mode: "0644"}}}, map[string][]byte{path: []byte("x")})
		if err == nil || len(numbered(t, dir)) != 0 {
			t.Fatalf("%s: err=%v, left %v", path, err, numbered(t, dir))
		}
	}
}

func TestCreateRefusesMode(t *testing.T) {
	for _, mode := range []string{"0666", "0755", ""} {
		dir := Dir(t.TempDir())
		path := netDir + "50-andaru-a.network"
		if _, err := Create(dir, Manifest{Files: []FileEntry{{Path: path, Mode: mode}}}, map[string][]byte{path: nil}); err == nil {
			t.Fatalf("mode %q accepted", mode)
		}
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	dir := Dir(t.TempDir())
	create(t, dir, Manifest{}, map[string]string{"50-andaru-a.network": "good"})
	g, _ := Read(dir, 1)
	stored := filepath.Join(dir, "1", "files", "50-andaru-a.network")
	writeFile(t, stored, "evil")
	if g.verify(dir) == nil {
		t.Fatal("changed content verified")
	}
	os.Remove(stored)
	if g.verify(dir) == nil {
		t.Fatal("missing file verified")
	}
}

func TestTornFilesAreErrors(t *testing.T) {
	dir := Dir(t.TempDir())
	create(t, dir, Manifest{}, nil)
	writeFile(t, filepath.Join(dir, "1", "state.json"), "{")
	if _, err := Read(dir, 1); err == nil {
		t.Fatal("torn state.json read")
	}
	os.Remove(filepath.Join(dir, "1", "state.json"))
	writeFile(t, filepath.Join(dir, "pointers.json"), `{"confirmed":`)
	if _, _, err := List(dir); err == nil {
		t.Fatal("torn pointers.json listed")
	}
}

func TestListIgnoresStrangers(t *testing.T) {
	dir := Dir(t.TempDir())
	if p, items, err := List(dir); err != nil || len(items) != 0 || p != (Pointers{}) {
		t.Fatalf("absent directory: %v %v %v", p, items, err)
	}
	create(t, dir, Manifest{}, nil)
	create(t, dir, Manifest{}, nil)
	for _, name := range []string{"abc", "0", "-1", "01", "baseline"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "7"), "")
	writeFile(t, filepath.Join(dir, "lock"), "")
	_, items, err := List(dir)
	if err != nil || len(items) != 2 || items[0].ID != 2 || items[1].ID != 1 {
		t.Fatalf("items = %+v, err = %v", items, err)
	}
}

func TestListSkipsIncompleteGeneration(t *testing.T) {
	dir := Dir(t.TempDir())
	create(t, dir, Manifest{}, nil)
	if err := os.Mkdir(filepath.Join(dir, "3"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, items, err := List(dir); err != nil || len(items) != 1 {
		t.Fatalf("items = %+v, err = %v", items, err)
	}
	if id := create(t, dir, Manifest{}, nil); id != 4 {
		t.Fatalf("id = %d, want 4", id)
	}
}

func TestReadReconcilesWithPointers(t *testing.T) {
	dir := Dir(t.TempDir())
	create(t, dir, Manifest{}, nil)
	for _, state := range []string{Awaiting, Activating} {
		writeFile(t, filepath.Join(dir, "1", "state.json"), `{"state":"`+state+`","previous":null}`)
		writeFile(t, filepath.Join(dir, "pointers.json"), `{"confirmed":null,"lastKnownGood":null,"pending":null,"live":null}`)
		if g, err := Read(dir, 1); err != nil || g.State.State != RolledBack || g.Reason != "interrupted" {
			t.Fatalf("%s without pending = %+v, %v", state, g.State, err)
		}
		writeFile(t, filepath.Join(dir, "pointers.json"), `{"confirmed":null,"lastKnownGood":null,"pending":1,"live":1}`)
		if g, _ := Read(dir, 1); g.State.State != state {
			t.Fatalf("%s while pending = %+v", state, g.State)
		}
	}
	writeFile(t, filepath.Join(dir, "pointers.json"), `{"confirmed":1,"lastKnownGood":null,"pending":null,"live":1}`)
	if g, _ := Read(dir, 1); g.State.State != Confirmed {
		t.Fatalf("confirmed pointer = %+v", g.State)
	}
}

// Package generation holds rendered network generations on disk and the
// privileged verbs that make one live. Boot and rollback read these files
// alone: no database, no engine.
package generation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"
)

const (
	Activating = "activating"
	Awaiting   = "awaiting_confirmation"
	Confirmed  = "confirmed"
	RolledBack = "rolled_back"
	Failed     = "failed"
)

type FileEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
}

// Manifest is written once by the engine and never changed.
type Manifest struct {
	// Baseline is what a takeover generation replaces: "netplan" or "ifupdown"; empty on other generations.
	Baseline              string      `json:"baseline,omitempty"`
	ID                    int         `json:"id"`
	SavedVersion          string      `json:"savedVersion"`
	CreatedAt             time.Time   `json:"createdAt"`
	Takeover              bool        `json:"takeover"`
	ConfirmTimeoutSeconds int         `json:"confirmTimeoutSeconds"`
	Files                 []FileEntry `json:"files"`
}

// State is the record realize keeps; the pointers decide.
type State struct {
	State string `json:"state"`
	// Previous is what was live when this generation was activated; nil is the pre-Andaru baseline.
	Previous *int       `json:"previous"`
	Deadline *time.Time `json:"deadline,omitempty"`
	Reason   string     `json:"reason,omitempty"`
}

// Pointers is the single commit point. A nil Live means the baseline is in /run.
type Pointers struct {
	Confirmed     *int `json:"confirmed"`
	LastKnownGood *int `json:"lastKnownGood"`
	Pending       *int `json:"pending"`
	Live          *int `json:"live"`
	// Takeover records that a takeover was confirmed, so boot keeps netplan and cloud-init out for good.
	Takeover bool `json:"takeover"`
}

type Generation struct {
	Manifest
	State
}

var (
	ownPath = regexp.MustCompile(`^/run/systemd/network/50-andaru-[^/]+\.(network|netdev)$`)
	idName  = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
)

// Dir is where generations live under the engine's state directory.
func Dir(stateDir string) string { return filepath.Join(stateDir, "generations") }

func allowed(path string) bool { return ownPath.MatchString(path) }

func is(p *int, id int) bool { return p != nil && *p == id }

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// writeAtomic leaves either the old file or the whole new one, also across a power loss.
func writeAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	// The dot prefix keeps networkd from parsing a half-written file.
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'), 0o600)
}

// readJSON reports whether the file exists; unparsable content is an error, never a guess.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return true, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func ids(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		if e.IsDir() && idName.MatchString(e.Name()) {
			n, _ := strconv.Atoi(e.Name())
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Create allocates the next number and stores the files and manifest. It writes no state and no pointer.
func Create(dir string, m Manifest, contents map[string][]byte) (int, error) {
	for i, f := range m.Files {
		data, ok := contents[f.Path]
		if !ok || !allowed(f.Path) || f.Mode != "0644" {
			return 0, fmt.Errorf("generation file %q (mode %q) is not one Andaru may write", f.Path, f.Mode)
		}
		m.Files[i].SHA256 = sum(data)
	}
	if m.Files == nil {
		m.Files = []FileEntry{}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	for {
		existing, err := ids(dir)
		if err != nil {
			return 0, err
		}
		m.ID = 1
		if len(existing) > 0 {
			m.ID = existing[len(existing)-1] + 1
		}
		own := filepath.Join(dir, strconv.Itoa(m.ID))
		// Mkdir is the claim: whoever creates the directory owns the number.
		if err := os.Mkdir(own, 0o700); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return 0, err
		}
		if err := fill(own, m, contents); err != nil {
			_ = os.RemoveAll(own)
			return 0, err
		}
		return m.ID, syncDir(dir)
	}
}

func fill(own string, m Manifest, contents map[string][]byte) error {
	if err := os.Mkdir(filepath.Join(own, "files"), 0o700); err != nil {
		return err
	}
	for _, f := range m.Files {
		if err := writeAtomic(filepath.Join(own, "files", filepath.Base(f.Path)), contents[f.Path], 0o600); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(own, "manifest.json"), m)
}

func readPointers(dir string) (Pointers, error) {
	var p Pointers
	_, err := readJSON(filepath.Join(dir, "pointers.json"), &p)
	return p, err
}

func read(dir string, id int, p Pointers) (Generation, error) {
	var g Generation
	own := filepath.Join(dir, strconv.Itoa(id))
	found, err := readJSON(filepath.Join(own, "manifest.json"), &g.Manifest)
	if err != nil {
		return g, err
	}
	if !found || g.ID != id {
		return g, fmt.Errorf("generation %d: %w", id, fs.ErrNotExist)
	}
	found, err = readJSON(filepath.Join(own, "state.json"), &g.State)
	if err != nil {
		return g, err
	}
	switch {
	case !found:
		g.State = State{State: Failed, Reason: "never started"}
	case is(p.Confirmed, id):
		// A kill between confirm's two writes leaves the record behind the pointer.
		g.State.State = Confirmed
	case (g.State.State == Activating || g.State.State == Awaiting) && !is(p.Pending, id):
		g.State.State, g.Reason = RolledBack, "interrupted"
	}
	return g, nil
}

// Read returns one generation as the pointers say it stands; fs.ErrNotExist when there is none.
func Read(dir string, id int) (Generation, error) {
	p, err := readPointers(dir)
	if err != nil {
		return Generation{}, err
	}
	return read(dir, id, p)
}

// List returns the pointers and every complete generation, newest first.
func List(dir string) (Pointers, []Generation, error) {
	p, err := readPointers(dir)
	if err != nil {
		return p, nil, err
	}
	all, err := ids(dir)
	if err != nil {
		return p, nil, err
	}
	items := []Generation{}
	for _, id := range slices.Backward(all) {
		g, err := read(dir, id, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue // claimed but never filled
		}
		if err != nil {
			return p, nil, err
		}
		items = append(items, g)
	}
	return p, items, nil
}

// content returns one stored file after checking it against the manifest.
func (g Generation) content(dir string, f FileEntry) ([]byte, error) {
	if !allowed(f.Path) || f.Mode != "0644" {
		return nil, fmt.Errorf("generation %d names %q, which Andaru may not write", g.ID, f.Path)
	}
	data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(g.ID), "files", filepath.Base(f.Path)))
	if err != nil {
		return nil, err
	}
	if sum(data) != f.SHA256 {
		return nil, fmt.Errorf("generation %d: %s does not match its manifest", g.ID, f.Path)
	}
	return data, nil
}

func (g Generation) verify(dir string) error {
	for _, f := range g.Files {
		if _, err := g.content(dir, f); err != nil {
			return err
		}
	}
	return nil
}

// Contents returns a generation's files by destination path, each checked against the manifest.
func Contents(dir string, g Generation) (map[string][]byte, error) {
	out := make(map[string][]byte, len(g.Files))
	for _, f := range g.Files {
		data, err := g.content(dir, f)
		if err != nil {
			return nil, err
		}
		out[f.Path] = data
	}
	return out, nil
}

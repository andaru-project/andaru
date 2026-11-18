// Package reset runs the root-local factory reset: it wipes /var/lib/andaru and brings the box up on the kept
// recovery copy, else on the last LAN, with one admin account. Every phase is journaled outside the wiped tree
// and resumes after a crash or a power loss.
package reset

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
)

// Dir holds the journal and the fence lock. It lives on the root disk, outside both the wiped tree and the kept copies.
const Dir = "/var/lib/andaru-reset"

// MarkerName is the file inside the state tree that names the operation; the wipe keeps it until done.
const MarkerName = ".andaru-reset"

type Phase string

const (
	Planned          Phase = "planned"
	Stopped          Phase = "stopped"
	TornDown         Phase = "torn-down"
	Wiped            Phase = "wiped"
	Fresh            Phase = "fresh"
	ApplyingRecovery Phase = "applying-recovery"
	ApplyingLastLAN  Phase = "applying-last-lan"
	Up               Phase = "up"
	ConsoleRequired  Phase = "console-required"
)

// order ranks the phases; a resume never moves backwards.
var order = map[Phase]int{Planned: 0, Stopped: 1, TornDown: 2, Wiped: 3, Fresh: 4, ApplyingRecovery: 5, ApplyingLastLAN: 6, Up: 7, ConsoleRequired: 7}

// Operation is the journal. The verifier is the new admin's password hash, never the password.
type Operation struct {
	ID        string               `json:"id"`
	Phase     Phase                `json:"phase"`
	Ownership generation.Ownership `json:"ownership"`
	Verifier  []byte               `json:"verifier"`
	Backup    bool                 `json:"backup"`
	Reboot    bool                 `json:"reboot"`
	// Units are Andaru's units that were active before the reset; only they start again.
	Units   []string  `json:"units"`
	Started time.Time `json:"started"`
}

func journalPath(dir string) string { return filepath.Join(dir, "operation.json") }

// Active reports whether a reset is in progress, and its phase.
func Active(dir string) (bool, Phase, error) {
	op, err := readJournal(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return true, "", err
	}
	return true, op.Phase, nil
}

// BeforeFresh reports a phase in which the old tree is still being stopped, torn down or wiped:
// nothing but the reset may run then.
func BeforeFresh(p Phase) bool { return order[p] < order[Fresh] }

func readJournal(dir string) (Operation, error) {
	var op Operation
	data, err := readPrivate(journalPath(dir))
	if err != nil {
		return op, err
	}
	if err = json.Unmarshal(data, &op); err != nil {
		return op, fmt.Errorf("read the reset journal: %w", err)
	}
	if _, ok := order[op.Phase]; !ok || op.ID == "" {
		return op, errors.New("the reset journal is not valid")
	}
	return op, nil
}

func writeJournal(dir string, op Operation) error {
	if err := privateDir(dir); err != nil {
		return err
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return writeSynced(journalPath(dir), data)
}

func removeJournal(dir string) error {
	if err := os.Remove(journalPath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(dir)
}

func writeMarker(stateDir, id string) error {
	return writeSynced(filepath.Join(stateDir, MarkerName), []byte(id))
}

func checkMarker(stateDir, id string) error {
	data, err := readPrivate(filepath.Join(stateDir, MarkerName))
	if err != nil {
		return fmt.Errorf("the state directory is not the one this reset started on (is its mount missing?): %w", err)
	}
	if string(data) != id {
		return errors.New("the state directory belongs to another reset")
	}
	return nil
}

func removeMarker(stateDir string) error {
	if err := os.Remove(filepath.Join(stateDir, MarkerName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(stateDir)
}

// privateDir makes dir a root-owned 0700 directory, or checks that it is one.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be a root-owned private directory", dir)
	}
	return nil
}

// readPrivate reads a root-owned 0600 regular file with one link, never following a symlink.
func readPrivate(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != 0 || stat.Nlink != 1 || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s is not a private regular file", path)
	}
	return os.ReadFile(path)
}

func writeSynced(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

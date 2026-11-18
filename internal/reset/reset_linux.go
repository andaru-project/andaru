package reset

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/identity"
	"github.com/andaru-project/andaru/internal/kept"
	"github.com/andaru-project/andaru/internal/upgrade"
)

// Env is everything a reset touches. Production builds it in cmd/andaru.
type Env struct {
	StateDir string // /var/lib/andaru, the wiped tree
	Dir      string // the journal directory, Dir in production
	KeptRoot string // "" uses kept.Root
	Realizer generation.Realizer
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	// PackageLocks are dpkg's frontend and database locks, held from the first stop to the end.
	PackageLocks []string
	LockWait     time.Duration
	// Apply applies a saved version through the engine and confirms it; true when it came up and was confirmed.
	Apply func(ctx context.Context, savedVersion string) (bool, error)
	// failAt lets tests stop the reset before a named step, as a crash would.
	failAt func(step string) error
}

func (e Env) at(step string) error {
	if e.failAt != nil {
		return e.failAt(step)
	}
	return nil
}

var root = authz.Actor{Kind: authz.ActorSystem, ID: "root"}

// Plan checks that a reset may start, then writes the marker and the planned journal. It stops and deletes nothing.
func Plan(ctx context.Context, e Env, verifier []byte, backup, reboot bool) (Operation, error) {
	if len(verifier) == 0 {
		return Operation{}, errors.New("the reset needs the new administrator's verifier")
	}
	if err := privateDir(e.Dir); err != nil {
		return Operation{}, err
	}
	fence, err := lockFile(filepath.Join(e.Dir, "lock"))
	if err != nil {
		return Operation{}, fmt.Errorf("a factory reset is running: %w", err)
	}
	defer fence.Close()
	if _, err := readJournal(e.Dir); err == nil {
		return Operation{}, errors.New("a factory reset is already in progress; see andaru ctl factory-reset status")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Operation{}, err
	}
	if err := check(ctx, e); err != nil {
		return Operation{}, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Operation{}, err
	}
	op := Operation{ID: hex.EncodeToString(id), Phase: Planned, Verifier: verifier, Backup: backup, Reboot: reboot, Started: time.Now().UTC()}
	// The marker goes first: a crash before the journal leaves only a stale marker, which the next plan replaces.
	if err := writeMarker(e.StateDir, op.ID); err != nil {
		return Operation{}, err
	}
	if err := e.at("plan:journal"); err != nil {
		return Operation{}, err
	}
	return op, writeJournal(e.Dir, op)
}

func check(ctx context.Context, e Env) error {
	if !kept.LiveTreeUsable(e.StateDir) {
		return fmt.Errorf("%s holds no Andaru state; is its mount missing?", e.StateDir)
	}
	return upgrade.CheckIdle(ctx, e.Run, e.StateDir)
}

// Abort drops a reset that has not stopped anything yet.
func Abort(e Env) error {
	fence, err := lockFile(filepath.Join(e.Dir, "lock"))
	if err != nil {
		return fmt.Errorf("the factory reset is running; it can no longer be aborted: %w", err)
	}
	defer fence.Close()
	op, err := readJournal(e.Dir)
	if err != nil {
		return err
	}
	if op.Phase != Planned {
		return fmt.Errorf("the reset is at %s; only a planned reset can be aborted", op.Phase)
	}
	return drop(e)
}

// drop deletes the journal before the marker, so a crash between them leaves no journal without its marker.
func drop(e Env) error {
	if err := removeJournal(e.Dir); err != nil {
		return err
	}
	if err := e.at("drop:marker"); err != nil {
		return err
	}
	return removeMarker(e.StateDir)
}

// Resume runs the journaled reset from its phase to the end. The reset unit calls it, at the start and after a boot.
func Resume(ctx context.Context, e Env) (Operation, error) {
	if err := privateDir(e.Dir); err != nil {
		return Operation{}, err
	}
	fence, err := lockFile(filepath.Join(e.Dir, "lock"))
	if err != nil {
		return Operation{}, fmt.Errorf("another reset is running: %w", err)
	}
	defer fence.Close()
	op, err := readJournal(e.Dir)
	if err != nil {
		return Operation{}, err
	}
	if err = checkMarker(e.StateDir, op.ID); err != nil {
		return op, err
	}
	held, err := holdPackageLocks(ctx, e.PackageLocks, e.LockWait)
	if err != nil {
		return op, err
	}
	defer func() {
		for _, f := range held {
			f.Close()
		}
	}()
	// A package operation may have run while the locks were free, before a boot or after a crash.
	if err = upgrade.DpkgConsistent(ctx, e.Run); err != nil {
		return op, err
	}
	// entered is the phase this run moved into; a phase found in the journal was interrupted.
	var entered Phase
	advance := func(p Phase) error {
		if err := e.at(string(p)); err != nil {
			return err
		}
		op.Phase, entered = p, p
		return writeJournal(e.Dir, op)
	}
	for {
		switch op.Phase {
		case Planned:
			if err := check(ctx, e); err != nil {
				return op, errors.Join(err, drop(e))
			}
			op.Units = nil
			for _, unit := range upgrade.WriterUnits() {
				if _, err := e.Run(ctx, "systemctl", "is-active", "--quiet", unit); err == nil {
					op.Units = append(op.Units, unit)
				}
			}
			if err := upgrade.StopWriters(ctx, e.Run); err != nil {
				return op, err
			}
			if op.Ownership, err = e.Realizer.ReadOwnership(); err != nil {
				return op, err
			}
			err = advance(Stopped)
		case Stopped:
			if err = upgrade.StopWriters(ctx, e.Run); err == nil {
				err = e.Realizer.Teardown(ctx)
			}
			if err == nil {
				err = advance(TornDown)
			}
		case TornDown:
			if err = upgrade.StopWriters(ctx, e.Run); err == nil {
				err = wipe(e.StateDir, MarkerName, func() error { return e.at("wipe:entry") })
			}
			if err == nil {
				err = advance(Wiped)
			}
		case Wiped:
			// Fresh stores are all or nothing: whatever an interrupted attempt left goes first.
			if err = wipe(e.StateDir, MarkerName, nil); err == nil {
				err = e.fresh(ctx, op)
			}
			if err == nil {
				err = advance(Fresh)
			}
		case Fresh:
			switch {
			case !op.Ownership.Takeover:
				// Andaru never took this host's networking over, so there is none of its own to bring up.
				err = advance(Up)
			case e.readable("recovery"):
				err = advance(ApplyingRecovery)
			default:
				err = advance(ApplyingLastLAN)
			}
		case ApplyingRecovery, ApplyingLastLAN:
			next := map[Phase]Phase{ApplyingRecovery: ApplyingLastLAN, ApplyingLastLAN: ConsoleRequired}[op.Phase]
			name := map[Phase]kept.Name{ApplyingRecovery: "recovery", ApplyingLastLAN: "last-lan"}[op.Phase]
			if entered == op.Phase {
				if err = e.at("apply:" + string(name)); err != nil {
					return op, err
				}
			}
			switch {
			case e.confirmedLive():
				err = advance(Up)
			case entered == op.Phase && e.applyCopy(ctx, name):
				err = advance(Up)
			default:
				// A copy that did not come up, or whose attempt a crash interrupted, is not tried again. An apply it
				// left pending would refuse the next one, so it is rolled back first.
				e.rollbackPending(ctx)
				err = advance(next)
			}
		case Up, ConsoleRequired:
			return op, e.finish(ctx, op)
		}
		if err != nil {
			return op, err
		}
	}
}

// fresh writes the new tree: the ownership record, empty configuration, the admin account and the first audit event.
func (e Env) fresh(ctx context.Context, op Operation) error {
	if err := e.Realizer.WriteOwnership(op.Ownership); err != nil {
		return err
	}
	cs, err := config.Open(ctx, e.StateDir)
	if err != nil {
		return err
	}
	if err = cs.Close(); err != nil {
		return err
	}
	ids, err := identity.Open(ctx, e.StateDir)
	if err != nil {
		return err
	}
	_, _, err = ids.BootstrapResetAdmin(ctx, op.Verifier)
	if err = errors.Join(err, ids.Close()); err != nil {
		return err
	}
	if err = e.at("fresh:audit"); err != nil {
		return err
	}
	audits, err := audit.Open(ctx, e.StateDir)
	if err != nil {
		return err
	}
	reason := audit.ReasonFactoryResetBackupSkipped
	if op.Backup {
		reason = audit.ReasonFactoryResetBackupWritten
	}
	// A safety event: its receipt is written even when the database refuses it, and the reset goes on either way.
	if err := audits.AppendSafety(ctx, audit.Event{
		Actor: root, Source: "local", Action: audit.ActionRecoveryCompleted, Result: audit.ResultRecovered,
		Category: audit.CategoryProtected, Metadata: map[string]string{audit.MetadataReasonCode: reason},
	}); err != nil {
		slog.Error("record the factory reset", "error", err)
	}
	return audits.Close()
}

func (e Env) kept() (*kept.Store, error) {
	if e.KeptRoot == "" {
		return kept.Open(e.StateDir)
	}
	return kept.OpenIn(e.KeptRoot, e.StateDir)
}

func (e Env) readable(name kept.Name) bool {
	store, err := e.kept()
	if err == nil {
		_, err = store.Read(name)
	}
	return err == nil
}

// applyCopy saves a kept copy as the next saved version and has the engine apply and confirm it.
func (e Env) applyCopy(ctx context.Context, name kept.Name) bool {
	err := func() error {
		store, err := e.kept()
		if err != nil {
			return err
		}
		body, err := store.Read(name)
		if err != nil {
			return err
		}
		docs, err := config.KeptDeclaration(body)
		if err != nil {
			return err
		}
		// The store takes the engine's lock, so the engine is stopped while the copy is saved.
		if _, err = e.Run(ctx, "systemctl", "stop", "andaru-engine.service"); err != nil {
			return err
		}
		cs, err := config.Open(ctx, e.StateDir)
		if err != nil {
			return err
		}
		head, err := cs.Export(ctx, false)
		var saved config.RestoreResult
		if err == nil {
			saved, err = cs.Restore(ctx, root, head.SavedVersion, docs)
		}
		if err = errors.Join(err, cs.Close()); err != nil {
			return err
		}
		if err = e.startEngine(ctx); err != nil {
			return err
		}
		up, err := e.Apply(ctx, string(saved.SavedVersion))
		if err == nil && !up {
			err = errors.New("the apply did not come up")
		}
		return err
	}()
	if err != nil {
		slog.Error("factory reset: kept copy not applied", "copy", name, "error", err)
		return false
	}
	return e.confirmedLive()
}

func (e Env) confirmedLive() bool {
	p, _, err := generation.List(generation.Dir(e.StateDir))
	return err == nil && p.Confirmed != nil && p.Live != nil && *p.Confirmed == *p.Live
}

func (e Env) rollbackPending(ctx context.Context) {
	p, _, err := generation.List(generation.Dir(e.StateDir))
	if err == nil && p.Pending != nil {
		err = e.Realizer.Rollback(ctx, *p.Pending, "factory reset: the copy did not come up")
	}
	if err != nil {
		slog.Error("factory reset: roll back the unconfirmed apply", "error", err)
	}
}

func (e Env) startEngine(ctx context.Context) error {
	if _, err := e.Run(ctx, "systemctl", "reset-failed", "andaru-engine.service"); err != nil {
		slog.Warn("reset the engine unit", "error", err)
	}
	_, err := e.Run(ctx, "systemctl", "start", "andaru-engine.service")
	return err
}

// finish starts Andaru's units on the fresh state, drops the journal and the marker, and reboots when asked.
func (e Env) finish(ctx context.Context, op Operation) error {
	var failures []error
	if op.Phase == ConsoleRequired {
		// Nothing came up: nothing but the console may reach the host.
		if err := e.Realizer.DenyAll(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	// The engine always runs, so the console has the root CLI; the rest only when it ran before.
	units := []string{"andaru-engine.service"}
	for _, unit := range upgrade.WriterUnits() {
		if unit != "andaru-engine.service" && slices.Contains(op.Units, unit) {
			units = append(units, unit)
		}
	}
	for _, unit := range units {
		if _, err := e.Run(ctx, "systemctl", "reset-failed", unit); err != nil {
			slog.Warn("reset unit", "unit", unit, "error", err)
		}
		if _, err := e.Run(ctx, "systemctl", "start", unit); err != nil {
			failures = append(failures, fmt.Errorf("start %s: %w", unit, err))
		}
	}
	if err := e.at("finish:drop"); err != nil {
		return err
	}
	if err := drop(e); err != nil {
		return errors.Join(append(failures, err)...)
	}
	if op.Reboot {
		if _, err := e.Run(ctx, "systemctl", "reboot"); err != nil {
			failures = append(failures, err)
		}
	}
	if op.Phase == ConsoleRequired {
		failures = append(failures, errors.New("CONSOLE_REQUIRED: neither the recovery copy nor the last LAN came up; log in at the console as admin"))
	}
	return errors.Join(failures...)
}

func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// holdPackageLocks takes dpkg's locks the way dpkg does, with fcntl, so no package operation runs while they are held.
// A lock file that does not exist means no dpkg on this root, as in tests.
func holdPackageLocks(ctx context.Context, paths []string, wait time.Duration) ([]*os.File, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var held []*os.File
	release := func() {
		for _, f := range held {
			f.Close()
		}
	}
	for _, path := range paths {
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			release()
			return nil, err
		}
		held = append(held, f)
		for {
			lock := syscall.Flock_t{Type: syscall.F_WRLCK}
			err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock)
			if err == nil {
				break
			}
			if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EACCES) {
				release()
				return nil, err
			}
			select {
			case <-ctx.Done():
				release()
				return nil, errors.New("an APT or dpkg operation holds its lock; wait for it to finish")
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return held, nil
}

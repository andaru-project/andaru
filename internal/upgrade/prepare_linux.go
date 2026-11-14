package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/securefs"
)

type environment struct {
	stateDir, recoveryDir string
	commands              func(string) commandRunner
	now                   func() time.Time
	packageLocks          []string
	bootID                string
	pauseTimeout          time.Duration
}

type preparationRecord struct {
	FormatVersion int         `json:"formatVersion"`
	BootID        string      `json:"bootId"`
	Version       string      `json:"version"`
	Stage         string      `json:"stage"`
	Units         []unitState `json:"units"`
}

var preparationUnits = []string{"andaru-leases.path", "andaru-prefixes.path", "andaru-prefixes.timer", "andaru-engine.service"}
var realizationUnits = []string{"andaru-leases.service", "andaru-prefixes.service"}

func Prepare(ctx context.Context) (Preparation, error) {
	if os.Geteuid() != 0 {
		return Preparation{}, errors.New("recovery preparation requires root")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Preparation{}, err
	}
	return prepare(ctx, environment{stateDir: "/var/lib/andaru", recoveryDir: "/var/lib/andaru-upgrades", commands: runnerAt, now: time.Now, bootID: strings.TrimSpace(string(boot)), pauseTimeout: 30 * time.Second, packageLocks: []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock"}})
}

func prepare(ctx context.Context, env environment) (result Preparation, retErr error) {
	if os.Geteuid() != 0 {
		return result, errors.New("recovery preparation requires root")
	}
	if err := privateDir(env.stateDir); err != nil {
		return result, err
	}
	relative, err := filepath.Rel(env.stateDir, env.recoveryDir)
	if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return result, errors.New("recovery directory cannot be inside application state")
	}
	if !filepath.IsAbs(env.recoveryDir) || filepath.Clean(env.recoveryDir) != env.recoveryDir {
		return result, errors.New("unsafe recovery directory")
	}
	if err = securefs.CheckAncestors(filepath.Dir(env.recoveryDir)); err != nil {
		return result, err
	}
	if err = os.Mkdir(env.recoveryDir, 0700); err != nil && !os.IsExist(err) {
		return result, err
	}
	if err = privateDir(env.recoveryDir); err != nil {
		return result, err
	}
	operationCtx, operationCancel := context.WithTimeout(ctx, time.Second)
	defer operationCancel()
	operation, err := acquireFileLock(operationCtx, filepath.Join(env.recoveryDir, "prepare.lock"))
	if err != nil {
		return result, fmt.Errorf("another preparation is active: %w", err)
	}
	defer operation.Close()
	if _, err = os.Lstat(filepath.Join(env.recoveryDir, "preparing.json")); err == nil {
		return result, errors.New("interrupted preparation: inspect /var/lib/andaru-upgrades/preparing.json and the recorded units before retrying")
	}
	if !os.IsNotExist(err) {
		return result, err
	}
	if err = packageActivity(env.packageLocks); err != nil {
		return result, err
	}
	run := env.commands("")
	audit, err := run(ctx, "dpkg", "--audit")
	if err != nil {
		return result, fmt.Errorf("inspect dpkg state: %w", err)
	}
	if err = textOutput(audit); err != nil {
		return result, err
	}
	if len(strings.TrimSpace(string(audit))) > 0 {
		return result, errors.New("dpkg has incomplete packages; repair them before preparing recovery")
	}
	if err = idleApplications(env.stateDir); err != nil {
		return result, err
	}
	info, err := installedPackage(ctx, run)
	if err != nil {
		return result, err
	}
	inventory, err := run(ctx, "dpkg-query", "-W", "-f=${Package}\t${Version}\t${Architecture}\n")
	if err != nil {
		return result, fmt.Errorf("read dependency inventory: %w", err)
	}
	if err = textOutput(inventory); err != nil {
		return result, err
	}
	if err = checkSpace(ctx, env.stateDir, env.recoveryDir); err != nil {
		return result, err
	}
	stage, err := newRecoveryStage(env.recoveryDir)
	if err != nil {
		return result, err
	}
	var paused []unitState
	var locks []*os.File
	markerWritten := false
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			retErr = errors.Join(retErr, locks[i].Close())
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var resumeErr error
		for i := len(paused) - 1; i >= 0; i-- {
			current, err := inspectUnit(cleanupCtx, run, paused[i].Name)
			if err != nil {
				resumeErr = errors.Join(resumeErr, err)
				continue
			}
			if current.EnabledState == "disabled" || strings.HasPrefix(current.EnabledState, "masked") {
				resumeErr = errors.Join(resumeErr, fmt.Errorf("%s is now disabled or masked; left stopped", current.Name))
				continue
			}
			// These units were healthy before our own stop/start cycles consumed their start budget.
			if _, err = run(cleanupCtx, "systemctl", "reset-failed", paused[i].Name); err != nil {
				resumeErr = errors.Join(resumeErr, fmt.Errorf("reset paused unit %s: %w", paused[i].Name, err))
				continue
			}
			if _, err = run(cleanupCtx, "systemctl", "start", paused[i].Name); err != nil {
				resumeErr = errors.Join(resumeErr, fmt.Errorf("restore %s: %w", paused[i].Name, err))
			}
		}
		if markerWritten && resumeErr == nil {
			if err := os.Remove(filepath.Join(env.recoveryDir, "preparing.json")); err != nil {
				resumeErr = errors.Join(resumeErr, err)
			} else {
				resumeErr = errors.Join(resumeErr, syncDirectory(env.recoveryDir))
			}
		}
		retErr = errors.Join(retErr, resumeErr, removeStage(env.recoveryDir, stage))
	}()
	previous, err := retainedLocalPackage(ctx, env.recoveryDir, info, stage)
	if err == nil && !previous.Available {
		previous, err = retainPackage(ctx, env.commands(stage), info, stage)
	}
	if err != nil {
		return result, err
	}
	if previous.Available {
		if err = os.Rename(previous.Path, filepath.Join(stage, "package.deb")); err != nil {
			return result, err
		}
	}
	if err = checkSpace(ctx, env.stateDir, env.recoveryDir); err != nil {
		return result, err
	}
	var units []unitState
	for _, name := range preparationUnits {
		state, err := inspectUnit(ctx, run, name)
		if err != nil {
			return result, err
		}
		if state.WasActive && (state.EnabledState == "disabled" || strings.HasPrefix(state.EnabledState, "masked")) {
			return result, fmt.Errorf("cannot safely pause active disabled/masked unit %s", name)
		}
		units = append(units, state)
	}
	if err = idleApplications(env.stateDir); err != nil {
		return result, err
	}
	record := preparationRecord{FormatVersion: 1, BootID: env.bootID, Version: info.Version, Stage: filepath.Base(stage), Units: units}
	if err = atomicJSON(filepath.Join(env.recoveryDir, "preparing.json"), record); err != nil {
		return result, err
	}
	markerWritten = true
	pauseCtx, cancel := context.WithTimeout(ctx, env.pauseTimeout)
	defer cancel()
	for _, unit := range units {
		if !unit.WasActive {
			continue
		}
		// A failed/cancelled stop may already have stopped the unit; include it in cleanup before issuing the command.
		paused = append(paused, unit)
		if _, err = run(pauseCtx, "systemctl", "stop", unit.Name); err != nil {
			return result, fmt.Errorf("pause %s: %w", unit.Name, err)
		}
	}
	if err = drainRealizers(pauseCtx, run); err != nil {
		return result, err
	}
	lock, err := acquireFileLock(pauseCtx, filepath.Join(env.stateDir, "engine.lock"))
	if err != nil {
		return result, err
	}
	locks = append(locks, lock)
	generationDir := filepath.Join(env.stateDir, "generations")
	if _, err = os.Lstat(generationDir); err == nil {
		if err = securefs.CheckAncestors(generationDir); err != nil {
			return result, err
		}
		lock, err = acquireFileLock(pauseCtx, filepath.Join(generationDir, "lock"))
		if err != nil {
			return result, err
		}
		locks = append(locks, lock)
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err = idleApplications(env.stateDir); err != nil {
		return result, err
	}
	if err = packageActivity(env.packageLocks); err != nil {
		return result, err
	}
	files, err := copyState(ctx, env.stateDir, filepath.Join(stage, "state"))
	if err != nil {
		return result, err
	}
	manifest := recoveryManifest{FormatVersion: 1, PreparedAt: env.now().UTC(), Package: info, PackageRetained: previous.Available, PackageSHA256: previous.SHA256, Dependencies: strings.Split(strings.TrimSpace(string(inventory)), "\n"), Files: files, Units: units}
	result, err = publishRecovery(ctx, env.recoveryDir, stage, manifest)
	return result, err
}

func packageActivity(paths []string) error {
	for _, path := range paths {
		file, _, err := regularFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
		err = syscall.FcntlFlock(file.Fd(), syscall.F_GETLK, &lock)
		closeErr := file.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return err
		}
		if lock.Type != syscall.F_UNLCK {
			return errors.New("APT/dpkg operation is active; wait for it to finish")
		}
	}
	return nil
}

func acquireFileLock(ctx context.Context, path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || !ok || stat.Uid != 0 || stat.Nlink != 1 || st.Mode().Perm() != 0600 {
		f.Close()
		return nil, errors.New("unsafe preparation lock")
	}
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func inspectUnit(ctx context.Context, run commandRunner, name string) (unitState, error) {
	raw, err := run(ctx, "systemctl", "show", "--property=ActiveState", "--property=UnitFileState", name)
	if err != nil {
		return unitState{}, fmt.Errorf("inspect %s: %w", name, err)
	}
	if err = textOutput(raw); err != nil {
		return unitState{}, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return unitState{}, errors.New("invalid systemd unit state")
		}
		if _, exists := fields[key]; exists {
			return unitState{}, errors.New("duplicate systemd unit state")
		}
		fields[key] = value
	}
	var active bool
	switch fields["ActiveState"] {
	case "active", "reloading":
		active = true
	case "inactive", "failed":
	case "activating", "deactivating":
		return unitState{}, fmt.Errorf("%s is changing state; retry preparation later", name)
	default:
		return unitState{}, errors.New("unknown systemd activation state")
	}
	enabled, exists := fields["UnitFileState"]
	if !exists {
		return unitState{}, errors.New("missing systemd enablement state")
	}
	return unitState{Name: name, WasActive: active, EnabledState: enabled}, nil
}

func drainRealizers(ctx context.Context, run commandRunner) error {
	for {
		allIdle := true
		for _, name := range realizationUnits {
			raw, err := run(ctx, "systemctl", "show", "--property=ActiveState", "--property=UnitFileState", name)
			if err != nil {
				return err
			}
			if err = textOutput(raw); err != nil {
				return err
			}
			active := ""
			for _, line := range strings.Split(string(raw), "\n") {
				if value, ok := strings.CutPrefix(line, "ActiveState="); ok {
					active = value
				}
			}
			switch active {
			case "inactive", "failed":
			case "active", "activating", "deactivating", "reloading":
				allIdle = false
			default:
				return errors.New("unknown realizer activation state")
			}
		}
		if allIdle {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for realization jobs: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func idleApplications(stateDir string) error {
	dir := filepath.Join(stateDir, "generations")
	pointer := filepath.Join(dir, "pointers.json")
	var raw map[string]json.RawMessage
	err := privateJSON(pointer, &raw)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read generation pointers: %w", err)
	}
	if err == nil {
		for _, key := range []string{"confirmed", "lastKnownGood", "pending", "live"} {
			value, ok := raw[key]
			if !ok {
				return errors.New("generation pointers are incomplete")
			}
			var id *int
			if err = json.Unmarshal(value, &id); err != nil || id != nil && *id <= 0 {
				return errors.New("invalid generation pointer")
			}
			if key == "pending" && id != nil {
				return errors.New("a connectivity apply is pending; confirm or roll it back before preparation")
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, e := strconv.Atoi(entry.Name())
		if e != nil || id <= 0 {
			continue
		}
		var state struct {
			State    string     `json:"state"`
			Previous *int       `json:"previous"`
			Deadline *time.Time `json:"deadline,omitempty"`
			Reason   string     `json:"reason,omitempty"`
		}
		err = privateJSON(filepath.Join(dir, entry.Name(), "state.json"), &state)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if state.State == "activating" || state.State == "awaiting_confirmation" || state.State == "rolling_back" {
			return errors.New("a connectivity apply is active; retry after it completes")
		}
	}
	return nil
}

func checkSpace(ctx context.Context, source, destination string) error {
	var bytes int64
	err := filepath.WalkDir(source, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, st, e := regularFile(path)
		if e != nil {
			return e
		}
		f.Close()
		if st.Size() > math.MaxInt64-bytes {
			return errors.New("state size overflow")
		}
		bytes += st.Size()
		return nil
	})
	if err != nil {
		return err
	}
	reserve := max(int64(64*1024*1024), bytes/10)
	if bytes > math.MaxInt64-reserve {
		return errors.New("state space estimate overflow")
	}
	var stat syscall.Statfs_t
	if err = syscall.Statfs(destination, &stat); err != nil {
		return err
	}
	required := uint64(bytes + reserve)
	if stat.Bsize <= 0 || stat.Bavail < required/uint64(stat.Bsize)+1 {
		return errors.New("insufficient free space for recovery state and filesystem reserve")
	}
	return nil
}

func removeStage(root, stage string) error {
	if filepath.Dir(stage) != root || !strings.HasPrefix(filepath.Base(stage), ".prepare-") {
		return errors.New("refusing to remove unowned staging")
	}
	if _, err := os.Lstat(stage); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := privateDir(stage); err != nil {
		return err
	}
	if err := verifyOwner(stage); err != nil {
		return err
	}
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	return syncDirectory(root)
}

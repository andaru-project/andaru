package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	bootcBinary = "/usr/bin/bootc"

	StateUnchecked   = "unchecked"
	StateUnsupported = "unsupported"
	StateUpToDate    = "up_to_date"
	StateAvailable   = "available"
	StateStaged      = "staged"
	StateChecking    = "checking"
	StateStaging     = "staging"
	StateRollingBack = "rolling_back"
	StateError       = "error"
)

// Deployment describes one bootc image deployment (booted, available, staged, or rollback).
type Deployment struct {
	Image     string     `json:"image,omitempty"`
	Version   string     `json:"version,omitempty"`
	Digest    string     `json:"digest,omitempty"`
	Timestamp *time.Time `json:"timestamp,omitempty"`
	Signature string     `json:"signature,omitempty"`
}

// Status is the persisted and API-served software update state for an Andaru appliance.
type Status struct {
	Supported      bool              `json:"supported"`
	State          string            `json:"state"`
	Booted         *Deployment       `json:"booted,omitempty"`
	Available      *Deployment       `json:"available,omitempty"`
	Download       *DownloadEstimate `json:"download,omitempty"`
	Progress       *Progress         `json:"progress,omitempty"`
	Staged         *Deployment       `json:"staged,omitempty"`
	Rollback       *Deployment       `json:"rollback,omitempty"`
	RollbackQueued bool              `json:"rollbackQueued,omitempty"`
	CheckedAt      *time.Time        `json:"checkedAt,omitempty"`
	Error          string            `json:"error,omitempty"`
	RebootNeeded   bool              `json:"rebootNeeded"`
}

// CommandRunner runs a host command and returns its combined or standard output.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager executes bootc update operations and persists status into StateDir/update/status.json.
type Manager struct {
	StateDir        string
	Run             CommandRunner
	RunWithProgress ProgressRunner
	CheckIdle       func(context.Context) error
	Now             func() time.Time
}

// DefaultCommandRunner executes host commands via os/exec.
func DefaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// StatusPath returns the canonical status cache path inside stateDir.
func StatusPath(stateDir string) string {
	return filepath.Join(stateDir, "update", "status.json")
}

// ReadStatus loads the cached update status from stateDir. If no cache exists yet, it returns
// an unchecked baseline so a fresh appliance prompts for or awaits its first check.
func ReadStatus(stateDir string) (Status, error) {
	raw, err := os.ReadFile(StatusPath(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Status{Supported: true, State: StateUnchecked}, nil
		}
		return Status{}, fmt.Errorf("read update status: %w", err)
	}
	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return Status{}, fmt.Errorf("parse update status: %w", err)
	}
	return st, nil
}

// WriteStatus persists status atomically inside stateDir/update/status.json (mode 0600).
func WriteStatus(stateDir string, st Status) error {
	return writeAtomicJSON(stateDir, StatusPath(stateDir), "status-*.tmp", st)
}

func writeAtomicJSON(stateDir, targetPath, tempPattern string, value any) error {
	dir := filepath.Join(stateDir, "update")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create update state dir: %w", err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update json: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return fmt.Errorf("create temp update file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp update file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp update file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp update file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp update file: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("publish update file: %w", err)
	}
	return nil
}

// bootc status --json subset (org.containers.bootc/v1).
type bootcStatusDoc struct {
	Status struct {
		Booted         *bootcDeploymentSlot `json:"booted"`
		Staged         *bootcDeploymentSlot `json:"staged"`
		Rollback       *bootcDeploymentSlot `json:"rollback"`
		RollbackQueued bool                 `json:"rollbackQueued"`
	} `json:"status"`
}

type bootcDeploymentSlot struct {
	Image        *bootcImageStatus `json:"image"`
	CachedUpdate *bootcImageStatus `json:"cachedUpdate"`
}

type bootcImageStatus struct {
	Image struct {
		Image     string          `json:"image"`
		Transport string          `json:"transport"`
		Signature json.RawMessage `json:"signature"`
	} `json:"image"`
	Version     string     `json:"version"`
	Timestamp   *time.Time `json:"timestamp"`
	ImageDigest string     `json:"imageDigest"`
}

func parseSignature(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		if _, ok := obj["ostreeRemote"]; ok {
			return "ostreeRemote"
		}
		for k := range obj {
			return k
		}
	}
	return ""
}

func toDeployment(img *bootcImageStatus) *Deployment {
	if img == nil {
		return nil
	}
	if img.Image.Image == "" && img.Version == "" && img.ImageDigest == "" {
		return nil
	}
	return &Deployment{
		Image:     img.Image.Image,
		Version:   img.Version,
		Digest:    img.ImageDigest,
		Timestamp: img.Timestamp,
		Signature: parseSignature(img.Image.Signature),
	}
}

// ParseBootcStatus parses `bootc status --json` output into an Andaru update Status.
func ParseBootcStatus(raw []byte) (Status, error) {
	var doc bootcStatusDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Status{}, fmt.Errorf("parse bootc status: %w", err)
	}
	if doc.Status.Booted == nil || doc.Status.Booted.Image == nil {
		return Status{Supported: false, State: StateUnsupported}, nil
	}
	st := Status{
		Supported:      true,
		Booted:         toDeployment(doc.Status.Booted.Image),
		Available:      toDeployment(doc.Status.Booted.CachedUpdate),
		RollbackQueued: doc.Status.RollbackQueued,
	}
	if doc.Status.Staged != nil {
		st.Staged = toDeployment(doc.Status.Staged.Image)
	}
	if doc.Status.Rollback != nil {
		st.Rollback = toDeployment(doc.Status.Rollback.Image)
	}
	// Ignore cachedUpdate when it points at the already-booted digest.
	if st.Available != nil && st.Booted != nil && st.Available.Digest != "" && st.Available.Digest == st.Booted.Digest {
		st.Available = nil
	}
	switch {
	case st.Staged != nil || st.RollbackQueued:
		st.State = StateStaged
		st.RebootNeeded = true
	case st.Available != nil:
		st.State = StateAvailable
	default:
		st.State = StateUpToDate
	}
	return st, nil
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m Manager) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}
	return DefaultCommandRunner(ctx, name, args...)
}

func isMissing(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

func (m Manager) ensureIdle(ctx context.Context, action string) error {
	if m.CheckIdle == nil {
		return nil
	}
	if err := m.CheckIdle(ctx); err != nil {
		return fmt.Errorf("cannot %s while system is busy: %w", action, err)
	}
	return nil
}

func (m Manager) readLiveBootc(ctx context.Context) (Status, error) {
	raw, err := m.run(ctx, bootcBinary, "status", "--json")
	if err != nil {
		if isMissing(err) {
			return Status{Supported: false, State: StateUnsupported}, nil
		}
		return Status{}, err
	}
	return ParseBootcStatus(raw)
}

func (m Manager) recordFailure(prev Status, err error, checkedAt *time.Time) Status {
	failed := prev
	if failed.Booted == nil && !failed.Supported {
		failed = Status{Supported: false}
	}
	failed.State = StateError
	failed.Error = err.Error()
	if checkedAt != nil {
		failed.CheckedAt = checkedAt
	}
	_ = WriteStatus(m.StateDir, failed)
	return failed
}

// Check runs `bootc upgrade --check`, reads `bootc status --json`, and persists the result.
func (m Manager) Check(ctx context.Context) (Status, error) {
	unlock, err := acquireLock(m.StateDir)
	if err != nil {
		return Status{}, err
	}
	defer unlock()

	prev, _ := ReadStatus(m.StateDir)
	now := m.now()

	checkOut, checkErr := m.run(ctx, bootcBinary, "upgrade", "--check")
	if checkErr != nil && isMissing(checkErr) {
		st := Status{Supported: false, State: StateUnsupported, CheckedAt: &now}
		return st, WriteStatus(m.StateDir, st)
	}

	st, statusErr := m.readLiveBootc(ctx)
	if statusErr != nil {
		errToRecord := statusErr
		if checkErr != nil {
			errToRecord = checkErr
		}
		return m.recordFailure(prev, errToRecord, &now), errToRecord
	}

	st.CheckedAt = &now
	if checkErr != nil {
		st.State = StateError
		st.Error = checkErr.Error()
		_ = WriteStatus(m.StateDir, st)
		return st, checkErr
	}
	if st.Available != nil && st.Staged == nil && !st.RollbackQueued {
		st.Download = ParseCheckEstimate(checkOut)
	}
	return st, WriteStatus(m.StateDir, st)
}

func (m Manager) runStageWithProgress(ctx context.Context, onLine func([]byte)) ([]byte, error) {
	if m.RunWithProgress != nil {
		return m.RunWithProgress(ctx, onLine, bootcBinary, "upgrade", "--quiet", "--progress-fd", "3")
	}
	if m.Run != nil {
		return m.Run(ctx, bootcBinary, "upgrade", "--quiet")
	}
	sawLine := false
	wrapped := func(line []byte) {
		sawLine = true
		onLine(line)
	}
	out, err := DefaultProgressRunner(ctx, wrapped, bootcBinary, "upgrade", "--quiet", "--progress-fd", "3")
	// Gracefully fall back if an older bootc binary does not recognize --progress-fd.
	if err != nil && !sawLine && strings.Contains(err.Error(), "--progress-fd") {
		return DefaultCommandRunner(ctx, bootcBinary, "upgrade", "--quiet")
	}
	return out, err
}

// Stage verifies the engine is idle, downloads and stages the next signed image via `bootc upgrade`,
// streams progress snapshots while running, and persists the resulting staged status. It never reboots automatically.
func (m Manager) Stage(ctx context.Context) (Status, error) {
	unlock, err := acquireLock(m.StateDir)
	if err != nil {
		return Status{}, err
	}
	defer unlock()

	if err := m.ensureIdle(ctx, "stage update"); err != nil {
		return Status{}, err
	}
	prev, _ := ReadStatus(m.StateDir)

	startedAt := m.now()
	cur := Progress{
		Phase:       "starting",
		Description: "Preparing update",
		StartedAt:   startedAt,
		UpdatedAt:   startedAt,
	}
	_ = WriteProgress(m.StateDir, cur)
	defer ClearProgress(m.StateDir)

	lastWrite := startedAt
	onLine := func(line []byte) {
		now := m.now()
		next, ok := ApplyProgressLine(cur, line, now)
		if !ok {
			return
		}
		important := next.Phase != cur.Phase ||
			next.Steps != cur.Steps ||
			(next.BytesTotal > 0 && next.Bytes >= next.BytesTotal)
		cur = next
		if important || now.Sub(lastWrite) >= progressWriteInterval {
			lastWrite = now
			_ = WriteProgress(m.StateDir, cur)
		}
	}

	if _, err := m.runStageWithProgress(ctx, onLine); err != nil {
		return m.recordFailure(prev, err, nil), err
	}

	st, err := m.readLiveBootc(ctx)
	if err != nil {
		return m.recordFailure(prev, err, nil), err
	}
	if prev.CheckedAt != nil {
		st.CheckedAt = prev.CheckedAt
	}
	return st, WriteStatus(m.StateDir, st)
}

// Rollback verifies the engine is idle, queues the previous bootc deployment via `bootc rollback`,
// and persists the updated status with RebootNeeded set.
func (m Manager) Rollback(ctx context.Context) (Status, error) {
	unlock, err := acquireLock(m.StateDir)
	if err != nil {
		return Status{}, err
	}
	defer unlock()

	if err := m.ensureIdle(ctx, "roll back update"); err != nil {
		return Status{}, err
	}
	prev, _ := ReadStatus(m.StateDir)

	if _, err := m.run(ctx, bootcBinary, "rollback"); err != nil {
		return m.recordFailure(prev, err, nil), err
	}

	st, err := m.readLiveBootc(ctx)
	if err != nil {
		return m.recordFailure(prev, err, nil), err
	}
	if prev.CheckedAt != nil {
		st.CheckedAt = prev.CheckedAt
	}
	if st.RollbackQueued || st.Staged != nil {
		st.RebootNeeded = true
	}
	return st, WriteStatus(m.StateDir, st)
}

// Reboot verifies the engine is idle and triggers a clean host reboot via systemctl.
func (m Manager) Reboot(ctx context.Context) error {
	unlock, err := acquireLock(m.StateDir)
	if err != nil {
		return err
	}
	defer unlock()

	if err := m.ensureIdle(ctx, "reboot"); err != nil {
		return err
	}
	_, err = m.run(ctx, "/usr/bin/systemctl", "reboot", "--no-block")
	return err
}

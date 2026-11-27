package engine

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/reset"
	"github.com/andaru-project/andaru/internal/update"
)

const (
	updateAPIPrefix = "/api/v1/system/update"
	rebootCooldown  = 60 * time.Second
)

// updateRunner asks PID 1 to execute privileged bootc/reboot verbs on behalf of the unprivileged engine.
type updateRunner interface {
	RunVerb(ctx context.Context, verb string) error
}

type systemdUpdateRunner struct {
	stateDir string
	exec     func(ctx context.Context, name string, args ...string) error
}

func newSystemdUpdateRunner(stateDir string) updateRunner {
	return systemdUpdateRunner{
		stateDir: stateDir,
		exec: func(ctx context.Context, name string, args ...string) error {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
}

// RunVerb starts a transient systemd unit named andaru-update-run-<suffix> so it never clashes
// with the packaged andaru-update-check.service while still matching andaru-update-*.service.
func (s systemdUpdateRunner) RunVerb(ctx context.Context, verb string) error {
	runtimeLimit := "RuntimeMaxSec=300"
	if verb == "update-stage" {
		runtimeLimit = "RuntimeMaxSec=900"
	}
	suffix := strings.TrimPrefix(verb, "update-")
	args := []string{
		"--quiet", "--collect", "--wait",
		"--unit=andaru-update-run-" + suffix, "-p", runtimeLimit,
		andaruBinary, "realize", "--state-dir", s.stateDir, verb,
	}
	return s.exec(ctx, "/usr/bin/systemd-run", args...)
}

type updateAPI struct {
	stateDir  string
	runner    updateRunner
	checkIdle func(stateDir string) error
	now       func() time.Time

	mu             sync.Mutex
	busyOp         string
	stageStartedAt time.Time
	lastProgress   *update.Progress
	rebootingUntil time.Time
	lastErr        string
	lastErrAt      time.Time
}

func newUpdateAPI(stateDir string, runner updateRunner) http.Handler {
	if runner == nil {
		runner = newSystemdUpdateRunner(stateDir)
	}
	return &updateAPI{
		stateDir:  stateDir,
		runner:    runner,
		checkIdle: reset.CheckPendingApply,
		now:       time.Now,
	}
}

func (u *updateAPI) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

func (u *updateAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case updateAPIPrefix:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		u.handleStatus(w, r)
	case updateAPIPrefix + "/check":
		u.handleSyncAction(w, r, update.StateChecking, "update-check", false)
	case updateAPIPrefix + "/stage":
		u.handleStage(w, r)
	case updateAPIPrefix + "/rollback":
		u.handleSyncAction(w, r, update.StateRollingBack, "update-rollback", true)
	case updateAPIPrefix + "/reboot":
		u.handleReboot(w, r)
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
	}
}

func reconcileBootedStatus(st update.Status) update.Status {
	// Right after rebooting into a staged image or queued rollback (before the post-boot check
	// timer runs), status.json may still report the new buildVersion as pending reboot.
	// Guard on Booted.Version != buildVersion so a same-version rebuild/re-tag isn't falsely
	// reconciled before reboot.
	if !st.RebootNeeded || buildVersion == "" {
		return st
	}
	if st.Booted != nil && st.Booted.Version == buildVersion {
		return st
	}
	if st.Staged != nil && st.Staged.Version == buildVersion {
		prev := st.Booted
		st.Booted = st.Staged
		st.Rollback = prev
		st.Staged = nil
		st.Available = nil
		st.Download = nil
		st.RollbackQueued = false
		st.RebootNeeded = false
		st.State = update.StateUpToDate
		st.Error = ""
		return st
	}
	if st.RollbackQueued && st.Rollback != nil && st.Rollback.Version == buildVersion {
		prev := st.Booted
		st.Booted = st.Rollback
		st.Rollback = prev
		st.Staged = nil
		st.Download = nil
		st.RollbackQueued = false
		st.RebootNeeded = false
		st.State = update.StateUpToDate
		st.Error = ""
	}
	return st
}

func (u *updateAPI) expireRebootLocked(now time.Time) {
	if u.busyOp == "rebooting" && !u.rebootingUntil.IsZero() && !now.Before(u.rebootingUntil) {
		u.busyOp = ""
		u.rebootingUntil = time.Time{}
	}
}

func (u *updateAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := update.ReadStatus(u.stateDir)
	if err != nil {
		slog.ErrorContext(r.Context(), "update status unavailable", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "UPDATE_UNAVAILABLE", "Software update status is unavailable; inspect the engine journal.")
		return
	}
	st = reconcileBootedStatus(st)
	now := u.clock()
	u.mu.Lock()
	u.expireRebootLocked(now)
	busy := u.busyOp
	stageStartedAt := u.stageStartedAt
	lastProgress := u.lastProgress
	lastErr := u.lastErr
	lastErrAt := u.lastErrAt
	if lastErr != "" && st.Error == "" && st.CheckedAt != nil && st.CheckedAt.After(lastErrAt) {
		u.lastErr = ""
		u.lastErrAt = time.Time{}
		lastErr = ""
	}
	u.mu.Unlock()
	if busy != "" && busy != "rebooting" {
		st.State = busy
		st.Error = ""
		if busy == update.StateStaging {
			st.Progress = u.currentStageProgress(st, stageStartedAt, lastProgress)
		}
	} else if lastErr != "" && st.Error == "" {
		st.State = update.StateError
		st.Error = lastErr
	}
	writeJSON(w, r, http.StatusOK, st)
}

func (u *updateAPI) currentStageProgress(st update.Status, stageStartedAt time.Time, lastProgress *update.Progress) *update.Progress {
	// Ignore any stale progress.json from an older crashed run predating this stage attempt.
	if prog, err := update.ReadProgress(u.stateDir); err == nil && prog != nil && !prog.StartedAt.Before(stageStartedAt.Add(-2*time.Second)) {
		copyProg := *prog
		u.mu.Lock()
		if u.busyOp == update.StateStaging && u.stageStartedAt.Equal(stageStartedAt) {
			u.lastProgress = &copyProg
		}
		u.mu.Unlock()
		return prog
	}
	// If the worker already cleaned up progress.json right before exiting, keep the last observed progress
	// rather than flashing backwards to "starting".
	if lastProgress != nil {
		copyProg := *lastProgress
		return &copyProg
	}
	if st.Staged != nil || st.RebootNeeded {
		return nil
	}
	p := update.StartingProgress(stageStartedAt)
	return &p
}

func (u *updateAPI) beginOp(w http.ResponseWriter, r *http.Request, opState string, requireIdle bool) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return false
	}
	if requireIdle && u.checkIdle != nil {
		if err := u.checkIdle(u.stateDir); err != nil {
			writeError(w, r, http.StatusConflict, "SYSTEM_BUSY", "A network apply is pending or active; finish or confirm it before updating.")
			return false
		}
	}
	now := u.clock()
	u.mu.Lock()
	u.expireRebootLocked(now)
	if u.busyOp != "" {
		current := u.busyOp
		u.mu.Unlock()
		writeError(w, r, http.StatusConflict, "UPDATE_IN_PROGRESS", "Another software update operation ("+current+") is already running.")
		return false
	}
	u.busyOp = opState
	if opState == update.StateStaging {
		u.stageStartedAt = now.UTC()
		u.lastProgress = nil
	}
	u.lastErr = ""
	u.lastErrAt = time.Time{}
	u.mu.Unlock()
	return true
}

func writeRunnerError(w http.ResponseWriter, r *http.Request, err error) {
	msg := err.Error()
	if strings.Contains(msg, update.ErrLocked.Error()) {
		writeError(w, r, http.StatusConflict, "UPDATE_IN_PROGRESS", "Another software update operation is already running.")
		return
	}
	if strings.Contains(msg, "busy") || strings.Contains(msg, "pending") || strings.Contains(msg, "queued or running") {
		writeError(w, r, http.StatusConflict, "SYSTEM_BUSY", "A network apply or realization job is in progress; retry once the box is idle.")
		return
	}
	writeError(w, r, http.StatusBadGateway, "UPDATE_FAILED", msg)
}

func (u *updateAPI) handleSyncAction(w http.ResponseWriter, r *http.Request, opState, verb string, requireIdle bool) {
	if !u.beginOp(w, r, opState, requireIdle) {
		return
	}
	defer func() {
		u.mu.Lock()
		u.busyOp = ""
		u.mu.Unlock()
	}()

	if err := u.runner.RunVerb(r.Context(), verb); err != nil {
		slog.WarnContext(r.Context(), "software update verb failed", "verb", verb, "error", err)
		writeRunnerError(w, r, err)
		return
	}
	st, err := update.ReadStatus(u.stateDir)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "UPDATE_UNAVAILABLE", "Software update completed but its status could not be read.")
		return
	}
	writeJSON(w, r, http.StatusOK, reconcileBootedStatus(st))
}

// handleStage runs `bootc upgrade` asynchronously so slow image downloads never hit the 180s web
// proxy timeout or orphan the busy guard when a client disconnects.
func (u *updateAPI) handleStage(w http.ResponseWriter, r *http.Request) {
	if !u.beginOp(w, r, update.StateStaging, true) {
		return
	}
	u.mu.Lock()
	startedAt := u.stageStartedAt
	u.mu.Unlock()

	st, err := update.ReadStatus(u.stateDir)
	if err != nil {
		st = update.Status{Supported: true}
	}
	st = reconcileBootedStatus(st)
	st.State = update.StateStaging
	st.Error = ""
	starting := update.StartingProgress(startedAt)
	st.Progress = &starting

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 16*time.Minute)
	go func() {
		defer cancel()
		err := u.runner.RunVerb(bgCtx, "update-stage")
		u.mu.Lock()
		u.busyOp = ""
		u.stageStartedAt = time.Time{}
		u.lastProgress = nil
		if err != nil {
			slog.Warn("software update stage failed", "error", err)
			u.lastErr = err.Error()
			u.lastErrAt = u.clock()
		}
		u.mu.Unlock()
	}()

	writeJSON(w, r, http.StatusAccepted, st)
}

func (u *updateAPI) handleReboot(w http.ResponseWriter, r *http.Request) {
	if !u.beginOp(w, r, "rebooting", true) {
		return
	}
	if err := u.runner.RunVerb(r.Context(), "update-reboot"); err != nil {
		u.mu.Lock()
		u.busyOp = ""
		u.rebootingUntil = time.Time{}
		u.mu.Unlock()
		slog.WarnContext(r.Context(), "software update reboot failed", "error", err)
		writeRunnerError(w, r, err)
		return
	}
	// Hold busyOp="rebooting" for rebootCooldown so no duplicate reboot or update starts while
	// the host shuts down, without wedging the engine if a reboot is cancelled.
	u.mu.Lock()
	u.rebootingUntil = u.clock().Add(rebootCooldown)
	u.mu.Unlock()
	writeJSON(w, r, http.StatusAccepted, map[string]bool{"rebooting": true})
}

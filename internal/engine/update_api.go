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

const updateAPIPrefix = "/api/v1/system/update"

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
	args := []string{"--quiet", "--collect", "--unit=andaru-update-run-" + suffix, "-p", runtimeLimit}
	if verb != "update-reboot" {
		args = append(args, "--wait")
	}
	args = append(args, andaruBinary, "realize", "--state-dir", s.stateDir, verb)
	return s.exec(ctx, "/usr/bin/systemd-run", args...)
}

type updateAPI struct {
	stateDir  string
	runner    updateRunner
	checkIdle func(stateDir string) error

	mu      sync.Mutex
	busyOp  string
	lastErr string
}

func newUpdateAPI(stateDir string, runner updateRunner) http.Handler {
	if runner == nil {
		runner = newSystemdUpdateRunner(stateDir)
	}
	return &updateAPI{
		stateDir:  stateDir,
		runner:    runner,
		checkIdle: reset.CheckPendingApply,
	}
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
	// Right after rebooting into a staged image (before the post-boot check timer runs),
	// status.json may still report the new buildVersion as staged/rebootNeeded.
	if st.RebootNeeded && st.Staged != nil && st.Staged.Version != "" && st.Staged.Version == buildVersion {
		st.Booted = st.Staged
		st.Staged = nil
		st.Available = nil
		st.RollbackQueued = false
		st.RebootNeeded = false
		st.State = update.StateUpToDate
		st.Error = ""
	}
	return st
}

func (u *updateAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := update.ReadStatus(u.stateDir)
	if err != nil {
		slog.ErrorContext(r.Context(), "update status unavailable", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "UPDATE_UNAVAILABLE", "Software update status is unavailable; inspect the engine journal.")
		return
	}
	st = reconcileBootedStatus(st)
	u.mu.Lock()
	busy := u.busyOp
	lastErr := u.lastErr
	u.mu.Unlock()
	if busy != "" && busy != "rebooting" {
		st.State = busy
		st.Error = ""
	} else if lastErr != "" && st.Error == "" {
		st.State = update.StateError
		st.Error = lastErr
	}
	writeJSON(w, r, http.StatusOK, st)
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
	u.mu.Lock()
	if u.busyOp != "" {
		current := u.busyOp
		u.mu.Unlock()
		writeError(w, r, http.StatusConflict, "UPDATE_IN_PROGRESS", "Another software update operation ("+current+") is already running.")
		return false
	}
	u.busyOp = opState
	u.lastErr = ""
	u.mu.Unlock()
	return true
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
		if strings.Contains(err.Error(), "busy") || strings.Contains(err.Error(), "pending") || strings.Contains(err.Error(), "queued or running") {
			writeError(w, r, http.StatusConflict, "SYSTEM_BUSY", "A network apply or realization job is in progress; retry once the box is idle.")
			return
		}
		writeError(w, r, http.StatusBadGateway, "UPDATE_FAILED", err.Error())
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
	st, err := update.ReadStatus(u.stateDir)
	if err != nil {
		st = update.Status{Supported: true}
	}
	st = reconcileBootedStatus(st)
	st.State = update.StateStaging
	st.Error = ""

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 16*time.Minute)
	go func() {
		defer cancel()
		err := u.runner.RunVerb(bgCtx, "update-stage")
		u.mu.Lock()
		u.busyOp = ""
		if err != nil {
			slog.Warn("software update stage failed", "error", err)
			u.lastErr = err.Error()
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
		u.mu.Unlock()
		slog.WarnContext(r.Context(), "software update reboot failed", "error", err)
		writeError(w, r, http.StatusBadGateway, "UPDATE_FAILED", err.Error())
		return
	}
	// Keep busyOp="rebooting" so no second reboot or update starts while the host shuts down.
	writeJSON(w, r, http.StatusAccepted, map[string]bool{"rebooting": true})
}

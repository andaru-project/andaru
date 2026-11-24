package engine

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"

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

func (s systemdUpdateRunner) RunVerb(ctx context.Context, verb string) error {
	runtimeLimit := "RuntimeMaxSec=300"
	if verb == "update-stage" {
		runtimeLimit = "RuntimeMaxSec=900"
	}
	args := []string{"--quiet", "--collect", "--unit=andaru-" + verb, "-p", runtimeLimit}
	if verb != "update-reboot" {
		args = append(args, "--wait")
	}
	args = append(args, andaruBinary, "realize", "--state-dir", s.stateDir, verb)
	return s.exec(ctx, "/usr/bin/systemd-run", args...)
}

type updateAPI struct {
	stateDir string
	runner   updateRunner

	mu     sync.Mutex
	busyOp string
}

func newUpdateAPI(stateDir string, runner updateRunner) http.Handler {
	if runner == nil {
		runner = newSystemdUpdateRunner(stateDir)
	}
	return &updateAPI{stateDir: stateDir, runner: runner}
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
		u.handleAction(w, r, update.StateChecking, "update-check", false)
	case updateAPIPrefix + "/stage":
		u.handleAction(w, r, update.StateStaging, "update-stage", false)
	case updateAPIPrefix + "/rollback":
		u.handleAction(w, r, update.StateRollingBack, "update-rollback", false)
	case updateAPIPrefix + "/reboot":
		u.handleAction(w, r, "rebooting", "update-reboot", true)
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
	}
}

func (u *updateAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, _, err := update.ReadStatus(u.stateDir)
	if err != nil {
		slog.ErrorContext(r.Context(), "update status unavailable", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "UPDATE_UNAVAILABLE", "Software update status is unavailable; inspect the engine journal.")
		return
	}
	u.mu.Lock()
	busy := u.busyOp
	u.mu.Unlock()
	if busy != "" && busy != "rebooting" {
		st.State = busy
	}
	writeJSON(w, r, http.StatusOK, st)
}

func (u *updateAPI) handleAction(w http.ResponseWriter, r *http.Request, opState, verb string, asyncReboot bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return
	}
	u.mu.Lock()
	if u.busyOp != "" {
		current := u.busyOp
		u.mu.Unlock()
		writeError(w, r, http.StatusConflict, "UPDATE_IN_PROGRESS", "Another software update operation ("+current+") is already running.")
		return
	}
	u.busyOp = opState
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		if !asyncReboot || u.busyOp == opState {
			u.busyOp = ""
		}
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
	if asyncReboot {
		writeJSON(w, r, http.StatusAccepted, map[string]bool{"rebooting": true})
		return
	}
	st, _, err := update.ReadStatus(u.stateDir)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "UPDATE_UNAVAILABLE", "Software update completed but its status could not be read.")
		return
	}
	writeJSON(w, r, http.StatusOK, st)
}

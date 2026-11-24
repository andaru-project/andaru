package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/update"
)

type stubUpdateRunner struct {
	run func(ctx context.Context, verb string) error
}

func (s stubUpdateRunner) RunVerb(ctx context.Context, verb string) error {
	if s.run != nil {
		return s.run(ctx, verb)
	}
	return nil
}

func TestSystemdUpdateRunnerArgsAvoidUnitClash(t *testing.T) {
	var calls [][]string
	r := systemdUpdateRunner{
		stateDir: "/var/lib/andaru",
		exec: func(_ context.Context, name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}

	if err := r.RunVerb(context.Background(), "update-check"); err != nil {
		t.Fatalf("RunVerb check: %v", err)
	}
	if err := r.RunVerb(context.Background(), "update-stage"); err != nil {
		t.Fatalf("RunVerb stage: %v", err)
	}
	if err := r.RunVerb(context.Background(), "update-reboot"); err != nil {
		t.Fatalf("RunVerb reboot: %v", err)
	}

	if !slices.Contains(calls[0], "--unit=andaru-update-run-check") || !slices.Contains(calls[0], "--wait") {
		t.Fatalf("check must use non-clashing transient unit with --wait, got %v", calls[0])
	}
	if slices.Contains(calls[0], "--unit=andaru-update-check") {
		t.Fatalf("check unit clashes with packaged andaru-update-check.service: %v", calls[0])
	}
	if !slices.Contains(calls[1], "--unit=andaru-update-run-stage") || !slices.Contains(calls[1], "RuntimeMaxSec=900") {
		t.Fatalf("stage args unexpected: %v", calls[1])
	}
	if !slices.Contains(calls[2], "--unit=andaru-update-run-reboot") || !slices.Contains(calls[2], "--wait") {
		t.Fatalf("reboot must pass --wait so lock/idle refusals are reported synchronously, got %v", calls[2])
	}
}

func TestUpdateAPILifecycleAndAuthz(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	stageDone := make(chan struct{})
	var lastVerb string
	runner := stubUpdateRunner{run: func(_ context.Context, verb string) error {
		lastVerb = verb
		switch verb {
		case "update-check":
			return update.WriteStatus(dir, update.Status{
				Supported: true,
				State:     update.StateAvailable,
				Booted:    &update.Deployment{Image: "ghcr.io/andaru-project/andaru:stable", Version: "0.5.0-a.4", Digest: "sha256:1111"},
				Available: &update.Deployment{Image: "ghcr.io/andaru-project/andaru:stable", Version: "0.5.0-a.5", Digest: "sha256:2222"},
				CheckedAt: &now,
			})
		case "update-stage":
			err := update.WriteStatus(dir, update.Status{
				Supported:    true,
				State:        update.StateStaged,
				Booted:       &update.Deployment{Version: "0.5.0-a.4", Digest: "sha256:1111"},
				Staged:       &update.Deployment{Version: "0.5.0-a.5", Digest: "sha256:2222"},
				CheckedAt:    &now,
				RebootNeeded: true,
			})
			close(stageDone)
			return err
		case "update-rollback":
			return update.WriteStatus(dir, update.Status{
				Supported:      true,
				State:          update.StateStaged,
				Booted:         &update.Deployment{Version: "0.5.0-a.4", Digest: "sha256:1111"},
				Rollback:       &update.Deployment{Version: "0.5.0-a.3", Digest: "sha256:0000"},
				RollbackQueued: true,
				RebootNeeded:   true,
			})
		}
		return nil
	}}

	uapi := newUpdateAPI(dir, runner).(*updateAPI)
	h := newHandlerWithServices(
		func(context.Context) ([]host.LinkStatus, error) { return nil, nil },
		http.NotFoundHandler(),
		nil, nil, nil,
		uapi,
	)

	// Uncached GET returns unchecked cleanly.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/system/update"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET initial update status: %d %s", rec.Code, rec.Body.String())
	}
	var st update.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !st.Supported || st.State != update.StateUnchecked {
		t.Fatalf("expected initial unchecked, got %+v", st)
	}

	// POST /check updates cached status.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/check"))
	if rec.Code != http.StatusOK || lastVerb != "update-check" {
		t.Fatalf("POST check: %d verb=%q body=%s", rec.Code, lastVerb, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.State != update.StateAvailable {
		t.Fatalf("expected available after check, err=%v st=%+v", err, st)
	}

	// POST /stage starts asynchronously (202 Accepted) and persists staged state.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/stage"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST stage: %d body=%s", rec.Code, rec.Body.String())
	}
	<-stageDone
	// Wait briefly for goroutine to clear busyOp.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/system/update"))
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		if st.State == update.StateStaged {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st.State != update.StateStaged || !st.RebootNeeded {
		t.Fatalf("expected staged after async stage, got %+v", st)
	}

	// Post-reboot reconciliation clears rebootNeeded when buildVersion matches staged version.
	prevVersion := buildVersion
	buildVersion = "0.5.0-a.5"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/system/update"))
	buildVersion = prevVersion
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.State != update.StateUpToDate || st.RebootNeeded || st.Booted == nil || st.Booted.Version != "0.5.0-a.5" {
		t.Fatalf("expected reconciled up_to_date after reboot, got %+v", st)
	}

	// POST /rollback queues rollback.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/rollback"))
	if rec.Code != http.StatusOK || lastVerb != "update-rollback" {
		t.Fatalf("POST rollback: %d verb=%q body=%s", rec.Code, lastVerb, rec.Body.String())
	}

	// POST /reboot returns 202 Accepted and locks out duplicate reboots.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/reboot"))
	if rec.Code != http.StatusAccepted || lastVerb != "update-reboot" {
		t.Fatalf("POST reboot: %d verb=%q body=%s", rec.Code, lastVerb, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/reboot"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate reboot, got %d %s", rec.Code, rec.Body.String())
	}

	// Pending apply refuses stage and reboot with 409 SYSTEM_BUSY before calling runner.
	called := false
	busyAPI := &updateAPI{
		stateDir:  dir,
		runner:    stubUpdateRunner{run: func(context.Context, string) error { called = true; return nil }},
		checkIdle: func(string) error { return errors.New("apply pending") },
	}
	for _, p := range []string{"/api/v1/system/update/stage", "/api/v1/system/update/rollback", "/api/v1/system/update/reboot"} {
		rec = httptest.NewRecorder()
		busyAPI.ServeHTTP(rec, rootRequest(http.MethodPost, p))
		if rec.Code != http.StatusConflict {
			t.Fatalf("POST %s when busy: got %d, want 409", p, rec.Code)
		}
	}
	if called {
		t.Fatal("runner must not be invoked when checkIdle fails")
	}

	// Verify OpenAPI + route capability table stays consistent and covers all 5 update endpoints.
	if err := validateRouteCapabilities(openAPIDocument, engineRoutes); err != nil {
		t.Fatalf("validateRouteCapabilities: %v", err)
	}
	wantCaps := map[string]authz.Capability{
		"GET /api/v1/system/update":           authz.StatusRead,
		"POST /api/v1/system/update/check":    authz.ConfigurationApply,
		"POST /api/v1/system/update/stage":    authz.ConfigurationApply,
		"POST /api/v1/system/update/rollback": authz.ConfigurationApply,
		"POST /api/v1/system/update/reboot":   authz.ConfigurationApply,
	}
	for _, rc := range engineRoutes {
		key := rc.Method + " " + rc.Path
		if want, ok := wantCaps[key]; ok {
			if rc.Capability != want {
				t.Fatalf("%s capability: got %q, want %q", key, rc.Capability, want)
			}
			delete(wantCaps, key)
		}
	}
	if len(wantCaps) > 0 {
		t.Fatalf("missing capability declarations: %v", wantCaps)
	}
}

func TestUpdateAPIRebootCooldownAndBusyError(t *testing.T) {
	dir := t.TempDir()
	cur := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	var nextErr error
	uapi := &updateAPI{
		stateDir: dir,
		runner:   stubUpdateRunner{run: func(context.Context, string) error { return nextErr }},
		now:      func() time.Time { return cur },
	}

	// Failure inside the reboot unit clears busyOp and maps busy error to 409.
	nextErr = errors.New("another update operation is busy")
	rec := httptest.NewRecorder()
	uapi.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/reboot"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("reboot busy failure: got %d, want 409 (%s)", rec.Code, rec.Body.String())
	}

	// Retry succeeds once idle, then blocks duplicate reboot during cooldown.
	nextErr = nil
	rec = httptest.NewRecorder()
	uapi.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/reboot"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reboot retry: got %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	uapi.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/reboot"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate reboot during cooldown: got %d, want 409", rec.Code)
	}

	// After rebootCooldown elapses (e.g. reboot cancelled), engine is no longer wedged.
	cur = cur.Add(rebootCooldown + time.Second)
	rec = httptest.NewRecorder()
	uapi.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/reboot"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reboot after cooldown: got %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
}

func TestReconcileBootedStatusRollbackAndSameVersionRebuild(t *testing.T) {
	prev := buildVersion
	defer func() { buildVersion = prev }()

	// Same-version rebuild (new digest, same version string) must NOT clear rebootNeeded before reboot.
	buildVersion = "0.5.0-a.4"
	rebuild := reconcileBootedStatus(update.Status{
		Supported:    true,
		State:        update.StateStaged,
		Booted:       &update.Deployment{Version: "0.5.0-a.4", Digest: "sha256:old"},
		Staged:       &update.Deployment{Version: "0.5.0-a.4", Digest: "sha256:new"},
		RebootNeeded: true,
	})
	if !rebuild.RebootNeeded || rebuild.State != update.StateStaged {
		t.Fatalf("same-version rebuild must keep rebootNeeded, got %+v", rebuild)
	}

	// Queued rollback reconciles once booted into the rollback version.
	buildVersion = "0.5.0-a.3"
	rolledBack := reconcileBootedStatus(update.Status{
		Supported:      true,
		State:          update.StateStaged,
		Booted:         &update.Deployment{Version: "0.5.0-a.4", Digest: "sha256:new"},
		Rollback:       &update.Deployment{Version: "0.5.0-a.3", Digest: "sha256:old"},
		RollbackQueued: true,
		RebootNeeded:   true,
	})
	if rolledBack.RebootNeeded || rolledBack.RollbackQueued || rolledBack.State != update.StateUpToDate || rolledBack.Booted == nil || rolledBack.Booted.Version != "0.5.0-a.3" {
		t.Fatalf("expected reconciled rollback status, got %+v", rolledBack)
	}
}

func TestUpdateAPIClearsStaleStageErrorAfterNewerCheck(t *testing.T) {
	dir := t.TempDir()
	errTime := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	uapi := &updateAPI{
		stateDir:  dir,
		runner:    stubUpdateRunner{},
		now:       func() time.Time { return errTime },
		lastErr:   "registry timeout",
		lastErrAt: errTime,
	}

	// Older check still surfaces the in-memory stage error.
	older := errTime.Add(-time.Minute)
	if err := update.WriteStatus(dir, update.Status{Supported: true, State: update.StateAvailable, CheckedAt: &older}); err != nil {
		t.Fatalf("WriteStatus older: %v", err)
	}
	rec := httptest.NewRecorder()
	uapi.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/system/update"))
	var st update.Status
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.State != update.StateError || st.Error != "registry timeout" {
		t.Fatalf("expected stage error with older check, got %+v", st)
	}

	// Newer successful timer check clears the stale in-memory error.
	newer := errTime.Add(time.Minute)
	if err := update.WriteStatus(dir, update.Status{Supported: true, State: update.StateAvailable, CheckedAt: &newer}); err != nil {
		t.Fatalf("WriteStatus newer: %v", err)
	}
	rec = httptest.NewRecorder()
	uapi.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/system/update"))
	st = update.Status{}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.State != update.StateAvailable || st.Error != "" {
		t.Fatalf("expected stale stage error cleared by newer check, got %+v", st)
	}
}

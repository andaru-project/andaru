package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestUpdateAPILifecycleAndAuthz(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

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
			return update.WriteStatus(dir, update.Status{
				Supported:    true,
				State:        update.StateStaged,
				Booted:       &update.Deployment{Version: "0.5.0-a.4", Digest: "sha256:1111"},
				Staged:       &update.Deployment{Version: "0.5.0-a.5", Digest: "sha256:2222"},
				CheckedAt:    &now,
				RebootNeeded: true,
			})
		case "update-rollback":
			return update.WriteStatus(dir, update.Status{
				Supported:    true,
				State:        update.StateStaged,
				Booted:       &update.Deployment{Version: "0.5.0-a.4", Digest: "sha256:1111"},
				Staged:       &update.Deployment{Version: "0.5.0-a.3", Digest: "sha256:0000"},
				RebootNeeded: true,
			})
		}
		return nil
	}}

	h := newHandlerWithServices(
		func(context.Context) ([]host.LinkStatus, error) { return nil, nil },
		http.NotFoundHandler(),
		nil, nil, nil,
		newUpdateAPI(dir, runner),
	)

	// Uncached GET returns unsupported cleanly.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/system/update"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET initial update status: %d %s", rec.Code, rec.Body.String())
	}
	var st update.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.Supported || st.State != update.StateUnsupported {
		t.Fatalf("expected initial unsupported, got %+v", st)
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

	// POST /stage stages update and sets rebootNeeded.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/stage"))
	if rec.Code != http.StatusOK || lastVerb != "update-stage" {
		t.Fatalf("POST stage: %d verb=%q body=%s", rec.Code, lastVerb, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.State != update.StateStaged || !st.RebootNeeded {
		t.Fatalf("expected staged after stage, err=%v st=%+v", err, st)
	}

	// POST /rollback queues rollback.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/rollback"))
	if rec.Code != http.StatusOK || lastVerb != "update-rollback" {
		t.Fatalf("POST rollback: %d verb=%q body=%s", rec.Code, lastVerb, rec.Body.String())
	}

	// POST /reboot returns 202 Accepted.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/reboot"))
	if rec.Code != http.StatusAccepted || lastVerb != "update-reboot" {
		t.Fatalf("POST reboot: %d verb=%q body=%s", rec.Code, lastVerb, rec.Body.String())
	}

	// Busy host returns 409 SYSTEM_BUSY.
	busyAPI := newUpdateAPI(dir, stubUpdateRunner{run: func(context.Context, string) error {
		return errors.New("cannot stage update while system is busy: apply pending")
	}})
	rec = httptest.NewRecorder()
	busyAPI.ServeHTTP(rec, rootRequest(http.MethodPost, "/api/v1/system/update/stage"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when busy, got %d %s", rec.Code, rec.Body.String())
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

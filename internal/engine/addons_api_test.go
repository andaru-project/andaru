package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

type fakeSnapshotStore struct {
	raw []byte
	err error
}

func (f fakeSnapshotStore) Snapshot(context.Context) (config.SnapshotView, error) {
	if f.err != nil {
		return config.SnapshotView{}, f.err
	}
	return config.SnapshotView{SavedVersion: "1", Value: f.raw}, nil
}

type fakeAddonRunner struct {
	calls []string
	err   error
}

func (f *fakeAddonRunner) Arm(context.Context, int, int) error { return nil }
func (f *fakeAddonRunner) Disarm(context.Context, int)         {}
func (f *fakeAddonRunner) Realize(_ context.Context, verb string, id int, extra ...string) error {
	f.calls = append(f.calls, verb+" "+strings.Join(extra, " "))
	return f.err
}

const testAPIAddonDigest = "ghcr.io/example/sample@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testAPIAddonCatalog(t *testing.T) *addon.Catalog {
	t.Helper()
	dir := t.TempDir()
	slugDir := filepath.Join(dir, "sample")
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "manifestVersion": 1,
  "requires": [{"name": "andaru.extension-api", "version": ">=1.0.0 <2.0.0"}],
  "id": "org.andaru.sample",
  "slug": "sample",
  "name": "Sample Add-on",
  "description": "Sample description",
  "version": "1.2.3",
  "application": {"name": "Sample", "version": "1.2.3"},
  "permissions": [
    {"name": "view", "title": "View"},
    {"name": "logs", "title": "Logs"}
  ],
  "runtime": {
    "image": "` + testAPIAddonDigest + `",
    "network": "host",
    "profile": "unprivileged"
  },
  "storage": [
    {"name": "logs", "purpose": "logs", "mountPath": "/var/log/sample", "backup": false}
  ],
  "endpoints": [
    {"name": "ui", "protocol": "https", "port": 8080, "exposure": "managed"}
  ],
  "webui": {
    "title": "Sample UI",
    "permission": "view",
    "endpoint": "ui",
    "hostnameSetting": "/uiHost",
    "path": "/app"
  },
  "bus": {
    "publish": [{"topic": "notification/addon.sample", "title": "Alerts"}],
    "subscribe": [{"topic": "apply/+", "title": "Applies"}]
  },
  "logs": [
    {"name": "runtime", "title": "Runtime", "permission": "logs", "source": {"type": "runtime"}},
    {"name": "appfile", "title": "File", "permission": "logs", "source": {"type": "file", "storage": "logs", "path": "app.log"}}
  ],
  "properties": [
    {"name": "uiHost", "type": "string", "format": "hostname", "title": "WebUI host"}
  ]
}`
	if err := os.WriteFile(filepath.Join(slugDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := addon.LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func withActor(r *http.Request, caps ...authz.Capability) *http.Request {
	const id = "00000000-0000-4000-8000-000000000001"
	actor := requestActor{
		Actor:        authz.Actor{Kind: authz.ActorAccount, ID: id},
		AccountID:    id,
		Source:       "127.0.0.1",
		Capabilities: caps,
	}
	return r.WithContext(context.WithValue(r.Context(), authenticatedActorKey{}, actor))
}

func TestAddonsAPICapabilityGatingWebUIOriginAndLogs(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	cat := testAPIAddonCatalog(t)
	runner := &fakeAddonRunner{}

	savedJSON := []byte(`{"schemaVersion":4,"addons":[{"id":"org.andaru.sample","enabled":true,"settings":{"uiHost":"admin.corp"},"busGrants":{"publish":{"notification/addon.sample":"granted"}}}]}`)
	store := fakeSnapshotStore{raw: savedJSON}

	// Seed staged state and live generation.
	stagedBytes, _ := json.Marshal(addon.StagedState{
		Images: map[string]addon.StagedImage{
			testAPIAddonDigest: {Digest: testAPIAddonDigest, StagedAt: time.Now().UTC()},
		},
	})
	if err := os.WriteFile(filepath.Join(stateDir, addon.StagedStateFile), stagedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	seedLiveGeneration(t, stateDir, 1, "1", "sample")

	// Seed file log + symlink escape attempt.
	logDir := filepath.Join(stateDir, "addons", "sample", "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "app.log"), []byte("line1\nline2\nline3\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	api := newAddonsAPI(cat, store, stateDir, root, []string{"https://fw.lan", "https://admin.corp:8443"}, runner, nil)
	api.readUnit = func(context.Context, string) (string, string, error) { return "active", "running", nil }
	api.readJournal = func(_ context.Context, _ string, lines int, cursor string) ([]string, string, bool, error) {
		return []string{"j1", "j2"}, "s=abc;i=1", false, nil
	}

	// 1. addons.read ONLY: saved settings MUST be omitted, unreviewedBusCount=1 (apply/+ unreviewed), WebUI suppressed because uiHost matches second origin admin.corp!
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample", nil), authz.AddonsRead))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET detail status = %d: %s", rec.Code, rec.Body.String())
	}
	var detail addonDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Saved != nil {
		t.Fatalf("saved config leaked to addons.read-only caller: %+v", detail.Saved)
	}
	if !detail.Installed || detail.UnreviewedBusCount != 1 {
		t.Fatalf("installed=%v unreviewed=%d", detail.Installed, detail.UnreviewedBusCount)
	}
	if detail.Runtime.ActiveState != "active" || detail.Runtime.Health != "healthy" {
		t.Fatalf("runtime = %+v", detail.Runtime)
	}
	if detail.WebUI == nil || !detail.WebUI.OriginConflict || detail.WebUI.URL != "" {
		t.Fatalf("expected multi-origin conflict suppression for admin.corp, got %+v", detail.WebUI)
	}

	// 2. With distinct WebUI hostname and configuration.read: saved IS included and WebUI URL is populated.
	api.store = fakeSnapshotStore{raw: []byte(`{"schemaVersion":4,"addons":[{"id":"org.andaru.sample","enabled":true,"settings":{"uiHost":"sample.lan"}}]}`)}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample", nil), authz.AddonsRead, authz.ConfigurationRead))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET detail with config.read status = %d", rec.Code)
	}
	detail = addonDetailResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Saved == nil || detail.Saved.Settings["uiHost"] != "sample.lan" {
		t.Fatalf("expected saved config for configuration.read caller, got %+v", detail.Saved)
	}
	if detail.WebUI == nil || detail.WebUI.OriginConflict || detail.WebUI.URL != "https://sample.lan:8080/app" {
		t.Fatalf("unexpected WebUI view: %+v", detail.WebUI)
	}

	// 3. Lifecycle control: refused in safe mode (409 SAFE_MODE_ACTIVE), succeeds when safe mode off.
	if err := os.MkdirAll(filepath.Join(root, "run", "andaru-boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+generation.AddonsHeldFile, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodPost, "/api/v1/addons/sample/restart", nil), authz.AddonsManage))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "SAFE_MODE_ACTIVE") {
		t.Fatalf("expected 409 SAFE_MODE_ACTIVE, got %d: %s", rec.Code, rec.Body.String())
	}
	_ = os.Remove(root + generation.AddonsHeldFile)
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodPost, "/api/v1/addons/sample/restart", nil), authz.AddonsManage))
	if rec.Code != http.StatusOK {
		t.Fatalf("restart status = %d: %s", rec.Code, rec.Body.String())
	}
	if !slices.Contains(runner.calls, "addon-ctl restart sample") {
		t.Fatalf("runner calls = %v", runner.calls)
	}

	// 4. DELETE /stage refused while saved head or generation references the image.
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodDelete, "/api/v1/addons/sample/stage", nil), authz.AddonsManage))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ADDON_IMAGE_IN_USE") {
		t.Fatalf("expected 409 ADDON_IMAGE_IN_USE, got %d: %s", rec.Code, rec.Body.String())
	}

	// 5. Runtime logs & file logs (bounded tail + invalid params).
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample/logs/runtime?lines=50", nil), authz.AddonsRead))
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime logs status = %d: %s", rec.Code, rec.Body.String())
	}
	var logsResp addonLogsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &logsResp); err != nil || len(logsResp.Lines) != 2 || logsResp.Cursor != "s=abc;i=1" {
		t.Fatalf("runtime logs = %+v, err=%v", logsResp, err)
	}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample/logs/appfile?lines=2", nil), authz.AddonsRead))
	if rec.Code != http.StatusOK {
		t.Fatalf("file logs status = %d: %s", rec.Code, rec.Body.String())
	}
	logsResp = addonLogsResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &logsResp); err != nil || !slices.Equal(logsResp.Lines, []string{"line2", "line3"}) || !logsResp.Truncated {
		t.Fatalf("file logs = %+v, err=%v", logsResp, err)
	}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample/logs/runtime?cursor=--evil", nil), authz.AddonsRead))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid cursor, got %d", rec.Code)
	}

	// 6. Symlink escape in file log directory is blocked by os.OpenRoot.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(logDir, "app.log"))
	if err := os.Symlink(outside, filepath.Join(logDir, "app.log")); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample/logs/appfile", nil), authz.AddonsRead))
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("file log followed symlink outside storage dir: %s", rec.Body.String())
	}
}

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
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/audit"
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
	if detail.Runtime.ActiveState != "active" || detail.Runtime.Health != "running" {
		t.Fatalf("runtime = %+v", detail.Runtime)
	}
	if detail.WebUI == nil || !detail.WebUI.OriginConflict || detail.WebUI.URL != "" {
		t.Fatalf("expected multi-origin conflict suppression for admin.corp, got %+v", detail.WebUI)
	}

	// 1b. GET /api/v1/addons list endpoint respects capability gating.
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons", nil), authz.AddonsRead))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/addons = %d: %s", rec.Code, rec.Body.String())
	}
	var listResp addonListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil || len(listResp.Items) != 1 || listResp.Items[0].Saved != nil {
		t.Fatalf("listResp = %+v, err=%v", listResp, err)
	}

	// 1c. Authority injection / alternate IPv4 WebUI hostname is suppressed.
	for _, evilHost := range []string{"x@admin.corp", "0xc0a80101", "3232235777", "0177.0.0.1"} {
		api.store = fakeSnapshotStore{raw: []byte(`{"schemaVersion":4,"addons":[{"id":"org.andaru.sample","enabled":true,"settings":{"uiHost":"` + evilHost + `"}}]}`)}
		rec = httptest.NewRecorder()
		api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample", nil), authz.AddonsRead))
		var evilDetail addonDetailResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &evilDetail); err != nil {
			t.Fatal(err)
		}
		if evilDetail.WebUI == nil || !evilDetail.WebUI.OriginConflict || evilDetail.WebUI.URL != "" {
			t.Fatalf("host %q was not suppressed: %+v", evilHost, evilDetail.WebUI)
		}
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

	// 4. DELETE /stage fails closed on snapshot errors AND refuses while saved head or generation references the image.
	api.store = fakeSnapshotStore{err: config.ErrUpgradePending}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodDelete, "/api/v1/addons/sample/stage", nil), authz.AddonsManage))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ADDON_IMAGE_IN_USE") {
		t.Fatalf("expected fail-closed 409 on ErrUpgradePending, got %d: %s", rec.Code, rec.Body.String())
	}
	api.store = fakeSnapshotStore{raw: []byte(`{"schemaVersion":4,"addons":[{"id":"org.andaru.sample","enabled":true,"settings":{"uiHost":"sample.lan"}}]}`)}
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

	// 6. Symlink escape, intra-root symlink, and FIFO are all rejected with 503 LOGS_UNAVAILABLE (never hanging).
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
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "LOGS_UNAVAILABLE") {
		t.Fatalf("external symlink expected 503 LOGS_UNAVAILABLE, got %d: %s", rec.Code, rec.Body.String())
	}

	// Intra-root symlink must also be rejected by RESOLVE_NO_SYMLINKS / O_NOFOLLOW.
	if err := os.WriteFile(filepath.Join(logDir, "other.log"), []byte("OTHER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(logDir, "app.log"))
	if err := os.Symlink("other.log", filepath.Join(logDir, "app.log")); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample/logs/appfile", nil), authz.AddonsRead))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "LOGS_UNAVAILABLE") {
		t.Fatalf("intra-root symlink expected 503 LOGS_UNAVAILABLE, got %d: %s", rec.Code, rec.Body.String())
	}

	// FIFO must fail immediately without blocking.
	_ = os.Remove(filepath.Join(logDir, "app.log"))
	if err := syscall.Mkfifo(filepath.Join(logDir, "app.log"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, "/api/v1/addons/sample/logs/appfile", nil), authz.AddonsRead))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "LOGS_UNAVAILABLE") {
		t.Fatalf("FIFO expected 503 LOGS_UNAVAILABLE, got %d: %s", rec.Code, rec.Body.String())
	}
}

type blockingAddonRunner struct {
	started chan struct{}
	unblock chan struct{}
}

func (b *blockingAddonRunner) Arm(context.Context, int, int) error { return nil }
func (b *blockingAddonRunner) Disarm(context.Context, int)         {}
func (b *blockingAddonRunner) Realize(_ context.Context, _ string, _ int, _ ...string) error {
	close(b.started)
	<-b.unblock
	return nil
}

func TestAddonsAPIAsyncStageAndConcurrentConflict(t *testing.T) {
	cat := testAPIAddonCatalog(t)
	runner := &blockingAddonRunner{started: make(chan struct{}), unblock: make(chan struct{})}
	api := newAddonsAPI(cat, fakeSnapshotStore{raw: []byte(`{"schemaVersion":4}`)}, t.TempDir(), t.TempDir(), nil, runner, nil)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodPost, "/api/v1/addons/sample/stage", nil), authz.AddonsManage))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /stage = %d: %s", rec.Code, rec.Body.String())
	}
	var st addonStagedView
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || !st.Staging {
		t.Fatalf("expected staging=true, got %+v (%v)", st, err)
	}
	<-runner.started

	rec2 := httptest.NewRecorder()
	api.ServeHTTP(rec2, withActor(httptest.NewRequest(http.MethodPost, "/api/v1/addons/sample/stage", nil), authz.AddonsManage))
	if rec2.Code != http.StatusConflict || !strings.Contains(rec2.Body.String(), "STAGE_IN_PROGRESS") {
		t.Fatalf("concurrent POST /stage = %d: %s", rec2.Code, rec2.Body.String())
	}
	close(runner.unblock)
}

func TestParseJournalOutputBoundsTailAndUTF8(t *testing.T) {
	// Exact-limit write to tailBuffer must NOT mark truncated or drop the first line.
	var exact tailBuffer
	exact.limit = 11
	_, _ = exact.Write([]byte("line1\nline2"))
	if exact.Truncated() || string(exact.Bytes()) != "line1\nline2" {
		t.Fatalf("exact-limit tailBuffer truncated=%v bytes=%q", exact.Truncated(), exact.Bytes())
	}

	// Edge case that previously panicked: raw length > limit before TrimRight, < limit after TrimRight.
	trailingNewlines := append([]byte(strings.Repeat("a", maxAddonLogBytes-1)), '\n', '\n')
	lines, cursor, truncated := parseJournalOutput(trailingNewlines, false, maxAddonLogBytes)
	if len(lines) != 1 || cursor != "" || truncated {
		t.Fatalf("unexpected trailing-newline result: lines=%d cursor=%q truncated=%v", len(lines), cursor, truncated)
	}

	// Oversized multi-byte UTF-8 output preserves newest lines, valid UTF-8, and trailing cursor.
	var tb tailBuffer
	tb.limit = 128
	_, _ = tb.Write([]byte(strings.Repeat("é", 100) + "\nnewest-line\n-- cursor: s=999\n"))
	lines, cursor, truncated = parseJournalOutput(tb.Bytes(), tb.Truncated(), 64)
	if !truncated || cursor != "s=999" || !slices.Contains(lines, "newest-line") {
		t.Fatalf("oversized journal parse: lines=%v cursor=%q truncated=%v", lines, cursor, truncated)
	}
	for _, line := range lines {
		if !utf8.ValidString(line) {
			t.Fatalf("invalid UTF-8 in journal line: %q", line)
		}
	}
	// Cutoff landing immediately after '\n' keeps the complete first retained line.
	lines, cursor, truncated = parseJournalOutput([]byte("old\nkept1\nkept2"), false, len("kept1\nkept2"))
	if !truncated || cursor != "" || !slices.Equal(lines, []string{"kept1", "kept2"}) {
		t.Fatalf("line-boundary cutoff dropped first line: lines=%v truncated=%v", lines, truncated)
	}
}

func TestManagementOriginsRejectsEmptyAndNoncanonicalHostnames(t *testing.T) {
	for _, bad := range []string{"https://:8443", "https://127.1", "https://0xc0a80101", "https://3232235777"} {
		if _, _, err := managementOrigins([]string{bad}); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
	for _, good := range []string{"https://fw.lan", "https://192.168.1.1:8443", "https://[::1]:8443"} {
		if _, _, err := managementOrigins([]string{good}); err != nil {
			t.Fatalf("expected %q to be accepted: %v", good, err)
		}
	}
}

func TestAddonsAPIBeginAndFinishAuditCommitsEvent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("audit store requires root")
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir stateDir: %v", err)
	}
	audits, err := audit.Open(t.Context(), stateDir)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	defer audits.Close()

	root := t.TempDir()
	seedLiveGeneration(t, stateDir, 1, "1", "sample")

	cat := testAPIAddonCatalog(t)
	runner := &fakeAddonRunner{}
	coord := &securityCoordinator{audits: audits}
	api := newAddonsAPI(cat, fakeSnapshotStore{raw: []byte(`{"schemaVersion":4}`)}, stateDir, root, nil, runner, coord)

	rec := httptest.NewRecorder()
	req := withActor(httptest.NewRequest(http.MethodPost, "/api/v1/addons/sample/restart", nil), authz.AddonsManage)
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /restart = %d: %s", rec.Code, rec.Body.String())
	}

	events, err := audits.ListEvents(t.Context(), 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d audit events, want 2 (%+v)", len(events), events)
	}
	if events[0].Action != audit.ActionAddonRestarted || events[0].Target != "sample" || events[0].Result != audit.ResultPending {
		t.Fatalf("unexpected begin audit event: %+v", events[0])
	}
	if events[1].Action != audit.ActionAddonRestarted || events[1].Target != "sample" || events[1].Result != audit.ResultSuccess || events[1].OperationID != events[0].OperationID {
		t.Fatalf("unexpected finish audit event: %+v", events[1])
	}
}

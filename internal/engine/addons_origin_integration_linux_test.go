//go:build integration

package engine

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/identity"
)

// Removing a configured management origin across restarts revokes all active browser sessions so a retired
// host's __Host-andaru_session cookie cannot be replayed if that host is later reassigned to an add-on WebUI.
func TestReconcileManagementOriginsRevokesSessionsOnOriginRemoval(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("identity store requires root")
	}
	ctx := t.Context()
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	identities, err := identity.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer identities.Close()

	if _, _, err := identities.BootstrapAdmin(ctx, "admin", "Admin", []byte("CorrectHorseBatteryStaple1!")); err != nil {
		t.Fatal(err)
	}
	// Pre-upgrade session existing before management-origins.json is created must be revoked once.
	preUpgrade, err := identities.AuthenticatePassword(ctx, "admin", "CorrectHorseBatteryStaple1!", "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	preSel, preSec, ok := splitCredential(preUpgrade.SessionCookie)
	if !ok {
		t.Fatal("invalid session cookie format")
	}
	revoked, err := reconcileManagementOrigins(ctx, stateDir, identities, []string{"https://fw.lan", "https://admin.corp:8443"})
	if err != nil || !revoked {
		t.Fatalf("first reconcile with pre-upgrade session: revoked=%v err=%v", revoked, err)
	}
	if _, err := identities.AuthenticateSession(ctx, preSel, preSec, 0, false); err == nil {
		t.Fatal("pre-upgrade session survived initial management-origins.json creation")
	}

	// Sign in after management-origins.json exists.
	loginRes, err := identities.AuthenticatePassword(ctx, "admin", "CorrectHorseBatteryStaple1!", "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	sel, sec, ok := splitCredential(loginRes.SessionCookie)
	if !ok {
		t.Fatal("invalid session cookie format")
	}

	// Unchanged restart keeps active sessions intact.
	revoked, err = reconcileManagementOrigins(ctx, stateDir, identities, []string{"https://fw.lan", "https://admin.corp:8443"})
	if err != nil || revoked {
		t.Fatalf("unchanged reconcile: revoked=%v err=%v", revoked, err)
	}
	if _, err := identities.AuthenticateSession(ctx, sel, sec, 0, false); err != nil {
		t.Fatalf("session unexpectedly revoked on unchanged restart: %v", err)
	}

	// Adding a third origin keeps active sessions intact.
	revoked, err = reconcileManagementOrigins(ctx, stateDir, identities, []string{"https://fw.lan", "https://admin.corp:8443", "https://mgmt3.lan"})
	if err != nil || revoked {
		t.Fatalf("add-origin reconcile: revoked=%v err=%v", revoked, err)
	}
	if _, err := identities.AuthenticateSession(ctx, sel, sec, 0, false); err != nil {
		t.Fatalf("session unexpectedly revoked on origin addition: %v", err)
	}

	// Dropping https://admin.corp:8443 MUST revoke all active browser sessions!
	revoked, err = reconcileManagementOrigins(ctx, stateDir, identities, []string{"https://fw.lan", "https://mgmt3.lan"})
	if err != nil || !revoked {
		t.Fatalf("drop-origin reconcile: revoked=%v err=%v; want revoked=true", revoked, err)
	}
	if _, err := identities.AuthenticateSession(ctx, sel, sec, 0, false); err == nil {
		t.Fatal("session survived management origin removal")
	}
}

// Multi-origin separation: saving an add-on whose WebUI hostname matches a second configured management origin
// (regardless of port) is rejected at save time.
func TestMultiOriginWebUISeparationRejectedOnSave(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("config store requires root")
	}
	ctx := t.Context()
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.SetCatalog(testAPIAddonCatalog(t))
	store.SetManagementOrigins([]string{"https://fw.lan", "https://admin.corp:8443"})

	actor := authz.Actor{Kind: authz.ActorSystem, ID: "root"}
	// Matching the second management origin (different port) MUST be rejected.
	if _, err := store.Save(ctx, actor, config.Changes{Operations: []config.Operation{{
		Op:         "create",
		Collection: "addons",
		Value:      json.RawMessage(`{"id":"org.andaru.sample","enabled":false,"settings":{"uiHost":"admin.corp"}}`),
	}}}); err == nil {
		t.Fatal("expected rejection when add-on WebUI hostname matches second management origin")
	}
	// Distinct hostname succeeds.
	if _, err := store.Save(ctx, actor, config.Changes{Operations: []config.Operation{{
		Op:         "create",
		Collection: "addons",
		Value:      json.RawMessage(`{"id":"org.andaru.sample","enabled":false,"settings":{"uiHost":"sample.lan"}}`),
	}}}); err != nil {
		t.Fatalf("expected distinct WebUI hostname to save: %v", err)
	}
}

// Config HTTP routes expose /api/v1/config/addons and /api/v1/config/addons/{id} with optimistic locking.
func TestConfigAPIAddonsCRUD(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("config store requires root")
	}
	ctx := t.Context()
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetCatalog(testAPIAddonCatalog(t))

	h := newConfigurationHandler(store)

	// Create
	req := withActor(httptest.NewRequest(http.MethodPost, "/api/v1/config/addons", bytes.NewReader([]byte(`{"id":"org.andaru.sample","enabled":false,"settings":{"uiHost":"sample.lan"}}`))), authz.ConfigurationWrite)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/config/addons = %d: %s", rec.Code, rec.Body.String())
	}

	// Read object
	req = withActor(httptest.NewRequest(http.MethodGet, "/api/v1/config/addons/org.andaru.sample", nil), authz.ConfigurationRead)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/config/addons/org.andaru.sample = %d: %s", rec.Code, rec.Body.String())
	}
	var obj struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil || obj.Version == "" {
		t.Fatalf("decode saved addon object: %s (%v)", rec.Body.String(), err)
	}

	// Replace with If-Match
	req = withActor(httptest.NewRequest(http.MethodPut, "/api/v1/config/addons/org.andaru.sample", bytes.NewReader([]byte(`{"id":"org.andaru.sample","enabled":true,"settings":{"uiHost":"sample2.lan"}}`))), authz.ConfigurationWrite)
	req.Header.Set("If-Match", `"org.andaru.sample.`+obj.Version+`"`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/v1/config/addons/org.andaru.sample = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil || obj.Version == "" {
		t.Fatalf("decode updated addon object: %s (%v)", rec.Body.String(), err)
	}

	// Delete with If-Match
	req = withActor(httptest.NewRequest(http.MethodDelete, "/api/v1/config/addons/org.andaru.sample", nil), authz.ConfigurationWrite)
	req.Header.Set("If-Match", `"org.andaru.sample.`+obj.Version+`"`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/config/addons/org.andaru.sample = %d: %s", rec.Code, rec.Body.String())
	}
}

// Optional add-on secrets can be created with expectedVersion:"0", removed on replace (pruning secrets.db),
// and cleanly re-added with expectedVersion:"0"; forged unresolved:true is rejected.
func TestAddonOptionalSecretPruneAndUnresolvedGuard(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("config store requires root")
	}
	ctx := t.Context()
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	catDir := t.TempDir()
	slugDir := catDir + "/sec"
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "manifestVersion": 1,
  "requires": [{"name": "andaru.extension-api", "version": ">=1.0.0 <2.0.0"}],
  "id": "org.andaru.sec",
  "slug": "sec",
  "name": "Sec Add-on",
  "version": "1.0.0",
  "application": {"name": "Sec", "version": "1.0"},
  "permissions": [{"name": "read", "title": "Read"}],
  "runtime": {"image": "` + testAPIAddonDigest + `", "network": "host"},
  "properties": [{"name": "authKey", "type": "secret-reference", "title": "Auth Key"}]
}`
	if err := os.WriteFile(slugDir+"/manifest.json", []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := addon.LoadCatalog(catDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetCatalog(cat)
	actor := authz.Actor{Kind: authz.ActorSystem, ID: "root"}

	// Forged unresolved:true on create is rejected.
	if _, err := store.Save(ctx, actor, config.Changes{Operations: []config.Operation{{
		Op:         "create",
		Collection: "addons",
		Value:      json.RawMessage(`{"id":"org.andaru.sec","enabled":false,"settings":{"authKey":{"secret":"authKey","unresolved":true}}}`),
	}}}); err == nil {
		t.Fatal("expected forged unresolved:true on add-on create to be rejected")
	}

	// Create with unconfigured secret using expectedVersion:"0".
	res1, err := store.Save(ctx, actor, config.Changes{Operations: []config.Operation{{
		Op:         "create",
		Collection: "addons",
		Value:      json.RawMessage(`{"id":"org.andaru.sec","enabled":false,"settings":{"authKey":{"secret":"authKey","set":"secret-1","expectedVersion":"0"}}}`),
	}}})
	if err != nil {
		t.Fatalf("create with new secret: %v", err)
	}

	// Remove optional secret on replace -> prunes addon:<id> scope entry.
	v1 := res1.SavedVersion
	res2, err := store.Save(ctx, actor, config.Changes{Operations: []config.Operation{{
		Op:              "replace",
		Collection:      "addons",
		ID:              "org.andaru.sec",
		ExpectedVersion: &v1,
		Value:           json.RawMessage(`{"id":"org.andaru.sec","enabled":false}`),
	}}})
	if err != nil {
		t.Fatalf("replace removing secret: %v", err)
	}

	// Re-adding the removed secret with expectedVersion:"0" must succeed (not 412!).
	v2 := res2.SavedVersion
	if _, err := store.Save(ctx, actor, config.Changes{Operations: []config.Operation{{
		Op:              "replace",
		Collection:      "addons",
		ID:              "org.andaru.sec",
		ExpectedVersion: &v2,
		Value:           json.RawMessage(`{"id":"org.andaru.sec","enabled":false,"settings":{"authKey":{"secret":"authKey","set":"secret-2","expectedVersion":"0"}}}`),
	}}}); err != nil {
		t.Fatalf("re-add removed secret with expectedVersion:0 failed: %v", err)
	}
}

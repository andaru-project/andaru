//go:build linux && integration

package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/identity"
)

func TestWebSetupBootstrapEndToEnd(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("identity store requires root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ctx := context.Background()
	if err := initializeAuthorizationState(ctx, dir); err != nil {
		t.Fatalf("initializeAuthorizationState: %v", err)
	}
	identities, err := identity.Open(ctx, dir)
	if err != nil {
		t.Fatalf("identity.Open: %v", err)
	}
	defer func() { _ = identities.Close() }()
	audits, err := audit.Open(ctx, dir)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	defer func() { _ = audits.Close() }()
	cfgStore, err := config.Open(ctx, dir)
	if err != nil {
		t.Fatalf("config.Open: %v", err)
	}
	defer func() { _ = cfgStore.Close() }()

	coordinator := newSecurityCoordinator(identities, audits, dir)
	codes := newSetupCodeStore(dir)
	codeHandler := newSetupCodeHandler(identities, codes)
	rootAPI := newHandlerWithAllServices(nil, http.NotFoundHandler(), nil, nil, nil, nil, codeHandler, nil, nil, nil)
	webHandler, err := newWebAPIWithSetupCodes(identities, cfgStore, coordinator, rootAPI, nil, codes, dir, []string{"https://andaru.local"})
	if err != nil {
		t.Fatalf("newWebAPIWithSetupCodes: %v", err)
	}

	webReq := func(method, path, body, source string) *http.Request {
		req := httptest.NewRequest(method, "https://andaru.local"+path, strings.NewReader(body))
		req.Host = "andaru.local"
		req.Header.Set("Origin", "https://andaru.local")
		req.Header.Set(WebSourceHeader, source)
		req.Header.Set("Content-Type", "application/json")
		return req.WithContext(context.WithValue(req.Context(), peerKey{}, peerIdentity{uid: 999, verified: true, web: true}))
	}

	// /api/v1/setup/code is NOT reachable via web socket.
	rec := httptest.NewRecorder()
	webHandler.ServeHTTP(rec, webReq(http.MethodGet, "/api/v1/setup/code", "", "192.0.2.10"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("web GET /api/v1/setup/code: got %d, want 404", rec.Code)
	}

	// Non-root peer on rootAPI gets 403 for /api/v1/setup/code.
	nonRootReq := httptest.NewRequest(http.MethodGet, "http://unix/api/v1/setup/code", nil)
	nonRootReq = nonRootReq.WithContext(context.WithValue(nonRootReq.Context(), peerKey{}, peerIdentity{uid: 1000, verified: true}))
	rec = httptest.NewRecorder()
	rootAPI.ServeHTTP(rec, nonRootReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-root GET /api/v1/setup/code: got %d, want 403", rec.Code)
	}

	// Root local socket returns setupRequired=true and the active setup code.
	rec = httptest.NewRecorder()
	rootAPI.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/setup/code"))
	if rec.Code != http.StatusOK {
		t.Fatalf("root GET /api/v1/setup/code: %d %s", rec.Code, rec.Body.String())
	}
	var codeReply setupCodeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &codeReply); err != nil || !codeReply.SetupRequired || codeReply.SetupCode == "" {
		t.Fatalf("unexpected setup code reply: %+v (%v)", codeReply, err)
	}

	// Missing/bad Origin is rejected with 403.
	badOrigin := webReq(http.MethodPost, "/api/v1/setup/bootstrap", `{}`, "192.0.2.10")
	badOrigin.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	webHandler.ServeHTTP(rec, badOrigin)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad origin: got %d, want 403", rec.Code)
	}

	// Existing Authorization header or cookie is rejected with 400.
	withAuth := webReq(http.MethodPost, "/api/v1/setup/bootstrap", `{}`, "192.0.2.10")
	withAuth.Header.Set("Authorization", "Bearer tok")
	rec = httptest.NewRecorder()
	webHandler.ServeHTTP(rec, withAuth)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous credential: got %d, want 400", rec.Code)
	}

	// Five bad attempts from one source hit HTTP 429 RATE_LIMITED.
	badPayload := `{"setupCode":"0000-0000-0000-0000","login":"admin","displayName":"Admin","password":"correct-horse-battery-9"}`
	for i := range setupMaxFailedAttempts {
		rec = httptest.NewRecorder()
		webHandler.ServeHTTP(rec, webReq(http.MethodPost, "/api/v1/setup/bootstrap", badPayload, "192.0.2.50"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bad code attempt %d: got %d, want 401 (%s)", i, rec.Code, rec.Body.String())
		}
	}
	rec = httptest.NewRecorder()
	webHandler.ServeHTTP(rec, webReq(http.MethodPost, "/api/v1/setup/bootstrap", badPayload, "192.0.2.50"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th bad attempt: got %d, want 429 (%s)", rec.Code, rec.Body.String())
	}

	// Valid setup code from another source creates admin on a fresh box (no live generation yet!) and signs in.
	payload, _ := json.Marshal(webSetupBootstrapRequest{
		SetupCode:   codeReply.SetupCode,
		Login:       "admin",
		DisplayName: "Administrator",
		Password:    "correct-horse-battery-9",
	})
	rec = httptest.NewRecorder()
	webHandler.ServeHTTP(rec, webReq(http.MethodPost, "/api/v1/setup/bootstrap", string(payload), "192.0.2.10"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid setup bootstrap: got %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var session webSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil || session.Account.Login != "admin" || session.CSRFToken == "" {
		t.Fatalf("unexpected bootstrap session: %+v (%v)", session, err)
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("expected session cookie on bootstrap response")
	}

	// setup-code removed and root GET now returns setupRequired=false with no code.
	if _, err := os.Stat(filepath.Join(dir, "setup-code")); !os.IsNotExist(err) {
		t.Fatalf("setup-code must be removed after bootstrap, got %v", err)
	}
	rec = httptest.NewRecorder()
	rootAPI.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/setup/code"))
	codeReply = setupCodeResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &codeReply); err != nil || codeReply.SetupRequired || codeReply.SetupCode != "" {
		t.Fatalf("root GET after bootstrap: got %+v (%v)", codeReply, err)
	}

	// Second bootstrap attempt returns 409 ALREADY_CONFIGURED.
	rec = httptest.NewRecorder()
	webHandler.ServeHTTP(rec, webReq(http.MethodPost, "/api/v1/setup/bootstrap", string(payload), "192.0.2.10"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second bootstrap: got %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

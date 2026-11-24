//go:build linux

package engine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/identity"
)

func TestWebSetupBootstrapEndToEnd(t *testing.T) {
	dir := t.TempDir()
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
	rootAPI := newHandlerWithAllServices(nil, http.NotFoundHandler(), nil, nil, nil, nil, codeHandler, nil)
	webHandler, err := newWebAPIWithSetupCodes(identities, cfgStore, coordinator, rootAPI, nil, codes, dir, []string{"https://andaru.local"})
	if err != nil {
		t.Fatalf("newWebAPIWithSetupCodes: %v", err)
	}

	webReq := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, "https://andaru.local"+path, strings.NewReader(body))
		req.Host = "andaru.local"
		req.Header.Set("Origin", "https://andaru.local")
		req.Header.Set(WebSourceHeader, "192.0.2.10")
		req.Header.Set("Content-Type", "application/json")
		return req.WithContext(context.WithValue(req.Context(), peerKey{}, peerIdentity{uid: 999, verified: true, web: true}))
	}

	// /api/v1/setup/code is NOT reachable via web socket.
	rec := httptest.NewRecorder()
	webHandler.ServeHTTP(rec, webReq(http.MethodGet, "/api/v1/setup/code", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("web GET /api/v1/setup/code: got %d, want 404", rec.Code)
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

	// Wrong setup code is rejected with 401.
	rec = httptest.NewRecorder()
	webHandler.ServeHTTP(rec, webReq(http.MethodPost, "/api/v1/setup/bootstrap", `{"setupCode":"0000-0000-0000-0000","login":"admin","displayName":"Admin","password":"correct-horse-battery-9"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong setup code: got %d, want 401 (%s)", rec.Code, rec.Body.String())
	}

	// Valid setup code + valid SSH public key creates admin, writes authorized_keys, clears code, and signs in.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	keyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " ops@workstation"
	payload, _ := json.Marshal(webSetupBootstrapRequest{
		SetupCode:    codeReply.SetupCode,
		Login:        "admin",
		DisplayName:  "Administrator",
		Password:     "correct-horse-battery-9",
		SSHPublicKey: keyStr,
	})
	rec = httptest.NewRecorder()
	webHandler.ServeHTTP(rec, webReq(http.MethodPost, "/api/v1/setup/bootstrap", string(payload)))
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

	// authorized_keys written with 0600, setup-code removed.
	ak, err := os.ReadFile(filepath.Join(dir, "ssh", "authorized_keys"))
	if err != nil || !strings.Contains(string(ak), "ops@workstation") {
		t.Fatalf("authorized_keys missing or unexpected: %q (%v)", string(ak), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "setup-code")); !os.IsNotExist(err) {
		t.Fatalf("setup-code must be removed after bootstrap, got %v", err)
	}

	// Second bootstrap attempt returns 409 ALREADY_CONFIGURED.
	rec = httptest.NewRecorder()
	webHandler.ServeHTTP(rec, webReq(http.MethodPost, "/api/v1/setup/bootstrap", string(payload)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second bootstrap: got %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

//go:build integration

package engine

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/identity"
)

// An upgrade may compute authorization digests differently from the release that applied the live generation, as
// one that changed them to leave the built-in roles out does. That must end the sessions, never refuse everyone.
func TestLiveDigestFromAnOlderReleaseEndsSessionsInsteadOfLockingEveryoneOut(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned stores require root")
	}
	ctx := context.Background()
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	liveInput, before := liveGenerationFixture(t, ctx, stateDir, store)

	legacy := strings.Repeat("ab", 32)
	id, err := generation.Create(generation.Dir(stateDir), generation.Manifest{
		SavedVersion: string(liveInput.SavedVersion), AuthorizationDigest: legacy, IdentityRevision: 1, ConfirmTimeoutSeconds: 120,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(generation.Dir(stateDir), "pointers.json"), fmt.Appendf(nil, `{"confirmed":%d,"lastKnownGood":null,"pending":null,"live":%d}`, id, id), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = LiveAuthorization(ctx, stateDir, store); err == nil {
		t.Fatal("a live digest the authorization state does not hold was accepted before the engine started")
	}

	if err = initializeAuthorizationState(ctx, stateDir); err != nil {
		t.Fatalf("engine start refused the older release's digest: %v", err)
	}
	snapshot, epoch, err := LiveAuthorization(ctx, stateDir, store)
	if err != nil {
		t.Fatalf("live authorization after engine start: %v", err)
	}
	if epoch != before.AuthorizationEpoch+1 || len(snapshot.Roles) != 1 || snapshot.Roles[0].Name != "Live operator" {
		t.Fatalf("live authorization = %+v epoch=%d, want the live roles at epoch %d", snapshot, epoch, before.AuthorizationEpoch+1)
	}
	if err = initializeAuthorizationState(ctx, stateDir); err != nil {
		t.Fatal(err)
	}
	if _, again, err := LiveAuthorization(ctx, stateDir, store); err != nil || again != epoch {
		t.Fatalf("a second start moved the epoch again: %d, error=%v", again, err)
	}
}

// The session cookie is HttpOnly, so a browser holding one whose session has ended cannot drop it: sign-in replaces
// it. A cookie that still authenticates keeps being refused.
func TestLoginReplacesAnEndedSessionCookieButRefusesALiveOne(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned stores require root")
	}
	ctx := context.Background()
	stateDir := filepath.Join(t.TempDir(), "state")
	configuration, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer configuration.Close()
	store, err := identity.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const password = "unique-admin-password-41"
	if _, _, err = store.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte(password)); err != nil {
		t.Fatal(err)
	}
	liveGenerationFixture(t, ctx, stateDir, configuration)
	const origin = "https://router.example"
	handler := &webAPI{identities: store, configuration: configuration, stateDir: stateDir, origins: map[string]struct{}{origin: {}}, hosts: map[string]struct{}{"router.example": {}}}
	var live identity.LoginResult
	if err = handler.withLiveAuthorization(ctx, func(_ authz.Snapshot, epoch uint64) error {
		var loginErr error
		live, loginErr = store.AuthenticatePassword(ctx, "rootadmin", password, "192.0.2.20", epoch)
		return loginErr
	}); err != nil {
		t.Fatal(err)
	}
	ended := base64.RawURLEncoding.EncodeToString(make([]byte, 16)) + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))

	// The body is refused after the credential checks, so nothing past them (the audit store) is reached.
	login := func(cookie string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(`not json`))
		r.Host = "router.example"
		r.Header.Set(WebSourceHeader, "192.0.2.20")
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: webSessionCookie, Value: cookie})
		r = r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 123, verified: true, web: true}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	if response := login(ended); strings.Contains(response.Body.String(), "AMBIGUOUS_CREDENTIALS") || response.Code != http.StatusBadRequest {
		t.Fatalf("ended session cookie: status=%d body=%s, want the body refused, not the cookie", response.Code, response.Body)
	}
	if response := login(live.SessionCookie); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "AMBIGUOUS_CREDENTIALS") {
		t.Fatalf("live session cookie: status=%d body=%s, want AMBIGUOUS_CREDENTIALS", response.Code, response.Body)
	}
}

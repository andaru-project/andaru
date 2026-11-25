//go:build integration

package engine

import (
	"context"
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

// The live generation's digest may differ from the one the authorization state holds (a legacy manifest, an
// activation cut short) and may not be what this release computes from its roles. Sessions end; no one is refused.
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
	signIn := func() string {
		t.Helper()
		var login identity.LoginResult
		if err := handler.withLiveAuthorization(ctx, func(_ authz.Snapshot, epoch uint64) error {
			var loginErr error
			login, loginErr = store.AuthenticatePassword(ctx, "rootadmin", password, "192.0.2.20", epoch)
			return loginErr
		}); err != nil {
			t.Fatal(err)
		}
		return login.SessionCookie
	}
	live, ended := signIn(), signIn()
	selector, secret, _ := strings.Cut(ended, ".")
	if err = store.Logout(ctx, selector, secret); err != nil {
		t.Fatal(err)
	}

	// The body is refused after the credential checks, so nothing past them (the audit store) is reached.
	login := func(cookies ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(`not json`))
		r.Host = "router.example"
		r.Header.Set(WebSourceHeader, "192.0.2.20")
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			r.AddCookie(&http.Cookie{Name: webSessionCookie, Value: cookie})
		}
		r = r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 123, verified: true, web: true}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	if response := login(ended); strings.Contains(response.Body.String(), "AMBIGUOUS_CREDENTIALS") || response.Code != http.StatusBadRequest {
		t.Fatalf("ended session cookie: status=%d body=%s, want the body refused, not the cookie", response.Code, response.Body)
	}
	for name, cookies := range map[string][]string{"live session cookie": {live}, "ended and live session cookies": {ended, live}} {
		if response := login(cookies...); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "AMBIGUOUS_CREDENTIALS") {
			t.Fatalf("%s: status=%d body=%s, want AMBIGUOUS_CREDENTIALS", name, response.Code, response.Body)
		}
	}
	// Whether a cookie still works cannot be told while authorization is changing: the request is turned away.
	lock, err := authz.Acquire(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	err = lock.SetPending(authz.PendingTransition{GenerationID: 1, Phase: authz.TransitionActivate, IdentityRevision: 1, AuthorizationDigest: strings.Repeat("ab", 32)})
	lock.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response := login(ended); response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "AUTHENTICATION_UNAVAILABLE") {
		t.Fatalf("cookie during an authorization change: status=%d body=%s, want AUTHENTICATION_UNAVAILABLE", response.Code, response.Body)
	}
}

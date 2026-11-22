//go:build integration

package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/identity"
)

func TestWebPreferencesBelongToTheSignedInAccount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned stores require root")
	}
	ctx := context.Background()
	stateDir := t.TempDir() + "/state"
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
	second, revision, err := store.CreateAccount(ctx, identity.CreateAccountInput{Login: "second", DisplayName: "Second", Kind: identity.Human, Enabled: true, RoleIDs: []string{"read-only"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetPassword(ctx, second.ID, revision, []byte(password)); err != nil {
		t.Fatal(err)
	}
	// A session needs a live generation to take its authorization from.
	liveGenerationFixture(t, ctx, stateDir, configuration)
	const origin = "https://router.example"
	handler := &webAPI{identities: store, configuration: configuration, stateDir: stateDir, origins: map[string]struct{}{origin: {}}, hosts: map[string]struct{}{"router.example": {}}}
	signIn := func(name string) identity.LoginResult {
		t.Helper()
		var login identity.LoginResult
		if err := handler.withLiveAuthorization(ctx, func(_ authz.Snapshot, epoch uint64) error {
			var loginErr error
			login, loginErr = store.AuthenticatePassword(ctx, name, password, "192.0.2.20", epoch)
			return loginErr
		}); err != nil {
			t.Fatal(err)
		}
		return login
	}
	login := signIn("rootadmin")

	// call sends one request as the browser behind andaru-web would; each option changes one thing about it.
	type option func(*http.Request)
	call := func(method, body string, options ...option) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin+"/api/v1/session/preferences", strings.NewReader(body))
		r.Host = "router.example"
		r.Header.Set(WebSourceHeader, "192.0.2.20")
		r.AddCookie(&http.Cookie{Name: webSessionCookie, Value: login.SessionCookie})
		if method != http.MethodGet {
			r.Header.Set("Origin", origin)
			r.Header.Set(webCSRFHeader, login.CSRFSecret)
			r.Header.Set("Content-Type", "application/json")
		}
		for _, change := range options {
			change(r)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 123, verified: true, web: true})))
		return response
	}
	header := func(name, value string) option { return func(r *http.Request) { r.Header.Set(name, value) } }
	without := func(name string) option { return func(r *http.Request) { r.Header.Del(name) } }
	expect := func(response *httptest.ResponseRecorder, status int, body string) {
		t.Helper()
		if response.Code != status || !strings.Contains(response.Body.String(), body) {
			t.Fatalf("want %d with %q, got %d %s", status, body, response.Code, response.Body)
		}
	}

	expect(call(http.MethodGet, ""), http.StatusOK, "{}")
	// The answer is the stored bytes, not a re-encoding: "<" stays "<".
	const layout = `{"dashboard": {"order": ["interfaces", "changes"], "hidden": ["prefixes"], "title": "a<b"}}`
	for _, response := range []*httptest.ResponseRecorder{call(http.MethodPut, layout), call(http.MethodGet, "")} {
		if response.Code != http.StatusOK || response.Body.String() != layout {
			t.Fatalf("want the stored document, got %d %s", response.Code, response.Body)
		}
	}

	// A refused write leaves the stored document as it was.
	expect(call(http.MethodPut, `{}`, without("Origin")), http.StatusForbidden, "ORIGIN_FAILED")
	expect(call(http.MethodPut, `{}`, header("Origin", "https://evil.example")), http.StatusForbidden, "ORIGIN_FAILED")
	expect(call(http.MethodPut, `{}`, without(webCSRFHeader)), http.StatusForbidden, "CSRF_FAILED")
	expect(call(http.MethodPut, `{}`, header("Content-Type", "text/plain")), http.StatusUnsupportedMediaType, "UNSUPPORTED_CONTENT")
	expect(call(http.MethodPut, `[]`), http.StatusBadRequest, "INVALID_REQUEST")
	expect(call(http.MethodPut, `{"a":"`+strings.Repeat("x", maxPreferencesBytes)+`"}`), http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE")
	query := func(r *http.Request) { r.URL.RawQuery = "x=1" }
	expect(call(http.MethodGet, "", query), http.StatusBadRequest, "INVALID_REQUEST")
	expect(call(http.MethodPut, `{}`, query), http.StatusBadRequest, "INVALID_REQUEST")
	expect(call(http.MethodGet, `{}`), http.StatusBadRequest, "INVALID_REQUEST")
	expect(call(http.MethodPost, `{}`), http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
	expect(call(http.MethodGet, "", without("Cookie")), http.StatusUnauthorized, "")
	expect(call(http.MethodPut, `{}`, without("Cookie")), http.StatusUnauthorized, "")
	expect(call(http.MethodGet, ""), http.StatusOK, layout)

	// Another account signs in: it sees its own document, not the first one's, and writes only its own.
	first := login
	login = signIn("second")
	if response := call(http.MethodGet, ""); response.Code != http.StatusOK || response.Body.String() != "{}" {
		t.Fatalf("a second account read %d %s", response.Code, response.Body)
	}
	expect(call(http.MethodPut, `{"mine":true}`), http.StatusOK, `{"mine":true}`)
	login = first
	if response := call(http.MethodGet, ""); response.Body.String() != layout {
		t.Fatalf("the second account's save changed the first one's document: %s", response.Body)
	}

	// A read marked as a poll leaves the session's idle time running; an unmarked one resets it.
	lastSeen := func() time.Time {
		t.Helper()
		var sessions []identity.Session
		if err := handler.withLiveAuthorization(ctx, func(_ authz.Snapshot, epoch uint64) error {
			var listErr error
			sessions, listErr = store.ListSessions(ctx, login.Account.ID, epoch)
			return listErr
		}); err != nil || len(sessions) != 1 {
			t.Fatalf("sessions %+v: %v", sessions, err)
		}
		return sessions[0].LastSeenAt
	}
	before := lastSeen()
	time.Sleep(1100 * time.Millisecond) // the stored time has whole seconds
	expect(call(http.MethodGet, "", header("X-Andaru-Background", "1")), http.StatusOK, layout)
	if after := lastSeen(); !after.Equal(before) {
		t.Fatalf("a poll moved the session's last activity from %v to %v", before, after)
	}
	expect(call(http.MethodGet, ""), http.StatusOK, layout)
	if after := lastSeen(); !after.After(before) {
		t.Fatalf("a read left the session's last activity at %v", after)
	}
}

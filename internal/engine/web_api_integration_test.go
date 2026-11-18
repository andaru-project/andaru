//go:build integration

package engine

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/identity"
)

func TestWebStartupStatusAndRootBootstrapIsolation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("identity store requires root")
	}
	store, err := identity.Open(context.Background(), t.TempDir()+"/state")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := &webAPI{identities: store, origins: map[string]struct{}{"https://router.example": {}}, hosts: map[string]struct{}{"router.example": {}}}
	request := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, "https://router.example"+path, nil)
		r.Host = "router.example"
		r.Header.Set(WebSourceHeader, "192.0.2.20")
		return r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 123, verified: true, web: true}))
	}
	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, request(http.MethodGet, "/api/v1/startup-status"))
	if initial.Code != http.StatusOK || initial.Body.String() != "{\"setupRequired\":true}\n" {
		t.Fatalf("initial startup status=%d body=%s", initial.Code, initial.Body)
	}
	if _, _, err = store.BootstrapAdmin(context.Background(), "rootadmin", "Administrator", []byte("unique-admin-password-41")); err != nil {
		t.Fatal(err)
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, request(http.MethodGet, "/api/v1/startup-status"))
	if ready.Code != http.StatusOK || ready.Body.String() != "{\"setupRequired\":false}\n" {
		t.Fatalf("ready startup status=%d body=%s", ready.Code, ready.Body)
	}
	bootstrap := httptest.NewRecorder()
	handler.ServeHTTP(bootstrap, request(http.MethodPost, "/api/v1/users/bootstrap-admin"))
	if bootstrap.Code != http.StatusNotFound {
		t.Fatalf("root bootstrap route reached web: status=%d body=%s", bootstrap.Code, bootstrap.Body)
	}
}

func TestWebCredentialWireCookieAndAmbiguityContracts(t *testing.T) {
	selector := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	gotSelector, gotSecret, ok := bearerCredential([]string{"Bearer " + selector + "." + secret})
	if !ok || gotSelector != selector || gotSecret != secret {
		t.Fatal("canonical selector.secret bearer was rejected")
	}
	for _, value := range []string{
		"Bearer " + selector + secret,
		"Bearer " + selector + "." + secret + ".extra",
		"Bearer " + selector[:len(selector)-1] + "." + secret,
		"Bearer " + selector + "." + strings.Repeat("A", len(secret)-1),
		"Basic " + selector + "." + secret,
	} {
		if _, _, ok = bearerCredential([]string{value}); ok {
			t.Fatalf("invalid bearer %q accepted", value)
		}
	}
	response := httptest.NewRecorder()
	expires := time.Now().Add(time.Hour).UTC()
	setSessionCookie(response, "selector.secret", expires)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-andaru_session" || cookies[0].Path != "/" || cookies[0].Domain != "" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe session cookie: %+v", cookies)
	}
	if raw := response.Header().Get("Set-Cookie"); !strings.Contains(raw, "Secure") || !strings.Contains(raw, "HttpOnly") || !strings.Contains(raw, "SameSite=Strict") || strings.Contains(raw, "Domain=") {
		t.Fatalf("unsafe Set-Cookie: %q", raw)
	}

	handler := &webAPI{hosts: map[string]struct{}{"router.example": {}}}
	request := httptest.NewRequest(http.MethodGet, "https://router.example/api/v1/status/links", nil)
	request.Host = "router.example"
	request.Header.Set(WebSourceHeader, "192.0.2.20")
	request.Header.Set("Authorization", "Bearer "+selector+"."+secret)
	request.AddCookie(&http.Cookie{Name: webSessionCookie, Value: "session.secret"})
	request = request.WithContext(context.WithValue(request.Context(), peerKey{}, peerIdentity{uid: 123, verified: true, web: true}))
	ambiguous := httptest.NewRecorder()
	handler.ServeHTTP(ambiguous, request)
	if ambiguous.Code != http.StatusBadRequest || !strings.Contains(ambiguous.Body.String(), "AMBIGUOUS_CREDENTIALS") {
		t.Fatalf("ambiguous credentials status=%d body=%s", ambiguous.Code, ambiguous.Body)
	}
}

func TestWebOriginAndCapabilityResolutionAreExact(t *testing.T) {
	origins, hosts, err := managementOrigins([]string{"https://router.example:8443"})
	if err != nil || len(origins) != 1 || len(hosts) != 1 {
		t.Fatalf("valid origin rejected: origins=%v hosts=%v err=%v", origins, hosts, err)
	}
	for _, origin := range []string{"http://router.example:8443", "https://router.example:8443/", "https://router.example:8443/path", "https://user@router.example:8443"} {
		if _, _, err = managementOrigins([]string{origin}); err == nil {
			t.Fatalf("invalid origin %q accepted", origin)
		}
	}
	role := "rol_00000000-0000-4000-8000-000000000001"
	resolved := resolveSnapshot(authz.Snapshot{Capabilities: map[string][]authz.Capability{
		authz.AdministratorRoleID: {authz.UsersManage, authz.StatusRead}, role: {authz.StatusRead, authz.ConfigurationRead},
	}}, []string{role, authz.AdministratorRoleID})
	want := []authz.Capability{authz.ConfigurationRead, authz.StatusRead, authz.UsersManage}
	if len(resolved) != len(want) {
		t.Fatalf("resolved capabilities=%v", resolved)
	}
	for i := range want {
		if resolved[i] != want[i] {
			t.Fatalf("resolved capabilities=%v want=%v", resolved, want)
		}
	}
}

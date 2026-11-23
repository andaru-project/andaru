//go:build integration

package engine

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/identity"
)

// The event stream sends a signed-in reader each saved head and each apply's state, and ends with its session.
func TestEventStreamFollowsTheSession(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned stores require root")
	}
	defer func(previous time.Duration) { eventsHeartbeat = previous }(eventsHeartbeat)
	eventsHeartbeat = 50 * time.Millisecond
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
	events, err := bus.Open(bus.Options{Path: filepath.Join(t.TempDir(), "bus.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	configuration.OnSave(publishHead(events))
	const password = "unique-admin-password-41"
	if _, _, err = store.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte(password)); err != nil {
		t.Fatal(err)
	}
	nobody, revision, err := store.CreateAccount(ctx, identity.CreateAccountInput{Login: "nobody", DisplayName: "Nobody", Kind: identity.Human, Enabled: true, RoleIDs: []string{}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetPassword(ctx, nobody.ID, revision, []byte(password)); err != nil {
		t.Fatal(err)
	}
	liveGenerationFixture(t, ctx, stateDir, configuration)
	audits, err := audit.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer audits.Close()
	handler := &webAPI{identities: store, configuration: configuration, events: events, stateDir: stateDir,
		coordinator: newSecurityCoordinator(store, audits, stateDir), hosts: map[string]struct{}{"router.example": {}}}
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 123, verified: true, web: true})))
	}))
	defer server.Close()
	open := func(method string, login *identity.LoginResult) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, server.URL+eventsPath, nil)
		r.Host = "router.example"
		r.Header.Set(WebSourceHeader, "192.0.2.20")
		if login != nil {
			r.AddCookie(&http.Cookie{Name: webSessionCookie, Value: login.SessionCookie})
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	admin := signIn("rootadmin")
	none := signIn("nobody")
	for _, refused := range []struct {
		method string
		login  *identity.LoginResult
		status int
	}{{http.MethodGet, nil, http.StatusUnauthorized}, {http.MethodPost, &admin, http.StatusMethodNotAllowed}, {http.MethodGet, &none, http.StatusForbidden}} {
		if response := open(refused.method, refused.login); response.StatusCode != refused.status {
			t.Fatalf("%s: %d, want %d", refused.method, response.StatusCode, refused.status)
		}
	}

	response := open(http.MethodGet, &admin)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	next := func(want string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				if strings.HasPrefix(line, "data: ") && strings.Contains(line, want) {
					return
				}
			case <-deadline:
				t.Fatalf("no %q", want)
			}
		}
	}
	if _, err = configuration.Save(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, config.Changes{Operations: []config.Operation{{Op: "create", Collection: "zones", Value: []byte(`{"name":"lab"}`)}}}); err != nil {
		t.Fatal(err)
	}
	next(`"type":"config.head.changed"`)
	payload, _ := bus.NewEvent("apply.changed", "realize", map[string]int{"id": 3})
	if err = events.Publish("apply/3", payload, false); err != nil {
		t.Fatal(err)
	}
	next(`"id":3`)

	// Opening and keeping the stream are not activity: they leave the session to its idle timeout.
	if userActivity(httptest.NewRequest(http.MethodGet, eventsPath, nil)) {
		t.Fatal("the event stream counts as activity")
	}

	selector, secret, _ := splitCredential(admin.SessionCookie)
	if err = store.Logout(ctx, selector, secret); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the stream outlived its session")
		}
	}
}

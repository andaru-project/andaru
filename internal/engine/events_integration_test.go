//go:build integration

package engine

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if revision, err = store.SetPassword(ctx, nobody.ID, revision, []byte(password)); err != nil {
		t.Fatal(err)
	}
	reader, revision, err := store.CreateAccount(ctx, identity.CreateAccountInput{Login: "reader", DisplayName: "Reader", Kind: identity.Human, Enabled: true, RoleIDs: []string{authz.ReadOnlyRoleID}}, revision)
	if err != nil {
		t.Fatal(err)
	}
	if revision, err = store.SetPassword(ctx, reader.ID, revision, []byte(password)); err != nil {
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
	readerLogin := signIn("reader")
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
				if strings.Contains(line, "injected") {
					t.Fatalf("a payload that is not JSON reached the stream: %q", line)
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
	// A payload that is not JSON could carry line breaks that end its event and add fields: it is dropped.
	if err = events.Publish("apply/9", []byte("{}\r\rdata: injected"), false); err != nil {
		t.Fatal(err)
	}
	payload, _ := bus.NewEvent("apply.changed", "realize", map[string]int{"id": 3})
	if err = events.Publish("apply/3", payload, false); err != nil {
		t.Fatal(err)
	}
	next(`"id":3`)

	// Opening and keeping the stream are not activity: they leave the session to its idle timeout.
	if userActivity(httptest.NewRequest(http.MethodGet, eventsPath, nil)) {
		t.Fatal("the event stream counts as activity")
	}

	// A browser that stops reading loses its stream once 16 events wait for it, and the publisher never waits.
	stalled := &stalledWriter{header: http.Header{}, flushed: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest(http.MethodGet, "https://router.example"+eventsPath, nil)
	request.Header.Set(WebSourceHeader, "192.0.2.20")
	request.AddCookie(&http.Cookie{Name: webSessionCookie, Value: admin.SessionCookie})
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		handler.ServeHTTP(stalled, request.WithContext(context.WithValue(request.Context(), peerKey{}, peerIdentity{uid: 123, verified: true, web: true})))
	}()
	<-stalled.flushed
	for range 20 {
		if err = events.Publish("apply/3", payload, false); err != nil {
			t.Fatal(err)
		}
	}
	close(stalled.release)
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled stream was kept")
	}

	// A stream ends when its account loses configuration.read, though its session goes on.
	lost := open(http.MethodGet, &readerLogin)
	defer lost.Body.Close()
	if lost.StatusCode != http.StatusOK {
		t.Fatalf("reader stream: %d", lost.StatusCode)
	}
	if _, _, err = store.UpdateAccount(ctx, reader.ID, revision, identity.UpdateAccountInput{RoleIDs: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	lostEnded := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, lost.Body); lostEnded <- err }()
	select {
	case <-lostEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived the account's configuration.read")
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

// stalledWriter is a browser that takes the stream's headers and then stops reading until released.
type stalledWriter struct {
	header           http.Header
	flushed, release chan struct{}
	once             sync.Once
}

func (w *stalledWriter) Header() http.Header { return w.header }
func (w *stalledWriter) WriteHeader(int)     {}
func (w *stalledWriter) Flush()              { w.once.Do(func() { close(w.flushed) }) }
func (w *stalledWriter) Write(p []byte) (int, error) {
	<-w.release
	return len(p), nil
}

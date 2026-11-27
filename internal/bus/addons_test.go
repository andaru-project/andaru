package bus

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTokenBus(t *testing.T) (b *Bus, rootSock, addonSock string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bus-addon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	rootSock = filepath.Join(dir, "root.sock")
	addonSock = filepath.Join(dir, "addon.sock")
	rl, err := net.Listen("unix", rootSock)
	if err != nil {
		t.Fatal(err)
	}
	al, err := net.Listen("unix", addonSock)
	if err != nil {
		rl.Close()
		t.Fatal(err)
	}
	b, err = Open(Options{
		Path: filepath.Join(dir, "bus.db"),
		Grants: map[string]Grants{
			"realize": {Publish: []string{"apply/+"}},
		},
		// Root socket trusts whoever reaches it, as in engine/events.go.
		Authenticate:   func(_, _ string) bool { return true },
		Listeners:      map[string]net.Listener{"root": rl},
		TokenListeners: map[string]net.Listener{"addons": al},
	})
	if err != nil {
		rl.Close()
		al.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, rootSock, addonSock
}

func TestTokenListenerIsolationAndAuth(t *testing.T) {
	b, rootSock, addonSock := openTokenBus(t)

	if err := b.SetClient("addon.sample", "tok-secret-1", Grants{
		Publish:   []string{"notification/addon.sample", "addon/sample/status"},
		Subscribe: []string{"apply/+"},
	}); err != nil {
		t.Fatalf("SetClient: %v", err)
	}

	// Add-on cannot impersonate root/realize on the add-on socket.
	if c, err := Dial(ctx(t), addonSock, "realize", ""); err == nil {
		c.Close()
		t.Fatal("realize was accepted on TokenListener")
	}
	if c, err := Dial(ctx(t), addonSock, "realize", "tok-secret-1"); err == nil {
		c.Close()
		t.Fatal("realize with addon token was accepted on TokenListener")
	}

	// Wrong, empty, or unknown-user token refused on add-on socket.
	for _, tc := range []struct{ user, tok string }{
		{"addon.sample", ""},
		{"addon.sample", "wrong-token"},
		{"addon.other", "tok-secret-1"},
		{"", "tok-secret-1"},
	} {
		if c, err := Dial(ctx(t), addonSock, tc.user, tc.tok); err == nil {
			c.Close()
			t.Fatalf("expected refusal for (%q, %q)", tc.user, tc.tok)
		}
	}

	// Dynamic add-on principal is NOT recognized on root Listeners (complete namespace isolation).
	if c, err := Dial(ctx(t), rootSock, "addon.sample", "tok-secret-1"); err == nil {
		c.Close()
		t.Fatal("dynamic addon client was accepted on root Listener")
	}

	// Root realize still works on root socket.
	rc, err := Dial(ctx(t), rootSock, "realize", "")
	if err != nil {
		t.Fatalf("root realize dial: %v", err)
	}
	defer rc.Close()

	// Valid add-on connects on add-on socket, receives granted events, and gets error frames on denied topics without disconnect.
	ac, err := Dial(ctx(t), addonSock, "addon.sample", "tok-secret-1")
	if err != nil {
		t.Fatalf("addon dial: %v", err)
	}
	defer ac.Close()

	if err := ac.Subscribe(ctx(t), "apply/+"); err != nil {
		t.Fatalf("subscribe granted topic: %v", err)
	}

	// Denied subscribe and publish return RefusedError and keep the connection alive.
	var refused *RefusedError
	if err := ac.Subscribe(ctx(t), "config/#"); !errors.As(err, &refused) {
		t.Fatalf("denied subscribe err = %v, want RefusedError", err)
	}
	if err := ac.Publish(ctx(t), "notification/engine", []byte(`{}`), true); !errors.As(err, &refused) {
		t.Fatalf("denied publish err = %v, want RefusedError", err)
	}

	// Connection is still alive: granted publish and subscribe still work.
	if err := ac.Publish(ctx(t), "notification/addon.sample", []byte(`{"ok":true}`), true); err != nil {
		t.Fatalf("publish granted topic after refusal: %v", err)
	}
	if err := rc.Publish(ctx(t), "apply/1", []byte(`{"id":1}`), false); err != nil {
		t.Fatalf("realize publish: %v", err)
	}
	got := receive(t, ac.Messages())
	if got.Topic != "apply/1" || string(got.Payload) != `{"id":1}` {
		t.Fatalf("unexpected delivery: %+v", got)
	}
}

func TestSetClientAndRemoveClientDisconnectLiveConnections(t *testing.T) {
	b, _, addonSock := openTokenBus(t)

	if err := b.SetClient("addon.sample", "tok-1", Grants{Subscribe: []string{"apply/+"}}); err != nil {
		t.Fatal(err)
	}
	ac, err := Dial(ctx(t), addonSock, "addon.sample", "tok-1")
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Close()
	if err := ac.Subscribe(ctx(t), "apply/+"); err != nil {
		t.Fatal(err)
	}

	// Rotating token via SetClient immediately closes the existing connection.
	if err := b.SetClient("addon.sample", "tok-2", Grants{Subscribe: []string{"config/head"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ac.Messages():
		if ok {
			t.Fatal("expected closed Messages channel after SetClient rotation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("live connection was not closed by SetClient")
	}

	// Old token is refused; new token succeeds with updated grants.
	if c, err := Dial(ctx(t), addonSock, "addon.sample", "tok-1"); err == nil {
		c.Close()
		t.Fatal("old token still accepted after rotation")
	}
	ac2, err := Dial(ctx(t), addonSock, "addon.sample", "tok-2")
	if err != nil {
		t.Fatalf("new token dial: %v", err)
	}
	defer ac2.Close()
	var refused *RefusedError
	if err := ac2.Subscribe(ctx(t), "apply/+"); !errors.As(err, &refused) {
		t.Fatalf("revoked grant subscribe err = %v, want RefusedError", err)
	}
	if err := ac2.Subscribe(ctx(t), "config/head"); err != nil {
		t.Fatalf("new grant subscribe: %v", err)
	}

	// RemoveClient immediately closes ac2 and refuses reconnect.
	b.RemoveClient("addon.sample")
	select {
	case _, ok := <-ac2.Messages():
		if ok {
			t.Fatal("expected closed Messages channel after RemoveClient")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("live connection was not closed by RemoveClient")
	}
	if c, err := Dial(ctx(t), addonSock, "addon.sample", "tok-2"); err == nil {
		c.Close()
		t.Fatal("removed client still accepted")
	}
}

func TestSetClientValidation(t *testing.T) {
	b, _, _ := openTokenBus(t)

	bad := []struct {
		name   string
		user   string
		token  string
		grants Grants
	}{
		{"non-addon user", "realize", "tok", Grants{}},
		{"bare addon", "addon.", "tok", Grants{}},
		{"reserved bus-socket slug", "addon.bus-socket", "tok", Grants{}},
		{"uppercase slug", "addon.Bad", "tok", Grants{}},
		{"slash in user", "addon.a/b", "tok", Grants{}},
		{"empty token", "addon.ok", "", Grants{}},
		{"nul token", "addon.ok", "a\x00b", Grants{}},
		{"wildcard root pub", "addon.ok", "tok", Grants{Publish: []string{"#"}}},
		{"wildcard root sub", "addon.ok", "tok", Grants{Subscribe: []string{"+/foo"}}},
		{"sub notification wildcard", "addon.ok", "tok", Grants{Subscribe: []string{"notification/#"}}},
		{"sub own notification", "addon.ok", "tok", Grants{Subscribe: []string{"notification/addon.ok"}}},
		{"sub delivery", "addon.ok", "tok", Grants{Subscribe: []string{"delivery/#"}}},
		{"sub alias-fetch", "addon.ok", "tok", Grants{Subscribe: []string{"alias-fetch/+"}}},
		{"pub core notification", "addon.ok", "tok", Grants{Publish: []string{"notification/engine"}}},
		{"pub other addon notification", "addon.ok", "tok", Grants{Publish: []string{"notification/addon.other"}}},
		{"pub notification wildcard", "addon.ok", "tok", Grants{Publish: []string{"notification/+"}}},
		{"pub config", "addon.ok", "tok", Grants{Publish: []string{"config/head"}}},
		{"pub apply", "addon.ok", "tok", Grants{Publish: []string{"apply/1"}}},
		{"pub notifications changed", "addon.ok", "tok", Grants{Publish: []string{"notifications/changed"}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.SetClient(tc.user, tc.token, tc.grants); err == nil {
				t.Fatalf("SetClient(%q) unexpectedly succeeded", tc.user)
			}
		})
	}
}

func TestSetClientConcurrentAccess(t *testing.T) {
	b, _, addonSock := openTokenBus(t)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 30 {
			tok := "tok-even"
			if i%2 == 1 {
				tok = "tok-odd"
			}
			_ = b.SetClient("addon.race", tok, Grants{Subscribe: []string{"apply/+"}})
			if i%5 == 0 {
				b.RemoveClient("addon.race")
			}
		}
	})
	wg.Go(func() {
		for range 30 {
			if c, err := Dial(ctx(t), addonSock, "addon.race", "tok-even"); err == nil {
				_ = c.Close()
			}
		}
	})
	wg.Wait()
}

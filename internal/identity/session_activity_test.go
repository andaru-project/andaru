package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
)

func TestOnlyActivityKeepsASessionAlive(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	admin, _, err := store.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	login, err := store.AuthenticatePassword(ctx, admin.Login, "unique-admin-password-41", "192.0.2.10", 7)
	if err != nil {
		t.Fatal(err)
	}
	selector, secret := splitCredential(t, login.SessionCookie)
	selectorBytes, _ := base64.RawURLEncoding.DecodeString(selector)
	idleFor := func(seconds int) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at=unixepoch()-? WHERE session_selector=?", seconds, selectorBytes); err != nil {
			t.Fatal(err)
		}
	}
	idleFor(1700)
	if _, err = store.AuthenticateSession(ctx, selector, secret, 7, false); err != nil {
		t.Fatalf("poll within the idle time: %v", err)
	}
	// 101 seconds pass, so the session is idle for good unless the poll reset its time.
	if _, err = store.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at=last_seen_at-101 WHERE session_selector=?", selectorBytes); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateSession(ctx, selector, secret, 7, false); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("polls kept an idle session alive: %v", err)
	}

	login, err = store.AuthenticatePassword(ctx, admin.Login, "unique-admin-password-41", "192.0.2.10", 7)
	if err != nil {
		t.Fatal(err)
	}
	selector, secret = splitCredential(t, login.SessionCookie)
	selectorBytes, _ = base64.RawURLEncoding.DecodeString(selector)
	idleFor(1700)
	if _, err = store.AuthenticateSession(ctx, selector, secret, 7, true); err != nil {
		t.Fatal(err)
	}
	var recent bool
	if err = store.db.QueryRowContext(ctx, "SELECT last_seen_at>unixepoch()-60 FROM sessions WHERE session_selector=?", selectorBytes).Scan(&recent); err != nil || !recent {
		t.Fatalf("activity did not reset the idle time: recent=%v err=%v", recent, err)
	}
}

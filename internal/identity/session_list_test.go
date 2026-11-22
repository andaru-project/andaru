package identity

import (
	"context"
	"errors"
	"testing"
)

func TestPasswordIntentPrecedesSessionAndOwnSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	admin, _, err := store.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	intentErr := errors.New("audit unavailable")
	if _, err = store.AuthenticatePasswordWithIntent(ctx, admin.Login, "unique-admin-password-41", "192.0.2.10", 7, func(Account) error {
		return intentErr
	}); !errors.Is(err, intentErr) {
		t.Fatalf("login intent failure = %v", err)
	}
	var count int
	if err = store.db.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("session committed before intent: count=%d err=%v", count, err)
	}
	login, err := store.AuthenticatePasswordWithIntent(ctx, admin.Login, "unique-admin-password-41", "192.0.2.10", 7, func(account Account) error {
		if account.ID != admin.ID {
			t.Fatalf("intent account = %q, want %q", account.ID, admin.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	selector, secret := splitCredential(t, login.SessionCookie)
	sessions, err := store.ListSessions(ctx, admin.ID, 7)
	if err != nil || len(sessions) != 1 || sessions[0].Selector != selector || sessions[0].CreatedAt.IsZero() || sessions[0].LastSeenAt.IsZero() || sessions[0].ExpiresAt.IsZero() {
		t.Fatalf("sessions = %+v, err=%v", sessions, err)
	}
	if err = store.RevokeSession(ctx, admin.ID, selector); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateSession(ctx, selector, secret, 7, true); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("revoked session authentication = %v", err)
	}
	if err = store.RevokeSession(ctx, admin.ID, selector); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session revocation = %v", err)
	}
}

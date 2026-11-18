package identity

import (
	"context"
	"errors"
	"testing"
)

func TestResetAdminDefault(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	verifier, err := HashResetPassword(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	admin, revision, err := s.BootstrapResetAdmin(ctx, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Login != "admin" || admin.Kind != Human || !admin.Enabled || revision != 1 || len(admin.RoleIDs) != 1 {
		t.Fatalf("admin=%+v revision=%d", admin, revision)
	}
	if ok, err := s.VerifyPassword(ctx, admin.ID, []byte("admin")); err != nil || !ok {
		t.Fatalf("default password: ok=%v err=%v", ok, err)
	}
}

func TestResetAdminPolicy(t *testing.T) {
	ctx := context.Background()
	if _, err := HashResetPassword(ctx, []byte("short")); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := HashResetPassword(ctx, []byte("admin")); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("typed admin password: %v", err)
	}
	s, _ := openTestStore(t)
	verifier, err := HashResetPassword(ctx, []byte("unique-reset-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := s.BootstrapResetAdmin(ctx, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyPassword(ctx, admin.ID, []byte("unique-reset-password-41")); err != nil || !ok {
		t.Fatalf("entered password: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.VerifyPassword(ctx, admin.ID, []byte("admin")); ok {
		t.Fatal("the default must not authenticate when a password was entered")
	}
}

func TestAdminStillRefusedElsewhere(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	if _, _, err := s.BootstrapAdmin(ctx, "admin", "", []byte("admin")); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("bootstrap: %v", err)
	}
	_, revision, err := s.BootstrapAdmin(ctx, "rootadmin", "", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	named, revision, err := s.CreateAccount(ctx, CreateAccountInput{Login: "admin", Kind: Human, Enabled: true}, revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetPassword(ctx, named.ID, revision, []byte("admin")); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("set password: %v", err)
	}
}

func TestResetAdminNeedsEmptyStore(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	if _, _, err := s.BootstrapAdmin(ctx, "rootadmin", "", []byte("unique-admin-password-41")); err != nil {
		t.Fatal(err)
	}
	verifier, err := HashResetPassword(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.BootstrapResetAdmin(ctx, verifier); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-empty store: %v", err)
	}
}

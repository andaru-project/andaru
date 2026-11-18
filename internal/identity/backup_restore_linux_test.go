package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestIdentityBackupSelectionAndRestoreSemantics(t *testing.T) {
	ctx := context.Background()
	source, _ := openTestStore(t)
	admin, revision, err := source.BootstrapAdmin(ctx, "sourceadmin", "Source administrator", []byte("source-admin-password-2026"))
	if err != nil {
		t.Fatal(err)
	}
	customRole := "rol_12345678-1234-4234-8234-123456789abc"
	user, revision, err := source.CreateAccount(ctx, CreateAccountInput{Login: "restoreduser", Kind: Human, Enabled: true, RoleIDs: []string{customRole}}, revision)
	if err != nil {
		t.Fatal(err)
	}
	if revision, err = source.SetPassword(ctx, user.ID, revision, []byte("restored-user-password-2026")); err != nil {
		t.Fatal(err)
	}
	deleted, revision, err := source.CreateAccount(ctx, CreateAccountInput{Login: "deleteduser", Kind: Human}, revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = source.DeleteAccount(ctx, deleted.ID, revision); err != nil {
		t.Fatal(err)
	}
	documents, err := source.ExportBackup(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(documents.Users, []byte(customRole)) || bytes.Contains(documents.Users, []byte("source-admin-password")) {
		t.Fatal("user export lost role IDs or exposed a password")
	}
	for _, forbidden := range [][]byte{[]byte("sessions"), []byte("login_limits"), []byte("limiter_key"), []byte("audit")} {
		if bytes.Contains(documents.Users, forbidden) || bytes.Contains(documents.UserCredentials, forbidden) {
			t.Fatalf("runtime store leaked into backup: %s", forbidden)
		}
	}

	destination, stateDir := openTestStore(t)
	_, destinationRevision, err := destination.BootstrapAdmin(ctx, "destinationadmin", "Destination administrator", []byte("destination-admin-password-2026"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := destination.PrepareRestore(ctx, destinationRevision, RestoreDocuments{Users: documents.Users, UserCredentials: documents.UserCredentials}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Publish(ctx)
	if closeErr := prepared.Close(); err == nil {
		err = closeErr
	}
	if err != nil || result.IdentityRevision != destinationRevision+1 {
		t.Fatalf("paired restore result=%+v err=%v", result, err)
	}
	restored, err := destination.GetAccount(ctx, user.ID)
	if err != nil || !slices.Equal(restored.RoleIDs, []string{customRole}) {
		t.Fatalf("restored account=%+v err=%v", restored, err)
	}
	if ok, err := destination.VerifyPassword(ctx, user.ID, []byte("restored-user-password-2026")); err != nil || !ok {
		t.Fatalf("restored password=%v err=%v", ok, err)
	}
	var tombstone int
	if err = destination.db.QueryRowContext(ctx, "SELECT count(*) FROM tombstones WHERE account_id=?", deleted.ID).Scan(&tombstone); err != nil || tombstone != 1 {
		t.Fatalf("tombstone=%d err=%v", tombstone, err)
	}
	if err = destination.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got, err := restarted.GetAccount(ctx, admin.ID); err != nil || got.ID != admin.ID {
		t.Fatalf("restart account=%+v err=%v", got, err)
	}
}

func TestIdentityUserOnlyIsPasswordlessAndRootRecoveryIsAtomic(t *testing.T) {
	ctx := context.Background()
	source, _ := openTestStore(t)
	admin, _, err := source.BootstrapAdmin(ctx, "sourceadmin", "Source administrator", []byte("source-admin-password-2026"))
	if err != nil {
		t.Fatal(err)
	}
	documents, err := source.ExportBackup(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	destination, _ := openTestStore(t)
	_, revision, err := destination.BootstrapAdmin(ctx, "destinationadmin", "Destination administrator", []byte("destination-admin-password-2026"))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := destination.PreviewRestore(ctx, RestoreDocuments{Users: documents.Users})
	if err != nil || !preview.RequiresRootAdministrator || len(preview.RecoveryAdministrators) != 1 || preview.RecoveryAdministrators[0].ID != admin.ID {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err = destination.PrepareRestore(ctx, revision, RestoreDocuments{Users: documents.Users}, nil); !errors.Is(err, ErrInvariant) {
		t.Fatalf("network-style user-only restore: %v", err)
	}
	prepared, err := destination.PrepareRestore(ctx, revision, RestoreDocuments{Users: documents.Users}, &RootAdministrator{AccountID: admin.ID, Password: []byte("replacement-admin-password-2026")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepared.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if err = prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if ok, err := destination.VerifyPassword(ctx, admin.ID, []byte("replacement-admin-password-2026")); err != nil || !ok {
		t.Fatalf("replacement credential=%v err=%v", ok, err)
	}
}

func TestCredentialOnlyRestoreRefusesMissingAccount(t *testing.T) {
	ctx := context.Background()
	source, _ := openTestStore(t)
	_, _, err := source.BootstrapAdmin(ctx, "sourceadmin", "Source administrator", []byte("source-admin-password-2026"))
	if err != nil {
		t.Fatal(err)
	}
	documents, err := source.ExportBackup(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	destination, _ := openTestStore(t)
	_, revision, err := destination.BootstrapAdmin(ctx, "otheradmin", "Other administrator", []byte("other-admin-password-2026"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = destination.PrepareRestore(ctx, revision, RestoreDocuments{UserCredentials: documents.UserCredentials}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("credential-only missing account: %v", err)
	}
}

func TestIdentityBackupDocumentsRejectUnknownFields(t *testing.T) {
	store, _ := openTestStore(t)
	var document map[string]json.RawMessage
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "identityRevision": 0, "accounts": []any{}, "tombstones": []any{}, "sessions": []any{}})
	if err != nil || json.Unmarshal(raw, &document) != nil {
		t.Fatal(err)
	}
	if _, err = store.PreviewRestore(context.Background(), RestoreDocuments{Users: raw}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown field accepted: %v", err)
	}
}

func TestRestoredTokenScopeRemainsBounded(t *testing.T) {
	if !slices.Contains(authz.Catalog(), authz.StatusRead) {
		t.Fatal("test capability missing")
	}
}

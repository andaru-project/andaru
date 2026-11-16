package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestMutationOperationRecordSharesIdentityCommit(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	admin, revision, err := store.BootstrapAdmin(ctx, "operator", "Operator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := store.HashRequest(ctx, []byte("create account"))
	if err != nil || len(hash) != 64 {
		t.Fatalf("request hash = %q, %v", hash, err)
	}
	request := OperationIntent{
		OperationID: "account-create-1", RequestHash: hash,
		Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "unix:uid:0",
		Action: "account.created", Category: "protected",
	}
	account, _, err := store.CreateAccount(WithOperation(ctx, request), CreateAccountInput{
		Login: "worker", DisplayName: "Worker", Kind: Service, Enabled: true, RoleIDs: []string{authz.ReadOnlyRoleID},
	}, revision)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := store.LookupOperation(ctx, request.OperationID)
	if err != nil || !found {
		t.Fatalf("operation record found=%t, error=%v", found, err)
	}
	if record.Target != "" || record.OutcomeTarget != account.ID || record.Actor != request.Actor || record.RequestHash != hash || record.Result != "success" {
		t.Fatalf("operation record = %+v; account=%+v admin=%+v", record, account, admin)
	}
}

func TestOperationRequestHashIsStableAndKeyed(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	one, err := store.HashRequest(ctx, []byte("same body"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.HashRequest(ctx, []byte("same body"))
	if err != nil || one != two {
		t.Fatalf("same request hashes differ: %q %q %v", one, two, err)
	}
	other, err := store.HashRequest(ctx, []byte("different body"))
	if err != nil || one == other {
		t.Fatalf("different request hashes matched: %q %q %v", one, other, err)
	}
}
func TestIdentityOperationSchemaMigratesV1(t *testing.T) {
	store, stateDir := openTestStore(t)
	if _, err := store.db.Exec(`DROP TABLE operations;
		CREATE TABLE operations (
			operation_id TEXT PRIMARY KEY,
			idempotency_key TEXT NOT NULL UNIQUE,
			request_hash BLOB NOT NULL CHECK(length(request_hash)=32),
			revision INTEGER NOT NULL REFERENCES revisions(revision),
			created_at INTEGER NOT NULL
		) STRICT;
		PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var version int
	if err = migrated.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != LayoutVersion {
		t.Fatalf("migrated layout=%d err=%v", version, err)
	}
}
func TestIssueTokenAtRevisionEnforcesTargetSnapshot(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	admin, revision, err := store.BootstrapAdmin(ctx, "operator", "Operator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.IssueTokenAtRevision(ctx, revision-1, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, authz.Catalog(), authz.Catalog(), TokenIssue{
		AccountID: admin.ID, Name: "stale-snapshot", Scope: []authz.Capability{authz.StatusRead}, NoExpiry: true,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale token scope snapshot error = %v", err)
	}
	page, err := store.ListTokensPage(ctx, admin.ID, "", MaxTokenPageSize)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("stale snapshot inserted tokens: %+v %v", page, err)
	}
}

package audit

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

// A LayoutVersion 1 audit database migrates cleanly to LayoutVersion 2, preserving existing events and
// accepting the new add-on lifecycle actions.
func TestAuditV1MigratesToV2AndAcceptsAddonActions(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("audit store requires root")
	}
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "audit.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databaseURI(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	v1Events := `CREATE TABLE events (
	id INTEGER PRIMARY KEY AUTOINCREMENT CHECK(id>0),
	at INTEGER NOT NULL CHECK(at>=0),
	actor_kind TEXT NOT NULL CHECK(actor_kind IN ('system','account','token')),
	actor_id TEXT NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 128),
	source TEXT NOT NULL CHECK(length(source) BETWEEN 1 AND 128),
	action TEXT NOT NULL CHECK(action IN ('login.success','login.failure','login.backoff','session.logout','session.revoked','permission.denied','account.created','account.updated','account.disabled','account.deleted','role.changed','password.changed','token.created','token.revoked','secret.exported','configuration.saved','configuration.applied','configuration.confirmed','configuration.rollback','backup.restored','setup.completed','recovery.completed','audit.queried','audit.health','audit.retention_pruned')),
	target TEXT NOT NULL CHECK(length(target)<=128),
	result TEXT NOT NULL CHECK(result IN ('pending','success','failure','denied','committed','not_committed','unavailable','recovered','rolled_back')),
	operation_id TEXT NOT NULL CHECK(length(operation_id)<=128),
	category TEXT NOT NULL CHECK(category IN ('general','protected')),
	coalesced_count INTEGER NOT NULL CHECK(coalesced_count BETWEEN 1 AND 9223372036854775807)
) STRICT`
	if _, err := db.ExecContext(ctx, v1Events); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, obj := range layoutObjects[1:] {
		if _, err := db.ExecContext(ctx, obj.ddl); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, seedSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events(at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count) VALUES(?,'system','root','local','setup.completed','','success','','protected',1)`, time.Now().UnixMilli()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE retention SET total_inserted_protected=1 WHERE id=1`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO event_metadata(event_id,key,value) VALUES(1,'reasonCode',?)`, ReasonFactoryResetBackupWritten); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA application_id=1095648321"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA user_version=1"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	if err := os.Chmod(dbPath, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, suf := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suf); err == nil {
			_ = os.Chmod(dbPath+suf, 0o600)
		}
	}

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open v1 audit db: %v", err)
	}
	defer s.Close()

	if _, err := s.Append(ctx, Event{
		Actor:    authz.Actor{Kind: authz.ActorSystem, ID: "root"},
		Source:   "local",
		Action:   ActionAddonStaged,
		Target:   "tailscale",
		Result:   ResultSuccess,
		Category: CategoryGeneral,
	}); err != nil {
		t.Fatalf("Append addon.staged after migration: %v", err)
	}
	items, err := s.ListEvents(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 events (migrated + addon.staged), got %d", len(items))
	}
	report, err := s.Verify(ctx)
	if err != nil || !report.Valid {
		t.Fatalf("Verify after migration = %+v, %v", report, err)
	}
}

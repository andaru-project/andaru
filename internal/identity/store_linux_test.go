package identity

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRestartAndSchema(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	ctx := context.Background()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := s.Revision(ctx)
	if err != nil || rev != 0 {
		t.Fatalf("revision %d, %v", rev, err)
	}
	var keyBefore []byte
	if err = s.db.QueryRowContext(ctx, "SELECT secret FROM limiter_key WHERE id=1").Scan(&keyBefore); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var keyAfter []byte
	if err = s.db.QueryRowContext(ctx, "SELECT secret FROM limiter_key WHERE id=1").Scan(&keyAfter); err != nil || string(keyBefore) != string(keyAfter) {
		t.Fatalf("limiter HMAC key changed after restart: %v", err)
	}
	var app, version int
	if err = s.db.QueryRow("PRAGMA application_id").Scan(&app); err != nil || app != applicationID {
		t.Fatalf("app=%x err=%v", app, err)
	}
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != LayoutVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	for _, table := range []string{"accounts", "tombstones", "account_roles", "password_credentials", "api_tokens", "sessions", "login_limits", "source_limits", "limiter_key", "limiter_overload", "metadata", "revisions", "operations"} {
		var n int
		if err = s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s: n=%d err=%v", table, n, err)
		}
	}
}

func TestOpenMigratesV1AndDropsUnusableSessions(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "identity.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	v1 := `
CREATE TABLE accounts(id TEXT PRIMARY KEY) STRICT;
CREATE TABLE metadata(key TEXT PRIMARY KEY,value INTEGER NOT NULL) STRICT;
INSERT INTO metadata(key,value) VALUES('revision',0),('password_epoch:legacy',1);
CREATE TABLE api_tokens(selector BLOB PRIMARY KEY,account_id TEXT NOT NULL,secret_hash BLOB NOT NULL,name TEXT NOT NULL,scopes TEXT NOT NULL,created_at INTEGER NOT NULL,expires_at INTEGER,revoked_at INTEGER,last_used_at INTEGER,revoke_on_password_change INTEGER NOT NULL);
CREATE TABLE login_limits(bucket_hash BLOB PRIMARY KEY,failures INTEGER NOT NULL,window_started_at INTEGER NOT NULL,expires_at INTEGER NOT NULL);
CREATE TABLE sessions(session_hash BLOB PRIMARY KEY,csrf_hash BLOB NOT NULL,account_id TEXT NOT NULL REFERENCES accounts(id),auth_epoch INTEGER NOT NULL,created_at INTEGER NOT NULL,last_seen_at INTEGER NOT NULL,expires_at INTEGER NOT NULL);
CREATE INDEX sessions_account ON sessions(account_id);
INSERT INTO accounts(id) VALUES('legacy');
INSERT INTO sessions VALUES(zeroblob(32),zeroblob(32),'legacy',0,1,1,9999999999);
`
	if _, err = db.ExecContext(ctx, v1); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=1;", applicationID)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version, sessions int
	if err = s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != LayoutVersion {
		t.Fatalf("migrated layout version=%d err=%v", version, err)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("unusable v1 sessions survived migration: count=%d err=%v", sessions, err)
	}
	var accountEpoch int
	if err = s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='account_epoch:legacy'").Scan(&accountEpoch); err != nil || accountEpoch != 0 {
		t.Fatalf("migrated account epoch=%d err=%v", accountEpoch, err)
	}
}

func TestOpenRefusesMissingLimiterKey(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "DELETE FROM limiter_key WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(ctx, dir); err == nil {
		t.Fatal("missing limiter key reset persisted limiter state")
	}
}

func TestOpenRefusesWrongApplication(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	first, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	db, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec("PRAGMA application_id=123"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = Open(context.Background(), dir); err == nil {
		t.Fatal("accepted database for another application")
	}
}

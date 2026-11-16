package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRestartAndSchema(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := s.Revision(ctx)
	if err != nil || rev != 0 {
		t.Fatalf("revision=%d err=%v", rev, err)
	}
	var key []byte
	if err = s.db.QueryRow("SELECT secret FROM limiter_key WHERE id=1").Scan(&key); err != nil {
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
	var after []byte
	if err = s.db.QueryRow("SELECT secret FROM limiter_key WHERE id=1").Scan(&after); err != nil || string(key) != string(after) {
		t.Fatalf("limiter key changed: %v", err)
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
			t.Fatalf("table %s count=%d err=%v", table, n, err)
		}
	}
}

func TestOpenRejectsInvalidEstablishedDatabase(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	for _, tc := range []struct{ name, statement string }{
		{"zero-app", "PRAGMA application_id=0"},
		{"negative-version", "PRAGMA user_version=-1"},
		{"wrong-layout", "PRAGMA user_version=8"},
		{"missing-table", "DROP TABLE operations"},
		{"missing-key", "DELETE FROM limiter_key"},
		{"malformed-columns", "DROP TABLE login_limits; CREATE TABLE login_limits (bucket_hash TEXT PRIMARY KEY) STRICT"},
		{"missing-constraint", "DROP TABLE login_limits; CREATE TABLE login_limits (bucket_hash BLOB PRIMARY KEY, failures INTEGER NOT NULL, window_started_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, backoff_level INTEGER NOT NULL, backoff_until INTEGER NOT NULL) STRICT"},
		{"missing-index", "DROP INDEX api_tokens_account_active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			s, err := Open(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(tc.statement); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if _, err = Open(context.Background(), dir); err == nil {
				t.Fatal("accepted invalid established database")
			}
		})
	}
}

func TestOpenRejectsUnsafeDatabaseMembers(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			s, err := Open(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			p := filepath.Join(dir, "identity.db"+suffix)
			if err = os.WriteFile(p, []byte("unsafe"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err = Open(context.Background(), dir); err == nil {
				t.Fatal("accepted unsafe database sidecar")
			}
		})
	}
}

func TestOpenInitializationFailureIsRetryable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	injected := errors.New("injected initialization failure")
	if _, err := openStore(context.Background(), dir, func() error { return injected }, nil); !errors.Is(err, injected) {
		t.Fatalf("openStore error=%v, want injected initialization failure", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "identity.db")); !os.IsNotExist(err) {
		t.Fatalf("failed initialization published identity.db: %v", err)
	}
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var app, version int
	if err = s.db.QueryRow("PRAGMA application_id").Scan(&app); err != nil || app != applicationID {
		t.Fatalf("app=%x err=%v", app, err)
	}
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != LayoutVersion {
		t.Fatalf("layout=%d err=%v", version, err)
	}
}

func TestOpenCheckpointFailureIsRetryable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	injected := errors.New("injected WAL checkpoint failure")
	if _, err := openStore(context.Background(), dir, nil, func() error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("openStore error=%v, want injected checkpoint failure", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "identity.db")); !os.IsNotExist(err) {
		t.Fatalf("checkpoint failure published identity.db: %v", err)
	}
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var revision int64
	if err = s.db.QueryRow("SELECT value FROM metadata WHERE key='revision'").Scan(&revision); err != nil || revision != 0 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
}

func TestOpenRefusesWrongApplication(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(fmt.Sprintf("PRAGMA application_id=%d", applicationID+1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = Open(context.Background(), dir); err == nil {
		t.Fatal("accepted database for another application")
	}
}

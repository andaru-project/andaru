package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

const configApp = 0x414e4443
const secretApp = 0x414e4453
const configDDL = `CREATE TABLE documents(sha256 BLOB PRIMARY KEY CHECK(length(sha256)=32),body BLOB NOT NULL);
CREATE TABLE versions(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES versions(id),schema_version INTEGER NOT NULL,config_sha256 BLOB NOT NULL REFERENCES documents(sha256),secrets_sha256 BLOB NOT NULL CHECK(length(secrets_sha256)=32),created_at TEXT NOT NULL,actor TEXT NOT NULL,changed_object_count INTEGER NOT NULL);
CREATE TABLE pointers(name TEXT PRIMARY KEY CHECK(name='saved'),version_id INTEGER NOT NULL REFERENCES versions(id));
CREATE TABLE object_changes(collection TEXT NOT NULL CHECK(collection IN ('links','segments','zones')),object_id TEXT NOT NULL,version_id INTEGER NOT NULL REFERENCES versions(id),created INTEGER NOT NULL CHECK(created IN (0,1)),deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),CHECK(NOT(created AND deleted)),PRIMARY KEY(collection,object_id,version_id));
CREATE UNIQUE INDEX creation_identity ON object_changes(object_id) WHERE created=1;
` + policyDDL

// policyDDL holds the versions of the filter's objects, lists and singletons. It is added beside
// object_changes rather than widening that table's CHECK, so an existing store gains it with one
// CREATE and an older engine, which never reads it, still opens the store.
const policyDDL = `CREATE TABLE IF NOT EXISTS policy_changes(collection TEXT NOT NULL CHECK(collection IN ('addresses','services','policy.input','policy.forward','management','nat.outbound')),object_id TEXT NOT NULL,version_id INTEGER NOT NULL REFERENCES versions(id),created INTEGER NOT NULL CHECK(created IN (0,1)),deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),CHECK(NOT(created AND deleted)),PRIMARY KEY(collection,object_id,version_id));
CREATE UNIQUE INDEX IF NOT EXISTS policy_creation_identity ON policy_changes(object_id) WHERE created=1;`

// changeTable is where a collection's object versions are recorded.
func changeTable(collection string) string {
	if collection == "links" || collection == "segments" || collection == "zones" {
		return "object_changes"
	}
	return "policy_changes"
}

const secretDDL = `CREATE TABLE documents(sha256 BLOB PRIMARY KEY CHECK(length(sha256)=32),body BLOB NOT NULL);`

func databaseURI(path, mode string, secret bool) string {
	q := url.Values{"mode": {mode}}
	if mode != "ro" {
		q["_pragma"] = []string{"busy_timeout(2000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)"}
		if secret {
			q["_pragma"] = append(q["_pragma"], "secure_delete(1)")
		}
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
}
func openDatabase(ctx context.Context, path, mode string, secret bool) (*sql.DB, error) {
	db, e := sql.Open("sqlite", databaseURI(path, mode, secret))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if e = db.PingContext(ctx); e != nil {
		db.Close()
		return nil, e
	}
	if mode != "ro" {
		c, e := db.Conn(ctx)
		if e != nil {
			db.Close()
			return nil, e
		}
		e = verifyPragmas(ctx, c, secret)
		c.Close()
		if e != nil {
			db.Close()
			return nil, e
		}
	}
	return db, nil
}
func verifyPragmas(ctx context.Context, c *sql.Conn, secret bool) error {
	for _, p := range []struct{ name, want, stage string }{
		{"journal_mode", "wal", "pragma-journal-mode"},
		{"synchronous", "2", "pragma-synchronous"},
		{"foreign_keys", "1", "pragma-foreign-keys"},
		{"busy_timeout", "2000", "pragma-busy-timeout"},
	} {
		var value string
		if e := c.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&value); e != nil {
			return diagnostic(p.stage+"-query", e)
		}
		if value != p.want {
			return diagnostic(p.stage+"-mismatch", nil)
		}
	}
	if secret {
		var v int
		if e := c.QueryRowContext(ctx, "PRAGMA secure_delete").Scan(&v); e != nil {
			return diagnostic("pragma-secure-delete-query", e)
		}
		if v != 1 {
			return diagnostic("pragma-secure-delete-mismatch", nil)
		}
	}
	return nil
}
func probeDatabase(ctx context.Context, path string, app int) error {
	db, e := openDatabase(ctx, path, "ro", false)
	if e != nil {
		return e
	}
	defer db.Close()
	var actual, version int
	if e = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&actual); e != nil {
		return e
	}
	if e = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); e != nil {
		return e
	}
	if actual != app {
		return diagnostic("probe-application-id-mismatch", nil)
	}
	if version != 1 {
		return diagnostic("probe-layout-version-incompatible", nil)
	}
	return nil
}
func initializePair(ctx context.Context, stage string) (result error) {
	for _, name := range []string{"config.db", "secrets.db"} {
		f, e := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
	}
	cfg, e := openDatabase(ctx, filepath.Join(stage, "config.db"), "rwc", false)
	if e != nil {
		return e
	}
	defer func() { result = errors.Join(result, cfg.Close()) }()
	sec, e := openDatabase(ctx, filepath.Join(stage, "secrets.db"), "rwc", true)
	if e != nil {
		return e
	}
	defer func() { result = errors.Join(result, sec.Close()) }()
	if _, e = cfg.ExecContext(ctx, configDDL+fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=1;", configApp)); e != nil {
		return e
	}
	if _, e = sec.ExecContext(ctx, secretDDL+fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=1;", secretApp)); e != nil {
		return e
	}
	cb, e := canonical(emptyDeclaration())
	if e != nil {
		return e
	}
	sb, e := canonical(emptySecrets())
	if e != nil {
		return e
	}
	ch := sha256.Sum256(cb)
	sh := sha256.Sum256(sb)
	if _, e = sec.ExecContext(ctx, "INSERT INTO documents VALUES(?,?)", sh[:], sb); e != nil {
		return e
	}
	tx, e := cfg.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "INSERT INTO documents VALUES(?,?)", ch[:], cb); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO versions VALUES(0,NULL,1,?,?,?,?,0)", ch[:], sh[:], time.Now().UTC().Format(time.RFC3339Nano), "unix:uid:0"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO pointers VALUES('saved',0)"); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	if _, e = cfg.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
		return e
	}
	if _, e = sec.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
		return e
	}
	if e = cfg.Close(); e != nil {
		return e
	}
	if e = sec.Close(); e != nil {
		return e
	}
	for _, name := range []string{"config.db", "secrets.db"} {
		f, e := os.OpenFile(filepath.Join(stage, name), os.O_RDWR, 0)
		if e != nil {
			return e
		}
		e = errors.Join(f.Sync(), f.Close())
		if e != nil {
			return e
		}
	}
	return syncDirectory(stage)
}
func putDocument(ctx context.Context, c *sql.Conn, body []byte) error {
	hash := sha256.Sum256(body)
	if _, e := c.ExecContext(ctx, "INSERT OR IGNORE INTO documents(sha256,body) VALUES(?,?)", hash[:], body); e != nil {
		return e
	}
	var existing []byte
	if e := c.QueryRowContext(ctx, "SELECT body FROM documents WHERE sha256=?", hash[:]).Scan(&existing); e != nil {
		return e
	}
	if !bytes.Equal(body, existing) {
		return unavailable()
	}
	return nil
}
func publishVersion(ctx context.Context, c *sql.Conn, p preparedChange, actor string) error {
	next, e := strconvVersion(p.State.Version)
	if e != nil {
		return e
	}
	var parent int64
	if e = c.QueryRowContext(ctx, "SELECT version_id FROM pointers WHERE name='saved'").Scan(&parent); e != nil {
		return e
	}
	if next != parent+1 {
		return unavailable()
	}
	if e = putDocument(ctx, c, p.ConfigBody); e != nil {
		return e
	}
	ch := sha256.Sum256(p.ConfigBody)
	sh := sha256.Sum256(p.SecretBody)
	if _, e = c.ExecContext(ctx, "INSERT INTO versions(id,parent_id,schema_version,config_sha256,secrets_sha256,created_at,actor,changed_object_count) VALUES(?,?,?,?,?,?,?,?)", next, parent, p.State.Config.SchemaVersion, ch[:], sh[:], time.Now().UTC().Format(time.RFC3339Nano), actor, len(p.Changed)); e != nil {
		return e
	}
	for _, change := range p.Changed {
		if _, e = c.ExecContext(ctx, "INSERT INTO "+changeTable(change.Collection)+" VALUES(?,?,?,?,?)", change.Collection, change.ID, next, change.Created, change.Deleted); e != nil {
			return e
		}
	}
	_, e = c.ExecContext(ctx, "UPDATE pointers SET version_id=? WHERE name='saved'", next)
	return e
}
func strconvVersion(v Version) (int64, error) {
	if _, e := ParseVersion(string(v)); e != nil {
		return 0, e
	}
	return strconv.ParseInt(string(v), 10, 64)
}

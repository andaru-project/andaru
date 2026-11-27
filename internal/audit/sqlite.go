package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

type layoutObject struct {
	kind string
	name string
	ddl  string
}

var layoutObjects = []layoutObject{
	{kind: "table", name: "events", ddl: `CREATE TABLE events (
	id INTEGER PRIMARY KEY AUTOINCREMENT CHECK(id>0),
	at INTEGER NOT NULL CHECK(at>=0),
	actor_kind TEXT NOT NULL CHECK(actor_kind IN ('system','account','token')),
	actor_id TEXT NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 128),
	source TEXT NOT NULL CHECK(length(source) BETWEEN 1 AND 128),
	action TEXT NOT NULL CHECK(action IN ('login.success','login.failure','login.backoff','session.logout','session.revoked','permission.denied','account.created','account.updated','account.disabled','account.deleted','role.changed','password.changed','token.created','token.revoked','secret.exported','configuration.saved','configuration.applied','configuration.confirmed','configuration.rollback','backup.restored','setup.completed','recovery.completed','audit.queried','audit.health','audit.retention_pruned','addon.staged','addon.unstaged','addon.started','addon.stopped','addon.restarted')),
	target TEXT NOT NULL CHECK(length(target)<=128),
	result TEXT NOT NULL CHECK(result IN ('pending','success','failure','denied','committed','not_committed','unavailable','recovered','rolled_back')),
	operation_id TEXT NOT NULL CHECK(length(operation_id)<=128),
	category TEXT NOT NULL CHECK(category IN ('general','protected')),
	coalesced_count INTEGER NOT NULL CHECK(coalesced_count BETWEEN 1 AND 9223372036854775807)
) STRICT`},
	{kind: "table", name: "event_metadata", ddl: `CREATE TABLE event_metadata (
	event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
	key TEXT NOT NULL CHECK(key IN ('reasonCode','changed','scopeCount','secretUnavailable','category','expiredGeneral','expiredProtected','countPrunedGeneral','watermark')),
	value TEXT NOT NULL CHECK(length(value)<=64),
	PRIMARY KEY(event_id,key)
) STRICT`},
	{kind: "table", name: "operations", ddl: `CREATE TABLE operations (
	operation_id TEXT PRIMARY KEY CHECK(length(operation_id) BETWEEN 1 AND 128),
	request_hash TEXT NOT NULL CHECK(length(request_hash)=64 AND request_hash NOT GLOB '*[^0-9a-f]*'),
	actor_kind TEXT NOT NULL CHECK(actor_kind IN ('system','account','token')),
	actor_id TEXT NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 128),
	source TEXT NOT NULL CHECK(length(source) BETWEEN 1 AND 128),
	action TEXT NOT NULL,
	target TEXT NOT NULL CHECK(length(target)<=128),
	category TEXT NOT NULL CHECK(category IN ('general','protected')),
	state TEXT NOT NULL CHECK(state IN ('pending','completed','not_committed')),
	intent_event_id INTEGER NOT NULL,
	outcome_event_id INTEGER,
	result TEXT NOT NULL CHECK(result IN ('','success','failure','denied','committed','not_committed','unavailable','recovered','rolled_back')),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
) STRICT`},
	{kind: "table", name: "operation_metadata", ddl: `CREATE TABLE operation_metadata (
	operation_id TEXT NOT NULL REFERENCES operations(operation_id) ON DELETE CASCADE,
	key TEXT NOT NULL CHECK(key IN ('reasonCode','changed','scopeCount','secretUnavailable','category','expiredGeneral','expiredProtected','countPrunedGeneral','watermark')),
	value TEXT NOT NULL CHECK(length(value)<=64),
	PRIMARY KEY(operation_id,key)
) STRICT`},
	{kind: "table", name: "retention", ddl: `CREATE TABLE retention (
	id INTEGER PRIMARY KEY CHECK(id=1),
	watermark_id INTEGER NOT NULL CHECK(watermark_id>=0),
	total_pruned_general INTEGER NOT NULL CHECK(total_pruned_general>=0),
	total_pruned_protected INTEGER NOT NULL CHECK(total_pruned_protected>=0),
	last_at INTEGER NOT NULL CHECK(last_at>=0),
	last_expired_general INTEGER NOT NULL CHECK(last_expired_general>=0),
	last_expired_protected INTEGER NOT NULL CHECK(last_expired_protected>=0),
	last_count_pruned_general INTEGER NOT NULL CHECK(last_count_pruned_general>=0),
	total_inserted_general INTEGER NOT NULL DEFAULT 0 CHECK(total_inserted_general>=0),
	total_inserted_protected INTEGER NOT NULL DEFAULT 0 CHECK(total_inserted_protected>=0)
) STRICT`},
	{kind: "table", name: "health", ddl: `CREATE TABLE health (
	id INTEGER PRIMARY KEY CHECK(id=1),
	general_saturated INTEGER NOT NULL CHECK(general_saturated IN (0,1)),
	protected_saturated INTEGER NOT NULL CHECK(protected_saturated IN (0,1)),
	storage_failure INTEGER NOT NULL CHECK(storage_failure IN (0,1)),
	integrity_failure INTEGER NOT NULL CHECK(integrity_failure IN (0,1))
) STRICT`},
	{kind: "index", name: "events_category_at", ddl: `CREATE INDEX events_category_at ON events(category,at,id)`},
	{kind: "index", name: "events_coalesce", ddl: `CREATE INDEX events_coalesce ON events(action,source,result,at)`},
	{kind: "index", name: "operations_state_id", ddl: `CREATE INDEX operations_state_id ON operations(state,operation_id)`},
}

const seedSchema = `
INSERT INTO retention(id,watermark_id,total_pruned_general,total_pruned_protected,last_at,last_expired_general,last_expired_protected,last_count_pruned_general)
VALUES(1,0,0,0,0,0,0,0);
INSERT INTO health(id,general_saturated,protected_saturated,storage_failure,integrity_failure)
VALUES(1,0,0,0,0);
`

func schemaSQL() string {
	statements := make([]string, 0, len(layoutObjects)+1)
	for _, object := range layoutObjects {
		statements = append(statements, object.ddl)
	}
	return strings.Join(statements, ";\n") + ";\n" + seedSchema
}

func verifySchema(ctx context.Context, db *sql.DB) error {
	want := make(map[string]layoutObject, len(layoutObjects))
	for _, object := range layoutObjects {
		want[object.name] = object
	}
	rows, err := db.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master
		WHERE type IN ('table','index','view','trigger') AND name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var kind, name, ddl string
		if err = rows.Scan(&kind, &name, &ddl); err != nil {
			return err
		}
		object, ok := want[name]
		if !ok || object.kind != kind || normalizeDDL(object.ddl) != normalizeDDL(ddl) {
			return errors.New("audit database layout mismatch")
		}
		seen++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if seen != len(want) {
		return errors.New("audit database layout incomplete")
	}
	var retentionRows, healthRows int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM retention WHERE id=1").Scan(&retentionRows); err != nil {
		return err
	}
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM health WHERE id=1").Scan(&healthRows); err != nil {
		return err
	}
	if retentionRows != 1 || healthRows != 1 {
		return errors.New("audit database state rows missing")
	}
	return nil
}

func verifyPragmas(ctx context.Context, db *sql.DB) error {
	var journal string
	var synchronous, foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	if journal != "wal" || synchronous != 2 || foreignKeys != 1 {
		return fmt.Errorf("audit SQLite pragmas mismatch")
	}
	return nil
}

func databaseURI(path string, readOnly bool) string {
	mode := "rw"
	pragmas := []string{"busy_timeout(2000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)"}
	if readOnly {
		mode = "ro"
		pragmas = nil
	}
	query := url.Values{"mode": {mode}}
	for _, pragma := range pragmas {
		query.Add("_pragma", pragma)
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func normalizeDDL(ddl string) string {
	return strings.Join(strings.Fields(ddl), " ")
}

func layoutDDL(name string) string {
	for _, object := range layoutObjects {
		if object.name == name {
			return object.ddl
		}
	}
	panic("audit: missing layout DDL for " + name)
}

// migrateAuditV1 upgrades a v1 audit database to LayoutVersion 2 by expanding the events.action CHECK constraint
// to include add-on lifecycle actions while preserving all existing events and metadata rows.
func migrateAuditV1(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON") }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldSeq int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='events'),0)`).Scan(&oldSeq); err != nil {
		return fmt.Errorf("migrate audit v1: read sequence: %w", err)
	}

	stmts := []string{
		`CREATE TEMP TABLE events_bak AS SELECT id,at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count FROM events`,
		`DROP TABLE events`,
		layoutDDL("events"),
		`INSERT INTO events(id,at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count)
		 SELECT id,at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count FROM events_bak`,
		`DROP TABLE events_bak`,
		layoutDDL("events_category_at"),
		layoutDDL("events_coalesce"),
		fmt.Sprintf("PRAGMA user_version=%d", LayoutVersion),
	}
	for _, stmt := range stmts {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate audit v1: %w", err)
		}
	}
	if oldSeq > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name='events'`); err != nil {
			return fmt.Errorf("migrate audit v1: clear sequence: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name,seq) VALUES('events',?)`, oldSeq); err != nil {
			return fmt.Errorf("migrate audit v1: restore sequence: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	hasFKViolation := rows.Next()
	fkErr := rows.Err()
	_ = rows.Close()
	if fkErr != nil {
		return fkErr
	}
	if hasFKViolation {
		return errors.New("audit v1 migration violated foreign key integrity")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	return err
}

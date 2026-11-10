package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

func (s *Store) loadState(ctx context.Context, conn *sql.Conn, id Version, requested []objectKey) (savedState, VersionInfo, error) {
	var state savedState
	var info VersionInfo
	var number int64
	var parent sql.NullInt64
	var schemaVersion int
	var cb, ch, sh []byte
	query := `SELECT v.id,v.parent_id,v.schema_version,v.created_at,v.actor,v.changed_object_count,d.body,v.config_sha256,v.secrets_sha256 FROM versions v JOIN documents d ON d.sha256=v.config_sha256 `
	var row *sql.Row
	if id == "" {
		row = conn.QueryRowContext(ctx, query+`JOIN pointers p ON p.version_id=v.id WHERE p.name='saved'`)
	} else {
		n, e := strconvVersion(id)
		if e != nil {
			return state, info, fault("INVALID_REQUEST", "", "Invalid version.")
		}
		row = conn.QueryRowContext(ctx, query+`WHERE v.id=?`, n)
	}
	if e := row.Scan(&number, &parent, &schemaVersion, &info.CreatedAt, &info.Actor, &info.ChangedObjectCount, &cb, &ch, &sh); e != nil {
		if errors.Is(e, sql.ErrNoRows) && id != "" {
			return state, info, fault("NOT_FOUND", "", "Saved version does not exist.")
		}
		return state, info, diagnostic("load-config-scan", e)
	}
	if number < 0 || schemaVersion != 1 || len(ch) != 32 || len(sh) != 32 || len(cb) > documentLimit {
		return state, info, diagnostic("load-config-metadata", nil)
	}
	hash := sha256.Sum256(cb)
	if !bytes.Equal(hash[:], ch) {
		return state, info, diagnostic("load-config-hash", nil)
	}
	d, e := decodeDeclaration(cb)
	if e != nil {
		return state, info, diagnostic("load-config-schema", e)
	}
	canonicalBody, e := canonical(d)
	if e != nil || !bytes.Equal(canonicalBody, cb) {
		return state, info, diagnostic("load-config-canonical", e)
	}
	state.Version = Version(strconv.FormatInt(number, 10))
	info.ID = state.Version
	if parent.Valid {
		v := Version(strconv.FormatInt(parent.Int64, 10))
		info.ParentID = &v
	}
	state.Config = d
	state.configBody = cb
	sec, e := s.secretDB.Conn(ctx)
	if e != nil {
		return state, info, diagnostic("load-secret-connect", e)
	}
	defer sec.Close()
	if e = verifyPragmas(ctx, sec, true); e != nil {
		return state, info, e
	}
	var sb []byte
	if e = sec.QueryRowContext(ctx, "SELECT body FROM documents WHERE sha256=?", sh).Scan(&sb); e != nil {
		return state, info, diagnostic("load-secret-lookup", e)
	}
	if len(sb) > documentLimit {
		return state, info, diagnostic("load-secret-lookup-size", nil)
	}
	hash = sha256.Sum256(sb)
	if !bytes.Equal(hash[:], sh) {
		return state, info, diagnostic("load-secret-hash", nil)
	}
	if _, e = decodeStrictLimit(sb, documentLimit); e != nil {
		return state, info, diagnostic("load-secret-strict", e)
	}
	if e = json.Unmarshal(sb, &state.Secrets); e != nil {
		return state, info, diagnostic("load-secret-strict", e)
	}
	if e = validateSecrets(state.Secrets, state.Version); e != nil {
		return state, info, diagnostic("load-secret-schema", e)
	}
	canonicalSecret, e := canonical(state.Secrets)
	if e != nil || !bytes.Equal(canonicalSecret, sb) {
		return state, info, diagnostic("load-secret-canonical", e)
	}
	state.secretBody = sb
	for _, l := range d.Links {
		if l.Password != nil {
			if _, ok := state.Secrets.Scopes["core"][l.Password.Secret]; !ok {
				return state, info, diagnostic("load-secret-missing-ref", nil)
			}
		}
	}
	active := objects(&d)
	wanted := map[objectKey]bool{}
	for k := range active {
		wanted[k] = true
	}
	for _, k := range requested {
		if validCollection(k.Collection) {
			wanted[k] = true
		}
	}
	state.Versions = make(map[objectKey]objectVersion, len(wanted))
	for _, col := range []string{"links", "segments", "zones"} {
		ids := []string{}
		for key := range wanted {
			if key.Collection == col {
				ids = append(ids, key.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		raw, _ := json.Marshal(ids)
		rows, e := conn.QueryContext(ctx, `SELECT c.object_id,c.version_id,c.deleted FROM object_changes c WHERE c.collection=? AND c.object_id IN (SELECT value FROM json_each(?)) AND c.version_id=(SELECT max(n.version_id) FROM object_changes n WHERE n.collection=c.collection AND n.object_id=c.object_id AND n.version_id<=?)`, col, string(raw), number)
		if e != nil {
			return state, info, diagnostic("load-object-metadata-query", e)
		}
		for rows.Next() {
			var oid string
			var version int64
			var deleted bool
			if e = rows.Scan(&oid, &version, &deleted); e != nil {
				rows.Close()
				return state, info, diagnostic("load-object-metadata-scan", e)
			}
			if version <= 0 || version > number {
				rows.Close()
				return state, info, diagnostic("load-object-metadata-range", nil)
			}
			state.Versions[objectKey{col, oid}] = objectVersion{Version: Version(strconv.FormatInt(version, 10)), Deleted: deleted}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return state, info, diagnostic("load-object-metadata-rows", e)
		}
	}
	for key := range active {
		meta, ok := state.Versions[key]
		if !ok || meta.Deleted {
			return state, info, diagnostic("load-object-metadata-missing", nil)
		}
	}
	return state, info, nil
}
func (s *Store) read(ctx context.Context, id Version, requested []objectKey, fn func(savedState, VersionInfo) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, e := s.acquire(ctx)
	if e != nil {
		return e
	}
	defer release()
	conn, e := s.configDB.Conn(ctx)
	if e != nil {
		return diagnostic("read-config-connect", e)
	}
	defer conn.Close()
	if e = verifyPragmas(ctx, conn, false); e != nil {
		return e
	}
	state, info, e := s.loadState(ctx, conn, id, requested)
	if e != nil {
		return e
	}
	return fn(state, info)
}
func (s *Store) Snapshot(ctx context.Context) (result SnapshotView, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error { var e error; result, e = publicSnapshot(state); return e })
	return
}
func (s *Store) List(ctx context.Context, collection string) (result CollectionView, err error) {
	if !validCollection(collection) {
		return result, fault("NOT_FOUND", "", "Unknown collection.")
	}
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		result = CollectionView{State: "saved", SavedVersion: state.Version, Items: []ObjectView{}}
		index := objects(&state.Config)
		keys := []objectKey{}
		for k := range index {
			if k.Collection == collection {
				keys = append(keys, k)
			}
		}
		slices.SortFunc(keys, compareKeys)
		for _, key := range keys {
			v, e := viewObject(index[key], state.Versions[key].Version, state.Secrets)
			if e != nil {
				return e
			}
			result.Items = append(result.Items, v)
		}
		return nil
	})
	return
}
func (s *Store) Get(ctx context.Context, collection, id string) (result ObjectView, version Version, err error) {
	key := objectKey{collection, id}
	if !validCollection(collection) {
		return result, "", fault("NOT_FOUND", "", "Unknown collection.")
	}
	err = s.read(ctx, "", []objectKey{key}, func(state savedState, _ VersionInfo) error {
		v, ok := objects(&state.Config)[key]
		if !ok {
			return fault("NOT_FOUND", "", "Object does not exist.")
		}
		var e error
		result, e = viewObject(v, state.Versions[key].Version, state.Secrets)
		version = state.Version
		return e
	})
	return
}
func (s *Store) History(ctx context.Context, before *Version, limit int) (result HistoryView, err error) {
	if limit < 1 || limit > 100 {
		return result, fault("INVALID_REQUEST", "", "History limit must be 1 to 100.")
	}
	upper := int64(^uint64(0) >> 1)
	if before != nil {
		var e error
		upper, e = strconvVersion(*before)
		if e != nil {
			return result, fault("INVALID_REQUEST", "", "Invalid history cursor.")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, e := s.acquire(ctx)
	if e != nil {
		return result, e
	}
	defer release()
	conn, e := s.configDB.Conn(ctx)
	if e != nil {
		return result, diagnostic("history-config-connect", e)
	}
	defer conn.Close()
	if e = verifyPragmas(ctx, conn, false); e != nil {
		return result, e
	}
	rows, e := conn.QueryContext(ctx, "SELECT id,parent_id,created_at,actor,changed_object_count FROM versions WHERE id<? ORDER BY id DESC LIMIT ?", upper, limit+1)
	if e != nil {
		return result, diagnostic("history-query", e)
	}
	defer rows.Close()
	result = HistoryView{State: "saved", Items: []VersionInfo{}}
	for rows.Next() {
		var number int64
		var parent sql.NullInt64
		var info VersionInfo
		if e = rows.Scan(&number, &parent, &info.CreatedAt, &info.Actor, &info.ChangedObjectCount); e != nil {
			return result, diagnostic("history-scan", e)
		}
		info.ID = Version(fmt.Sprint(number))
		if parent.Valid {
			v := Version(fmt.Sprint(parent.Int64))
			info.ParentID = &v
		}
		if len(result.Items) == limit {
			v := result.Items[len(result.Items)-1].ID
			result.NextBefore = &v
			break
		}
		result.Items = append(result.Items, info)
	}
	if e = rows.Err(); e != nil {
		return result, diagnostic("history-rows", e)
	}
	return result, nil
}
func (s *Store) Version(ctx context.Context, id Version) (result VersionView, err error) {
	if _, e := ParseVersion(string(id)); e != nil {
		return result, fault("INVALID_REQUEST", "", "Invalid version.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, e := s.acquire(ctx)
	if e != nil {
		return result, e
	}
	defer release()
	conn, e := s.configDB.Conn(ctx)
	if e != nil {
		return result, diagnostic("version-config-connect", e)
	}
	defer conn.Close()
	state, info, e := s.loadState(ctx, conn, id, nil)
	if e != nil {
		return result, e
	}
	snapshot, e := publicSnapshot(state)
	if e != nil {
		return result, e
	}
	result = VersionView{State: "saved", VersionInfo: info, Value: snapshot.Value, Changes: []objectChange{}}
	number, _ := strconvVersion(id)
	rows, e := conn.QueryContext(ctx, "SELECT collection,object_id,created,deleted FROM object_changes WHERE version_id=? ORDER BY collection,object_id", number)
	if e != nil {
		return result, diagnostic("version-changes-query", e)
	}
	defer rows.Close()
	for rows.Next() {
		var change objectChange
		if e = rows.Scan(&change.Collection, &change.ID, &change.Created, &change.Deleted); e != nil {
			return result, diagnostic("version-changes-scan", e)
		}
		result.Changes = append(result.Changes, change)
	}
	if e = rows.Err(); e != nil {
		return result, diagnostic("version-changes-rows", e)
	}
	return result, nil
}

// RealizationInput carries secret values for rendering. It has no JSON form
// and must never be serialised or logged.
type RealizationInput struct {
	SavedVersion Version
	Value        json.RawMessage
	Secrets      map[string]string
}

// RealizationInput returns one saved version with the secrets its links
// reference. A nil version means the saved head.
func (s *Store) RealizationInput(ctx context.Context, version *Version) (result RealizationInput, err error) {
	id := Version("")
	if version != nil {
		id = *version
	}
	err = s.read(ctx, id, nil, func(state savedState, _ VersionInfo) error {
		view, e := publicSnapshot(state)
		if e != nil {
			return e
		}
		result = RealizationInput{SavedVersion: state.Version, Value: view.Value, Secrets: map[string]string{}}
		for _, l := range state.Config.Links {
			if l.Password != nil {
				result.Secrets[l.Password.Secret] = state.Secrets.Scopes["core"][l.Password.Secret].Value
			}
		}
		return nil
	})
	return
}

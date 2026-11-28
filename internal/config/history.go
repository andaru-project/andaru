package config

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

// Newer reports whether a saved version exists and is of a schema newer than this Andaru knows: one a downgrade set
// aside (settings design, "Downgrades").
func (s *Store) Newer(ctx context.Context, version Version) (bool, error) {
	n, err := strconvVersion(version)
	if err != nil {
		return false, fault("INVALID_REQUEST", "", "Invalid version.")
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	var schema int
	err = s.configDB.QueryRowContext(ctx, "SELECT schema_version FROM versions WHERE id=?", n).Scan(&schema)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, diagnostic("newer-scan", err)
	}
	return schema > schemaVersion, nil
}

// Roles returns the roles of a saved version. A version of a schema newer than this Andaru knows, which the live
// generation still names after a rollback, is read for its roles alone; the caller trusts them only when they match
// the digest that generation recorded, so only roles that were applied are used.
func (s *Store) Roles(ctx context.Context, version Version) ([]authz.Role, error) {
	n, err := strconvVersion(version)
	if err != nil {
		return nil, fault("INVALID_REQUEST", "", "Invalid version.")
	}
	var body, hash []byte
	var schema int
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	err = s.configDB.QueryRowContext(ctx, "SELECT d.body,v.config_sha256,v.schema_version FROM versions v JOIN documents d ON d.sha256=v.config_sha256 WHERE v.id=?", n).Scan(&body, &hash, &schema)
	release()
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fault("NOT_FOUND", "", "Saved version does not exist.")
	}
	if err != nil {
		return nil, diagnostic("roles-scan", err)
	}
	var doc struct {
		Roles []authz.Role `json:"roles"`
	}
	if schema <= schemaVersion {
		saved, err := s.Version(ctx, version)
		if err != nil {
			return nil, err
		}
		body = saved.Value
	} else if sum := sha256.Sum256(body); !bytes.Equal(sum[:], hash) || len(body) > documentLimit {
		return nil, diagnostic("roles-hash", nil)
	}
	if err = json.Unmarshal(body, &doc); err != nil {
		return nil, diagnostic("roles-decode", err)
	}
	return doc.Roles, nil
}

func (s *Store) loadState(ctx context.Context, conn *sql.Conn, id Version, requested []objectKey) (savedState, VersionInfo, error) {
	var state savedState
	var info VersionInfo
	var number int64
	var parent sql.NullInt64
	var schema int
	var cb, ch, sh []byte
	query := `SELECT v.id,v.parent_id,v.schema_version,v.created_at,v.actor,v.changed_object_count,d.body,v.config_sha256,v.secrets_sha256,coalesce(w.andaru_version,'') FROM versions v JOIN documents d ON d.sha256=v.config_sha256 LEFT JOIN version_writers w ON w.version_id=v.id `
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
	if e := row.Scan(&number, &parent, &schema, &info.CreatedAt, &info.Actor, &info.ChangedObjectCount, &cb, &ch, &sh, &info.WrittenBy); e != nil {
		if errors.Is(e, sql.ErrNoRows) && id != "" {
			return state, info, fault("NOT_FOUND", "", "Saved version does not exist.")
		}
		return state, info, diagnostic("load-config-scan", e)
	}
	if id != "" && schema > schemaVersion {
		return state, info, fault("NOT_FOUND", "", "This version was saved by a newer Andaru, whose settings this Andaru cannot read.")
	}
	if number < 0 || schema < 1 || schema > schemaVersion || len(ch) != 32 || len(sh) != 32 || len(cb) > documentLimit {
		return state, info, diagnostic("load-config-metadata", nil)
	}
	hash := sha256.Sum256(cb)
	if !bytes.Equal(hash[:], ch) {
		return state, info, diagnostic("load-config-hash", nil)
	}
	d, stored, origin, questions, e := decodeSavedWith(cb, nil)
	if e != nil {
		return state, info, diagnostic("load-config-schema", e)
	}
	if stored != schema {
		return state, info, diagnostic("load-config-schema-mismatch", nil)
	}
	// A version of an earlier form keeps the bytes it was saved with, and the document as it reads now is what
	// a batch that changes nothing is compared with.
	canonicalBody, e := canonical(d)
	state.legacy, state.origin, state.storedBody, state.questions = stored < schemaVersion, origin, cb, questions
	if e != nil || !state.legacy && !bytes.Equal(canonicalBody, cb) {
		return state, info, diagnostic("load-config-canonical", e)
	}
	cb = canonicalBody
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
	for _, h := range heldSecrets(&d) {
		if !h.ref.Unresolved {
			if _, ok := state.Secrets.Scopes[h.scope][h.ref.Secret]; !ok {
				return state, info, diagnostic("load-secret-missing-ref", nil)
			}
		}
	}
	state.Versions, e = loadObjectVersions(ctx, conn, &state, number, requested, false)
	if e != nil {
		return state, info, e
	}
	return state, info, nil
}

// A cold recovery check may encounter additive tables that normal startup creates.
func loadObjectVersions(ctx context.Context, conn *sql.Conn, state *savedState, number int64, requested []objectKey, allowAbsentTables bool) (map[objectKey]objectVersion, error) {
	active := objects(&state.Config)
	wanted := map[objectKey]bool{}
	for k := range active {
		wanted[k] = true
	}
	// Lists and singletons carry versions even when empty or unset.
	for _, col := range append(slices.Clone(orderedCollections), singletons...) {
		wanted[listKey(col)] = true
	}
	for _, k := range requested {
		if validCollection(k.Collection) {
			wanted[k] = true
		}
	}
	versions := make(map[objectKey]objectVersion, len(wanted))
	latest := map[objectKey]int64{}
	for _, col := range collections {
		ids := []string{}
		for key := range wanted {
			if key.Collection == col {
				ids = append(ids, key.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		// A version of a form before schema 4 has its aliases and rules recorded under the names they had.
		table, recorded := changeTable(col), []string{col}
		if state.legacy && legacyCollections[col] != nil {
			table, recorded = "policy_changes", legacyCollections[col]
		}
		if allowAbsentTables && table != "object_changes" {
			var count int
			if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil {
				return nil, err
			}
			if count == 0 {
				continue
			}
		}
		for _, name := range recorded {
			asked := map[string][]string{}
			for _, id := range ids {
				// A rule the upgrade split from another has the version of the rule it came from.
				was := cmp.Or(state.origin[id], id)
				if id == col {
					was = name
				}
				asked[was] = append(asked[was], id)
			}
			raw, _ := json.Marshal(slices.Collect(maps.Keys(asked)))
			rows, err := conn.QueryContext(ctx, `SELECT c.object_id,c.version_id,c.deleted FROM `+table+` c WHERE c.collection=? AND c.object_id IN (SELECT value FROM json_each(?)) AND c.version_id=(SELECT max(n.version_id) FROM `+table+` n WHERE n.collection=c.collection AND n.object_id=c.object_id AND n.version_id<=?)`, name, string(raw), number)
			if err != nil {
				return nil, diagnostic("load-object-metadata-query", err)
			}
			for rows.Next() {
				var oid string
				var version int64
				var deleted bool
				if err = rows.Scan(&oid, &version, &deleted); err != nil {
					rows.Close()
					return nil, diagnostic("load-object-metadata-scan", err)
				}
				if version <= 0 || version > number {
					rows.Close()
					return nil, diagnostic("load-object-metadata-range", nil)
				}
				for _, id := range asked[oid] {
					// The rule list was two lists: it changed last when either did.
					if key := (objectKey{col, id}); version > latest[key] {
						latest[key] = version
						versions[key] = objectVersion{Version: Version(strconv.FormatInt(version, 10)), Deleted: deleted}
					}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, diagnostic("load-object-metadata-rows", err)
			}
		}
	}
	for key := range active {
		meta, ok := versions[key]
		if !ok || meta.Deleted {
			return nil, diagnostic("load-object-metadata-missing", nil)
		}
	}
	return versions, nil
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
		if collection == "roles" {
			for _, role := range authz.BuiltIns() {
				view, e := viewObject(&role, "0", state.Secrets)
				if e != nil {
					return e
				}
				result.Items = append(result.Items, view)
			}
		}
		index := objects(&state.Config)
		keys := []objectKey{}
		for k := range index {
			if k.Collection == collection {
				keys = append(keys, k)
			}
		}
		slices.SortFunc(keys, compareKeys)
		if slices.Contains(orderedCollections, collection) {
			// A rule list is returned in execution order, with the version that guards that order.
			result.Version = versionOf(&state, listKey(collection)).Version
			keys = keys[:0]
			for _, id := range orderedIDs(&state.Config, collection) {
				keys = append(keys, objectKey{collection, id})
			}
		}
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
		if collection == "roles" {
			for _, role := range authz.BuiltIns() {
				if role.ID == id {
					var e error
					result, e = viewObject(&role, "0", state.Secrets)
					version = state.Version
					return e
				}
			}
		}
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
	rows, e := conn.QueryContext(ctx, "SELECT v.id,v.parent_id,v.created_at,v.actor,v.changed_object_count,coalesce(w.andaru_version,'') FROM versions v LEFT JOIN version_writers w ON w.version_id=v.id WHERE v.id<? ORDER BY v.id DESC LIMIT ?", upper, limit+1)
	if e != nil {
		return result, diagnostic("history-query", e)
	}
	defer rows.Close()
	result = HistoryView{State: "saved", Items: []VersionInfo{}}
	for rows.Next() {
		var number int64
		var parent sql.NullInt64
		var info VersionInfo
		if e = rows.Scan(&number, &parent, &info.CreatedAt, &info.Actor, &info.ChangedObjectCount, &info.WrittenBy); e != nil {
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
	result = VersionView{State: "saved", VersionInfo: info, Value: snapshot.Value, Changes: []objectChange{}, Questions: state.questions}
	number, _ := strconvVersion(id)
	var query []string
	var arguments []any
	for _, table := range changeTables {
		query = append(query, "SELECT collection,object_id,created,deleted FROM "+table+" WHERE version_id=?")
		arguments = append(arguments, number)
	}
	rows, e := conn.QueryContext(ctx, strings.Join(query, " UNION ALL "), arguments...)
	if e != nil {
		return result, diagnostic("version-changes-query", e)
	}
	defer rows.Close()
	for rows.Next() {
		var change objectChange
		if e = rows.Scan(&change.Collection, &change.ID, &change.Created, &change.Deleted); e != nil {
			return result, diagnostic("version-changes-scan", e)
		}
		// A version of a form before schema 4 recorded the aliases and the two rule lists under other names.
		if current := currentCollection(change.Collection); current != change.Collection {
			if change.ID == change.Collection {
				change.ID = current
			}
			change.Collection = current
		}
		result.Changes = append(result.Changes, change)
		// What changed a rule the upgrade split changed the rules it was split into, which the value shows.
		for derived, from := range state.origin {
			if change.Collection == "rules" && change.ID == from {
				split := change
				split.ID = derived
				result.Changes = append(result.Changes, split)
			}
		}
	}
	if e = rows.Err(); e != nil {
		return result, diagnostic("version-changes-rows", e)
	}
	slices.SortFunc(result.Changes, func(a, b objectChange) int {
		return compareKeys(objectKey{a.Collection, a.ID}, objectKey{b.Collection, b.ID})
	})
	result.Changes = slices.Compact(result.Changes)
	return result, nil
}

// RealizationInput carries secret values for rendering. It has no JSON form
// and must never be serialised or logged.
type RealizationInput struct {
	SavedVersion Version
	Value        json.RawMessage
	Secrets      map[string]string
	AddonSecrets map[string]map[string]string
	// CertificateKeys are the private keys of the certificates enabled add-ons name, by certificate ID. They are not
	// compared by SameRealization: andaru realize cert-sync deploys a renewed certificate without an apply.
	CertificateKeys map[string]string
	Authorization   authz.Snapshot
}

// SameRealization reports whether two saved versions realize the same network and add-ons: their declarations
// agree except for the settings that take effect when saved, notifications, and the secrets their links and
// enabled add-ons use agree. An apply of one over the other would change nothing, so the other needs no apply (#218).
func SameRealization(a, b RealizationInput) bool {
	return maps.Equal(a.Secrets, b.Secrets) && maps.EqualFunc(a.AddonSecrets, b.AddonSecrets, maps.Equal) && bytes.Equal(realized(a.Value), realized(b.Value))
}

func stripSecretViewMeta(raw json.RawMessage, requireSecretKey bool) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw, nil
	}
	if requireSecretKey {
		if _, hasSecret := m["secret"]; !hasSecret {
			return raw, nil
		}
	}
	delete(m, "updatedAt")
	delete(m, "version")
	return json.Marshal(m)
}

// realized is a declaration without the settings an apply does not realize; one it cannot read is kept whole.
func realized(value json.RawMessage) []byte {
	var d map[string]json.RawMessage
	if json.Unmarshal(value, &d) != nil {
		return value
	}
	delete(d, "notifications")
	delete(d, "pki") // its certificates are served where they are named, by cert-sync
	// Of the ACME orders, only an enabled HTTP-01 order's segments are realized: the packet filter redirects port 80
	// for them, and the engine opens the redirect on the segments the live generation holds.
	if raw, ok := d["acme"]; ok {
		delete(d, "acme")
		var a struct {
			Orders []struct {
				ID        string `json:"id"`
				Enabled   bool   `json:"enabled"`
				Challenge struct {
					Type   string `json:"type"`
					HTTP01 *struct {
						ListenSegments []string `json:"listenSegments"`
					} `json:"http01"`
				} `json:"challenge"`
			} `json:"orders"`
		}
		if json.Unmarshal(raw, &a) != nil {
			return value
		}
		listens := map[string][]string{}
		for _, o := range a.Orders {
			if o.Enabled && o.Challenge.Type == "http-01" && o.Challenge.HTTP01 != nil {
				listens[o.ID] = o.Challenge.HTTP01.ListenSegments
			}
		}
		if len(listens) > 0 {
			b, err := json.Marshal(listens)
			if err != nil {
				return value
			}
			d["acmeHttp01"] = b
		}
	}
	// A disabled route renders nothing, and a route's name and description are not rendered.
	var routes []map[string]json.RawMessage
	if len(d["routes"]) > 0 && json.Unmarshal(d["routes"], &routes) == nil {
		routes = slices.DeleteFunc(routes, func(r map[string]json.RawMessage) bool { return string(r["enabled"]) != "true" })
		for _, r := range routes {
			delete(r, "name")
			delete(r, "description")
		}
		if len(routes) == 0 {
			delete(d, "routes")
		} else if raw, err := json.Marshal(routes); err == nil {
			d["routes"] = raw
		} else {
			return value
		}
	}
	// A disabled WireGuard interface or peer renders nothing, names and descriptions are not rendered, and its keys
	// are compared in SameRealization.
	if raw, ok := d["vpn"]; ok {
		var err error
		var vpn struct {
			WireGuard []map[string]json.RawMessage `json:"wireguard"`
		}
		if json.Unmarshal(raw, &vpn) != nil {
			return value
		}
		vpn.WireGuard = slices.DeleteFunc(vpn.WireGuard, func(w map[string]json.RawMessage) bool { return string(w["enabled"]) != "true" })
		for _, w := range vpn.WireGuard {
			delete(w, "name")
			delete(w, "description")
			delete(w, "privateKeyRef")
			var peers []map[string]json.RawMessage
			if json.Unmarshal(w["peers"], &peers) != nil {
				continue
			}
			peers = slices.DeleteFunc(peers, func(p map[string]json.RawMessage) bool { return string(p["enabled"]) != "true" })
			for _, p := range peers {
				delete(p, "name")
				delete(p, "description")
				if _, has := p["presharedKeyRef"]; has {
					p["presharedKeyRef"] = json.RawMessage("true") // its value is in Secrets; that it has one is rendered
				}
			}
			if len(peers) == 0 {
				delete(w, "peers")
			} else if w["peers"], err = json.Marshal(peers); err != nil {
				return value
			}
		}
		delete(d, "vpn")
		if len(vpn.WireGuard) > 0 {
			if d["vpn"], err = json.Marshal(vpn.WireGuard); err != nil {
				return value
			}
		}
	}
	// A secret view says when its secret last changed; the secret itself is compared in SameRealization, so a
	// secret changed and changed back realizes as before.
	var links []map[string]json.RawMessage
	if json.Unmarshal(d["links"], &links) == nil {
		for _, l := range links {
			rawPass, ok := l["password"]
			if !ok {
				continue
			}
			stripped, err := stripSecretViewMeta(rawPass, false)
			if err != nil {
				return value
			}
			l["password"] = stripped
		}
		raw, err := json.Marshal(links)
		if err != nil {
			return value
		}
		d["links"] = raw
	}
	var addons []map[string]json.RawMessage
	if len(d["addons"]) > 0 && json.Unmarshal(d["addons"], &addons) == nil {
		for _, a := range addons {
			var settings map[string]json.RawMessage
			if json.Unmarshal(a["settings"], &settings) != nil || settings == nil {
				continue
			}
			for k, rawVal := range settings {
				stripped, err := stripSecretViewMeta(rawVal, true)
				if err != nil {
					return value
				}
				settings[k] = stripped
			}
			rawSettings, err := json.Marshal(settings)
			if err != nil {
				return value
			}
			a["settings"] = rawSettings
		}
		raw, err := json.Marshal(addons)
		if err != nil {
			return value
		}
		d["addons"] = raw
	}
	out, err := json.Marshal(d) // with its keys sorted, so both read alike
	if err != nil {
		return value
	}
	return out
}

// RealizationInput returns one saved version with the secrets its links
// reference. A nil version means the saved head.
func (s *Store) RealizationInput(ctx context.Context, version *Version) (result RealizationInput, err error) {
	return s.realizationInput(ctx, version, false)
}

// ApplyInput is RealizationInput for an apply or a kept recovery network, refused while the version it reads (the
// head, for every caller) waits for decisions after an upgrade, in the same read: realizing a provisional reading
// would put choices nobody made on the network (settings safe mode).
func (s *Store) ApplyInput(ctx context.Context, version *Version) (RealizationInput, error) {
	return s.realizationInput(ctx, version, true)
}

func (s *Store) realizationInput(ctx context.Context, version *Version, held bool) (result RealizationInput, err error) {
	id := Version("")
	if version != nil {
		id = *version
	}
	err = s.read(ctx, id, nil, func(state savedState, _ VersionInfo) error {
		if held && len(state.questions) > 0 {
			return ErrUpgradePending
		}
		if e := checkResolvedReferences(&state.Config); e != nil {
			return e
		}
		view, e := publicSnapshot(state)
		if e != nil {
			return e
		}
		authorization, e := authz.NewSnapshot(state.Config.Roles)
		if e != nil {
			return e
		}
		result = RealizationInput{SavedVersion: state.Version, Value: view.Value, Secrets: map[string]string{}, Authorization: authorization}
		for _, l := range state.Config.Links {
			if l.Password != nil {
				result.Secrets[l.Password.Secret] = state.Secrets.Scopes["core"][l.Password.Secret].Value
			}
		}
		realizedWireGuardSecrets(&state.Config, state.Secrets, result.Secrets)
		certs := certificateIndex(&state.Config)
		for i := range state.Config.Addons {
			a := &state.Config.Addons[i]
			if !a.Enabled {
				continue
			}
			for _, id := range addonCertificateRefs(a) {
				if c, ok := certs[id]; ok && !c.KeyRef.Unresolved {
					if result.CertificateKeys == nil {
						result.CertificateKeys = map[string]string{}
					}
					result.CertificateKeys[id] = state.Secrets.Scopes["core"][c.KeyRef.Secret].Value
				}
			}
			scoped := state.Secrets.Scopes[addonSecretScope(a.ID)]
			for _, k := range addonSettingSecretKeys(a.Settings) {
				if ref, ok := addonSecretRef(a.Settings[k]); ok && !ref.Unresolved {
					if result.AddonSecrets == nil {
						result.AddonSecrets = map[string]map[string]string{}
					}
					if result.AddonSecrets[a.ID] == nil {
						result.AddonSecrets[a.ID] = map[string]string{}
					}
					result.AddonSecrets[a.ID][ref.Secret] = scoped[ref.Secret].Value
				}
			}
		}
		return nil
	})
	return
}

// orderedIDs is an ordered list's IDs in execution order.
func orderedIDs(d *declaration, collection string) []string {
	var ids []string
	switch collection {
	case "rules":
		for _, r := range d.Rules {
			ids = append(ids, r.ID)
		}
	case "nat.portForwards":
		for _, f := range d.NAT.PortForwards {
			ids = append(ids, f.ID)
		}
	case "nat.oneToOne":
		for _, o := range d.NAT.OneToOne {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

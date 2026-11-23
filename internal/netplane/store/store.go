// SPDX-License-Identifier: AGPL-3.0-or-later
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// LinkSample is one rolling time-series measurement for a physical link.
type LinkSample struct {
	DeviceID string    `json:"device_id"`
	IfName   string    `json:"if_name"`
	Time     time.Time `json:"time"`
	RxBps    float64   `json:"rx_bps"`
	TxBps    float64   `json:"tx_bps"`
	RxErrors uint64    `json:"rx_errors"`
	TxErrors uint64    `json:"tx_errors"`
	SfpRxDbm *float64  `json:"sfp_rx_dbm,omitempty"`
	SfpTempC *float64  `json:"sfp_temp_c,omitempty"`
	PoEWatts *float64  `json:"poe_watts,omitempty"`
}

// NetworkState aggregates the latest snapshots across all polled targets.
type NetworkState struct {
	driver.Inventory
}

// Store provides SQLite WAL persistence for targets, snapshots, samples, topology, and diagnostics.
type Store struct {
	db        *sql.DB
	retention time.Duration
}

// Open opens (or creates) a SQLite database at path in WAL mode and runs embedded migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// Single connection eliminates SQLITE_BUSY under concurrent poller goroutines.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 5000;",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply %q: %w", pragma, err)
		}
	}

	s := &Store{
		db:        db,
		retention: 24 * time.Hour,
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// SetRetention configures rolling link_samples retention duration.
func (s *Store) SetRetention(d time.Duration) {
	if d > 0 {
		s.retention = d
	}
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	sqlBytes, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		return fmt.Errorf("read migration 0001: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("apply migration 0001: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations (version, applied_at) VALUES (1, ?)`,
		time.Now().UTC().UnixNano(),
	); err != nil {
		return err
	}

	var v2 int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&v2); err != nil {
		return err
	}
	if v2 == 0 {
		hasCol, err := s.hasColumn(ctx, "link_samples", "target_id")
		if err != nil {
			return err
		}
		if !hasCol {
			m2, err := migrationsFS.ReadFile("migrations/0002_link_samples_target.sql")
			if err != nil {
				return fmt.Errorf("read migration 0002: %w", err)
			}
			if _, err := s.db.ExecContext(ctx, string(m2)); err != nil {
				return fmt.Errorf("apply migration 0002: %w", err)
			}
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO schema_migrations (version, applied_at) VALUES (2, ?)`,
			time.Now().UTC().UnixNano(),
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) hasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// UpsertTarget saves a target configuration (excluding runtime ResolvedSecrets).
func (s *Store) UpsertTarget(ctx context.Context, cfg driver.TargetConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal target %s: %w", cfg.ID, err)
	}
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}
	pollMs := cfg.PollInterval.Milliseconds()
	if pollMs <= 0 {
		pollMs = 15000
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO targets (id, name, driver, endpoint, enabled, poll_interval_ms, config_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			driver = excluded.driver,
			endpoint = excluded.endpoint,
			enabled = excluded.enabled,
			poll_interval_ms = excluded.poll_interval_ms,
			config_json = excluded.config_json,
			updated_at = excluded.updated_at
	`, cfg.ID, cfg.Name, cfg.Driver, cfg.Endpoint, enabled, pollMs, string(raw), time.Now().UTC().UnixNano())
	return err
}

// ListTargets returns all stored target configurations ordered by ID.
func (s *Store) ListTargets(ctx context.Context) ([]driver.TargetConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT config_json FROM targets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []driver.TargetConfig{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var cfg driver.TargetConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// DeleteTarget removes a target and its cached snapshot, devices and counter baselines; link and
// diagnostic history ages out via retention.
func (s *Store) DeleteTarget(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM targets WHERE id = ?`, id); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM target_snapshots WHERE target_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE target_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM link_counter_state WHERE target_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// maxCounterGap bounds the time between two counter readings used for a bitrate; after a
// longer outage the average over the gap says nothing about current traffic.
// shortcut: fixed bound, not tied to the target's poll interval; derive it if intervals exceed ~5m.
const maxCounterGap = 15 * time.Minute

// maxCarryOver bounds how long a section the driver keeps failing to refresh is carried over.
const maxCarryOver = 15 * time.Minute

// PruneTargets deletes the stored config, snapshot and devices of every target whose ID is
// not in keep, so removed or disabled targets stop showing stale (online) devices.
func (s *Store) PruneTargets(ctx context.Context, keep map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM targets UNION SELECT target_id FROM target_snapshots UNION SELECT target_id FROM devices`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	// Wipe the stored graph first: it may show the pruned devices, readers re-solve without
	// it, and a failure here leaves the stale IDs in place so the next start retries.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM topology_snapshots`); err != nil {
		return err
	}
	for _, id := range stale {
		if err := s.DeleteTarget(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// SaveSnapshot normalizes snap, computes positive link bitrates from previous counter state,
// persists the target's snapshot and indexed devices transactionally, records link_samples,
// and prunes samples and diagnostic runs older than retention. Returns the previous snapshot for diffing (nil on first poll).
func (s *Store) SaveSnapshot(ctx context.Context, snap *driver.Snapshot) (*driver.Snapshot, error) {
	if snap == nil || snap.TargetID == "" {
		return nil, fmt.Errorf("snapshot and target_id are required")
	}
	if snap.CollectedAt.IsZero() {
		snap.CollectedAt = time.Now().UTC()
	}
	snap.Normalize()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var prev *driver.Snapshot
	var prevRaw string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_json FROM target_snapshots WHERE target_id = ?`, snap.TargetID).Scan(&prevRaw)
	if err == nil {
		var ps driver.Snapshot
		if uerr := json.Unmarshal([]byte(prevRaw), &ps); uerr == nil {
			prev = &ps
		}
	} else if err != sql.ErrNoRows {
		return nil, err
	}

	// Sections the driver could not refresh keep their last data for a while, so a failed
	// best-effort call does not look like every client or neighbour leaving.
	snap.CarryOver(prev, maxCarryOver)

	tsNano := snap.CollectedAt.UTC().UnixNano()

	// Compute bitrates from persisted counter state and record rolling samples.
	for i := range snap.Links {
		l := &snap.Links[i]
		var rxBps, txBps float64
		if l.Counters != nil {
			var prevTs, prevRx, prevTx int64
			var prev32 bool
			err := tx.QueryRowContext(ctx,
				`SELECT collected_at, rx_bytes, tx_bytes, counter32 FROM link_counter_state WHERE target_id = ? AND device_id = ? AND if_name = ?`,
				snap.TargetID, l.DeviceID, l.IfName,
			).Scan(&prevTs, &prevRx, &prevTx, &prev32)
			// A counter width change (e.g. SNMP falling back from ifXTable) makes the delta meaningless.
			elapsed := time.Duration(tsNano - prevTs)
			if err == nil && elapsed > 0 && elapsed <= maxCounterGap && prev32 == l.Counters.Counter32 {
				rxBps = model.ComputeBitrate(uint64(prevRx), l.Counters.RxBytes, elapsed, l.Counters.Counter32)
				txBps = model.ComputeBitrate(uint64(prevTx), l.Counters.TxBytes, elapsed, l.Counters.Counter32)
			}
			l.Rates = &model.PortRates{RxBps: rxBps, TxBps: txBps}

			if _, err := tx.ExecContext(ctx, `
				INSERT INTO link_counter_state (target_id, device_id, if_name, collected_at, rx_bytes, tx_bytes, counter32)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(target_id, device_id, if_name) DO UPDATE SET
					collected_at = excluded.collected_at,
					rx_bytes = excluded.rx_bytes,
					tx_bytes = excluded.tx_bytes,
					counter32 = excluded.counter32
			`, snap.TargetID, l.DeviceID, l.IfName, tsNano, int64(l.Counters.RxBytes), int64(l.Counters.TxBytes), l.Counters.Counter32); err != nil {
				return nil, err
			}
		} else if l.Rates != nil {
			rxBps = l.Rates.RxBps
			txBps = l.Rates.TxBps
		}

		var rxErr, txErr uint64
		if l.Counters != nil {
			rxErr = l.Counters.RxErrors
			txErr = l.Counters.TxErrors
		}
		var sfpRx, sfpTemp, poeW any
		if l.SFP != nil && l.SFP.Present {
			if l.SFP.RxPowerDbm != nil {
				sfpRx = *l.SFP.RxPowerDbm
			}
			if l.SFP.TempC != nil {
				sfpTemp = *l.SFP.TempC
			}
		}
		if l.PoE != nil && l.PoE.Supported {
			poeW = l.PoE.Watts
		}

		if l.Counters != nil || l.Rates != nil || sfpRx != nil || sfpTemp != nil || poeW != nil {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO link_samples (target_id, device_id, if_name, ts, rx_bps, tx_bps, rx_errors, tx_errors, sfp_rx_dbm, sfp_temp_c, poe_watts)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, snap.TargetID, l.DeviceID, l.IfName, tsNano, rxBps, txBps, int64(rxErr), int64(txErr), sfpRx, sfpTemp, poeW); err != nil {
				return nil, err
			}
		}
	}

	snapRaw, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO target_snapshots (target_id, collected_at, snapshot_json)
		VALUES (?, ?, ?)
		ON CONFLICT(target_id) DO UPDATE SET
			collected_at = excluded.collected_at,
			snapshot_json = excluded.snapshot_json
	`, snap.TargetID, tsNano, string(snapRaw)); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE target_id = ?`, snap.TargetID); err != nil {
		return nil, err
	}
	for _, d := range snap.Devices {
		devRaw, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		online := 0
		if d.Online {
			online = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO devices (id, target_id, driver, name, role, primary_mac, mgmt_ip, online, last_seen, data_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id, target_id) DO UPDATE SET
				driver = excluded.driver,
				name = excluded.name,
				role = excluded.role,
				primary_mac = excluded.primary_mac,
				mgmt_ip = excluded.mgmt_ip,
				online = excluded.online,
				last_seen = excluded.last_seen,
				data_json = excluded.data_json
		`, d.ID, snap.TargetID, d.Driver, d.Name, string(d.Role), d.PrimaryMAC, d.MgmtIP, online, d.LastSeen.UTC().UnixNano(), string(devRaw)); err != nil {
			return nil, err
		}
	}

	cutoff := snap.CollectedAt.UTC().Add(-s.retention).UnixNano()
	if _, err := tx.ExecContext(ctx, `DELETE FROM link_samples WHERE ts < ?`, cutoff); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostic_runs WHERE started_at < ?`, cutoff); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return prev, nil
}

// MarkTargetOffline flags every device and client in the target's stored snapshot offline,
// for a target that stopped answering. It returns the snapshot before and after the change
// for diffing, or nil, nil when the target has no stored snapshot.
func (s *Store) MarkTargetOffline(ctx context.Context, targetID string, at time.Time) (prev, curr *driver.Snapshot, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var raw string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_json FROM target_snapshots WHERE target_id = ?`, targetID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	prev, curr = &driver.Snapshot{}, &driver.Snapshot{}
	if err := json.Unmarshal([]byte(raw), prev); err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal([]byte(raw), curr) // same bytes, cannot fail now
	curr.CollectedAt = at.UTC()
	curr.MarkedOffline = true // its sections are not fresh: never carry them over
	for i := range curr.Devices {
		curr.Devices[i].Online = false
	}
	for i := range curr.Clients {
		curr.Clients[i].Online = false
	}
	currRaw, err := json.Marshal(curr)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE target_snapshots SET snapshot_json = ? WHERE target_id = ?`, string(currRaw), targetID); err != nil {
		return nil, nil, err
	}
	for _, d := range curr.Devices {
		devRaw, err := json.Marshal(d)
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET online = 0, data_json = ? WHERE id = ? AND target_id = ?`, string(devRaw), d.ID, targetID); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return prev, curr, nil
}

// LoadState merges all stored target snapshots into one sorted NetworkState.
func (s *Store) LoadState(ctx context.Context) (*NetworkState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT snapshot_json FROM target_snapshots ORDER BY target_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	state := &NetworkState{}

	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var snap driver.Snapshot
		if err := json.Unmarshal([]byte(raw), &snap); err != nil {
			return nil, err
		}
		state.Append(snap.Inventory)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	state.FillNil()
	state.Devices = dedupeDevices(state.Devices)
	state.Clients = mergeClients(state.Clients)
	sort.Slice(state.Devices, func(i, j int) bool { return state.Devices[i].ID < state.Devices[j].ID })
	return state, nil
}

// dedupeDevices keeps one device per ID (the most recently seen) when targets overlap.
func dedupeDevices(in []model.Device) []model.Device {
	idx := make(map[string]int, len(in))
	out := in[:0]
	for _, d := range in {
		if i, ok := idx[d.ID]; ok {
			if d.LastSeen.After(out[i].LastSeen) {
				out[i] = d
			}
			continue
		}
		idx[d.ID] = len(out)
		out = append(out, d)
	}
	return out
}

// betterClient reports whether a is the better base record than b: attached beats
// unattached, then online beats offline, then the more recently seen wins.
func betterClient(a, b model.Client) bool {
	if (a.AttachedDeviceID != "") != (b.AttachedDeviceID != "") {
		return a.AttachedDeviceID != ""
	}
	if a.Online != b.Online {
		return a.Online
	}
	return a.LastSeen.After(b.LastSeen)
}

// mergeClients folds clients reported by several targets (e.g. a router's DHCP view and an
// AP's association list) into one record per MAC, based on the best record (see betterClient).
func mergeClients(in []model.Client) []model.Client {
	idx := make(map[string]int, len(in))
	out := make([]model.Client, 0, len(in))
	for _, c := range in {
		if i, ok := idx[c.MAC]; !ok {
			idx[c.MAC] = len(out)
			out = append(out, c)
		} else if betterClient(c, out[i]) {
			out[i] = c
		}
	}
	// Fold the other records into each MAC's best record, compared on its own fields above.
	for _, c := range in {
		m := &out[idx[c.MAC]]
		m.Online = m.Online || c.Online
		if c.LastSeen.After(m.LastSeen) {
			m.LastSeen = c.LastSeen
		}
		if m.Hostname == "" {
			m.Hostname = c.Hostname
		}
		for _, ip := range c.IPs {
			if !slices.Contains(m.IPs, ip) {
				m.IPs = append(m.IPs, ip)
			}
		}
	}
	return out
}

// GetDevice returns one indexed Device by ID: when several targets report it, the most
// recently seen record, ties going to the lowest target ID (the same pick as LoadState).
func (s *Store) GetDevice(ctx context.Context, id string) (*model.Device, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data_json FROM devices WHERE id = ? ORDER BY last_seen DESC, target_id LIMIT 1`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var d model.Device
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, false, err
	}
	return &d, true, nil
}

// QueryLinkSamples returns rolling telemetry samples for a link since the given time.
func (s *Store) QueryLinkSamples(ctx context.Context, deviceID, ifName string, since time.Time) ([]LinkSample, error) {
	sinceNano := since.UTC().UnixNano()
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, rx_bps, tx_bps, rx_errors, tx_errors, sfp_rx_dbm, sfp_temp_c, poe_watts
		FROM link_samples
		WHERE device_id = ? AND if_name = ? AND ts >= ?
		  AND target_id = (
			SELECT target_id FROM link_samples
			WHERE device_id = ? AND if_name = ? AND ts >= ?
			GROUP BY target_id
			ORDER BY (target_id != '') DESC, COUNT(*) DESC, MAX(ts) DESC, target_id ASC
			LIMIT 1
		  )
		ORDER BY ts ASC
	`, deviceID, ifName, sinceNano, deviceID, ifName, sinceNano)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []LinkSample{}
	for rows.Next() {
		// Error counters are stored as int64 bit patterns (SQLite integers are signed), so a
		// counter at or above 1<<63 reads back negative and must convert, not scan, to uint64.
		var ts, rxErr, txErr int64
		sample := LinkSample{DeviceID: deviceID, IfName: ifName}
		if err := rows.Scan(&ts, &sample.RxBps, &sample.TxBps, &rxErr, &txErr, &sample.SfpRxDbm, &sample.SfpTempC, &sample.PoEWatts); err != nil {
			return nil, err
		}
		sample.Time = time.Unix(0, ts).UTC()
		sample.RxErrors, sample.TxErrors = uint64(rxErr), uint64(txErr)
		out = append(out, sample)
	}
	return out, rows.Err()
}

// SaveTopology persists the latest solved topology JSON document.
func (s *Store) SaveTopology(ctx context.Context, generatedAt time.Time, graph any) error {
	raw, err := json.Marshal(graph)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO topology_snapshots (id, generated_at, graph_json)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			generated_at = excluded.generated_at,
			graph_json = excluded.graph_json
	`, generatedAt.UTC().UnixNano(), string(raw))
	return err
}

// LoadTopologyRaw returns the latest persisted topology JSON bytes (nil if none saved yet).
func (s *Store) LoadTopologyRaw(ctx context.Context) ([]byte, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT graph_json FROM topology_snapshots WHERE id = 1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(raw), true, nil
}

// SaveDiagnosticRun persists one executed diagnostic result.
func (s *Store) SaveDiagnosticRun(ctx context.Context, res model.DiagnosticResult) error {
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	okInt := 0
	if res.OK {
		okInt = 1
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO diagnostic_runs (id, kind, device_id, target, ok, started_at, duration_ms, result_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, res.ID, string(res.Kind), res.DeviceID, res.Target, okInt, res.StartedAt.UTC().UnixNano(), res.DurationMs, string(raw))
	return err
}

// ListDiagnosticRuns returns recent diagnostic runs for a device (or all devices if deviceID is empty).
func (s *Store) ListDiagnosticRuns(ctx context.Context, deviceID string, limit int) ([]model.DiagnosticResult, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if deviceID == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT result_json FROM diagnostic_runs ORDER BY started_at DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT result_json FROM diagnostic_runs WHERE device_id = ? ORDER BY started_at DESC LIMIT ?`, deviceID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.DiagnosticResult{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r model.DiagnosticResult
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

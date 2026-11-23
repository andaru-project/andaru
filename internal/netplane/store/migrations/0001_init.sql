-- SPDX-License-Identifier: AGPL-3.0-or-later

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS targets (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    driver           TEXT NOT NULL,
    endpoint         TEXT NOT NULL,
    enabled          INTEGER NOT NULL DEFAULT 1,
    poll_interval_ms INTEGER NOT NULL DEFAULT 15000,
    config_json      TEXT NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS target_snapshots (
    target_id     TEXT PRIMARY KEY,
    collected_at  INTEGER NOT NULL,
    snapshot_json TEXT NOT NULL
);

-- One row per target observing a device: overlapping targets may report the same ID.
CREATE TABLE IF NOT EXISTS devices (
    id          TEXT NOT NULL,
    target_id   TEXT NOT NULL,
    driver      TEXT NOT NULL,
    name        TEXT NOT NULL,
    role        TEXT NOT NULL,
    primary_mac TEXT NOT NULL DEFAULT '',
    mgmt_ip     TEXT NOT NULL DEFAULT '',
    online      INTEGER NOT NULL DEFAULT 0,
    last_seen   INTEGER NOT NULL,
    data_json   TEXT NOT NULL,
    PRIMARY KEY (id, target_id)
);
CREATE INDEX IF NOT EXISTS idx_devices_target ON devices(target_id);
CREATE INDEX IF NOT EXISTS idx_devices_mac ON devices(primary_mac);

-- Keyed per target: two targets reading one port keep separate baselines.
CREATE TABLE IF NOT EXISTS link_counter_state (
    target_id    TEXT NOT NULL,
    device_id    TEXT NOT NULL,
    if_name      TEXT NOT NULL,
    collected_at INTEGER NOT NULL,
    rx_bytes     INTEGER NOT NULL,
    tx_bytes     INTEGER NOT NULL,
    counter32    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (target_id, device_id, if_name)
);

CREATE TABLE IF NOT EXISTS link_samples (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id  TEXT NOT NULL,
    if_name    TEXT NOT NULL,
    ts         INTEGER NOT NULL,
    rx_bps     REAL NOT NULL DEFAULT 0,
    tx_bps     REAL NOT NULL DEFAULT 0,
    rx_errors  INTEGER NOT NULL DEFAULT 0,
    tx_errors  INTEGER NOT NULL DEFAULT 0,
    sfp_rx_dbm REAL,
    sfp_temp_c REAL,
    poe_watts  REAL
);
CREATE INDEX IF NOT EXISTS idx_link_samples_lookup ON link_samples(device_id, if_name, ts);
CREATE INDEX IF NOT EXISTS idx_link_samples_ts ON link_samples(ts);

CREATE TABLE IF NOT EXISTS topology_snapshots (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    generated_at INTEGER NOT NULL,
    graph_json   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS diagnostic_runs (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    device_id   TEXT NOT NULL,
    target      TEXT NOT NULL,
    ok          INTEGER NOT NULL,
    started_at  INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    result_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_dev ON diagnostic_runs(device_id, started_at DESC);

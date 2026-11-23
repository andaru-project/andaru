// SPDX-License-Identifier: AGPL-3.0-or-later
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "netplane.db")
	st, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestStoreTargetsAndIdempotentMigrate(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "netplane.db")
	st1, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	cfg := driver.TargetConfig{
		ID:           "t-sw1",
		Name:         "LG510S",
		Driver:       "openwrt",
		Endpoint:     "https://192.0.2.10",
		Enabled:      true,
		PollInterval: 10 * time.Second,
		SecretRefs:   map[string]model.SecretRef{"token": {Secret: "SW1_TOKEN"}},
	}
	if err := st1.UpsertTarget(ctx, cfg); err != nil {
		t.Fatalf("UpsertTarget: %v", err)
	}
	_ = st1.Close()

	// Re-open verifies migration idempotency and persisted data.
	st2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer st2.Close()

	targets, err := st2.ListTargets(ctx)
	if err != nil || len(targets) != 1 || targets[0].ID != "t-sw1" {
		t.Fatalf("ListTargets = (%v, %v)", targets, err)
	}
	if err := st2.DeleteTarget(ctx, "t-sw1"); err != nil {
		t.Fatalf("DeleteTarget: %v", err)
	}
	targets, err = st2.ListTargets(ctx)
	if err != nil || len(targets) != 0 {
		t.Fatalf("ListTargets after delete = (%v, %v), want none", targets, err)
	}
}

func TestStoreSnapshotRatesAndRetention(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	st.SetRetention(10 * time.Minute)

	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	rxDbm := -5.4
	tempC := 43.2

	snap1 := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: t0,
		Devices: []model.Device{{
			ID:         "openwrt:02:51:0a:00:00:01",
			Driver:     "openwrt",
			TargetID:   "t1",
			Name:       "LG510S",
			Role:       model.RoleSwitch,
			PrimaryMAC: "02510A000001",
			Online:     true,
			LastSeen:   t0,
		}},
		Links: []model.Link{{
			DeviceID: "openwrt:02:51:0a:00:00:01",
			Index:    9,
			IfName:   "sfp1",
			Media:    model.MediaSFPPl,
			AdminUp:  true,
			LinkUp:   true,
			SFP:      &model.SfpDom{Present: true, RxPowerDbm: &rxDbm, TempC: &tempC},
			Counters: &model.PortCounters{RxBytes: 1000, TxBytes: 2000},
		}},
	}
	prev, err := st.SaveSnapshot(ctx, snap1)
	if err != nil {
		t.Fatalf("SaveSnapshot 1: %v", err)
	}
	if prev != nil {
		t.Fatalf("expected nil prev on first save, got %+v", prev)
	}

	// Second poll 10s later: +10,000 RxBytes = 8,000 bps; +20,000 TxBytes = 16,000 bps.
	t1 := t0.Add(10 * time.Second)
	snap2 := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: t1,
		Devices:     snap1.Devices,
		Links: []model.Link{{
			DeviceID: "openwrt:02:51:0a:00:00:01",
			Index:    9,
			IfName:   "sfp1",
			Media:    model.MediaSFPPl,
			AdminUp:  true,
			LinkUp:   true,
			SFP:      &model.SfpDom{Present: true, RxPowerDbm: &rxDbm, TempC: &tempC},
			Counters: &model.PortCounters{RxBytes: 11000, TxBytes: 22000},
		}},
	}
	prev, err = st.SaveSnapshot(ctx, snap2)
	if err != nil {
		t.Fatalf("SaveSnapshot 2: %v", err)
	}
	if prev == nil || prev.CollectedAt != t0 {
		t.Fatalf("unexpected prev snapshot: %+v", prev)
	}
	if snap2.Links[0].Rates == nil || snap2.Links[0].Rates.RxBps != 8000 || snap2.Links[0].Rates.TxBps != 16000 {
		t.Fatalf("unexpected computed rates: %+v", snap2.Links[0].Rates)
	}

	// Verify indexed device lookup & merged state.
	dev, ok, err := st.GetDevice(ctx, "openwrt:02:51:0a:00:00:01")
	if err != nil || !ok || dev.PrimaryMAC != "02:51:0a:00:00:01" {
		t.Fatalf("GetDevice = (%+v, %v, %v)", dev, ok, err)
	}
	state, err := st.LoadState(ctx)
	if err != nil || len(state.Devices) != 1 || len(state.Links) != 1 {
		t.Fatalf("LoadState = (%+v, %v)", state, err)
	}

	// Third poll 20m later prunes samples from t0/t1 (retention = 10m).
	t2 := t0.Add(20 * time.Minute)
	snap3 := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: t2,
		Devices:     snap1.Devices,
		Links: []model.Link{{
			DeviceID: "openwrt:02:51:0a:00:00:01",
			Index:    9,
			IfName:   "sfp1",
			Media:    model.MediaSFPPl,
			SFP:      &model.SfpDom{Present: true, RxPowerDbm: &rxDbm, TempC: &tempC},
			Counters: &model.PortCounters{RxBytes: 500, TxBytes: 500}, // simulates counter reset
		}},
	}
	if _, err := st.SaveSnapshot(ctx, snap3); err != nil {
		t.Fatalf("SaveSnapshot 3: %v", err)
	}
	if snap3.Links[0].Rates.RxBps < 0 {
		t.Fatalf("negative bitrate after counter reset: %v", snap3.Links[0].Rates.RxBps)
	}

	samples, err := st.QueryLinkSamples(ctx, "openwrt:02:51:0a:00:00:01", "sfp1", t0)
	if err != nil {
		t.Fatalf("QueryLinkSamples: %v", err)
	}
	if len(samples) != 1 || !samples[0].Time.Equal(t2) {
		t.Fatalf("expected 1 retained sample at t2, got %+v", samples)
	}
	if got := samples[0]; got.SfpRxDbm == nil || *got.SfpRxDbm != rxDbm || got.SfpTempC == nil || *got.SfpTempC != tempC || got.PoEWatts != nil {
		t.Fatalf("SFP sample readback = %+v", got)
	}

	// Verify PoE + SFP DOM sample readback and missing device lookup.
	t3 := t2.Add(15 * time.Second)
	snap4 := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: t3,
		Devices:     snap1.Devices,
		Links: []model.Link{{
			DeviceID: "openwrt:02:51:0a:00:00:01",
			IfName:   "lan1",
			Media:    model.Media25GE,
			PoE:      &model.PortPoE{Supported: true, Enabled: true, Watts: 12.4},
			Rates:    &model.PortRates{RxBps: 5000, TxBps: 7000},
		}},
	}
	if _, err := st.SaveSnapshot(ctx, snap4); err != nil {
		t.Fatalf("SaveSnapshot 4: %v", err)
	}
	poeSamples, err := st.QueryLinkSamples(ctx, "openwrt:02:51:0a:00:00:01", "lan1", t2)
	if err != nil || len(poeSamples) != 1 || poeSamples[0].PoEWatts == nil || *poeSamples[0].PoEWatts != 12.4 || poeSamples[0].SfpRxDbm != nil || poeSamples[0].SfpTempC != nil {
		t.Fatalf("unexpected PoE samples: (%+v, %v)", poeSamples, err)
	}
	if _, ok, err := st.GetDevice(ctx, "nonexistent"); err != nil || ok {
		t.Fatalf("expected missing device false, got (%v, %v)", ok, err)
	}
	if _, err := st.SaveSnapshot(ctx, nil); err == nil {
		t.Fatal("expected error for nil snapshot")
	}
}

func TestStoreTopologyAndDiagnostics(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if raw, ok, err := st.LoadTopologyRaw(ctx); err != nil || ok || raw != nil {
		t.Fatalf("expected empty topology, got (%v, %v, %v)", raw, ok, err)
	}
	now := time.Now().UTC()
	if err := st.SaveTopology(ctx, now, map[string]any{"nodes": []string{"d1"}}); err != nil {
		t.Fatalf("SaveTopology: %v", err)
	}
	raw, ok, err := st.LoadTopologyRaw(ctx)
	if err != nil || !ok || len(raw) == 0 {
		t.Fatalf("LoadTopologyRaw = (%s, %v, %v)", raw, ok, err)
	}

	run := model.DiagnosticResult{
		ID:         "diag-1",
		Kind:       model.DiagPing,
		DeviceID:   "d1",
		Target:     "1.1.1.1",
		OK:         true,
		StartedAt:  now,
		DurationMs: 14,
		Summary:    "3/3 packets received",
	}
	if err := st.SaveDiagnosticRun(ctx, run); err != nil {
		t.Fatalf("SaveDiagnosticRun: %v", err)
	}
	runs, err := st.ListDiagnosticRuns(ctx, "d1", 10)
	if err != nil || len(runs) != 1 || runs[0].ID != "diag-1" {
		t.Fatalf("ListDiagnosticRuns = (%+v, %v)", runs, err)
	}
	allRuns, err := st.ListDiagnosticRuns(ctx, "", 10)
	if err != nil || len(allRuns) != 1 {
		t.Fatalf("ListDiagnosticRuns(all) = (%+v, %v)", allRuns, err)
	}
}

func TestStorePruneTargetsAndStaleDevices(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	save := func(target string, devIDs ...string) {
		t.Helper()
		snap := &driver.Snapshot{TargetID: target}
		for _, id := range devIDs {
			snap.Devices = append(snap.Devices, model.Device{ID: id, TargetID: target, Online: true})
		}
		if _, err := st.SaveSnapshot(ctx, snap); err != nil {
			t.Fatalf("SaveSnapshot %s: %v", target, err)
		}
	}
	has := func(id string) bool {
		t.Helper()
		_, ok, err := st.GetDevice(ctx, id)
		if err != nil {
			t.Fatalf("GetDevice %s: %v", id, err)
		}
		return ok
	}

	save("keep", "k1", "k2")
	save("keep", "k1") // k2 left the target's snapshot: its index row must go too
	save("gone", "g1")
	if has("k2") || !has("g1") {
		t.Fatalf("after resave: k2=%v g1=%v", has("k2"), has("g1"))
	}

	if err := st.PruneTargets(ctx, map[string]bool{"keep": true}); err != nil {
		t.Fatalf("PruneTargets: %v", err)
	}
	state, err := st.LoadState(ctx)
	if err != nil || len(state.Devices) != 1 || state.Devices[0].ID != "k1" || has("g1") {
		t.Fatalf("after prune: devices=%v err=%v g1=%v", state.Devices, err, has("g1"))
	}
}

func TestLinkSampleHugeErrorCounters(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	snap := &driver.Snapshot{TargetID: "t", CollectedAt: t0, Links: []model.Link{{
		DeviceID: "d", IfName: "p1", Counters: &model.PortCounters{RxErrors: 1 << 63, TxErrors: ^uint64(0)},
	}}}
	if _, err := st.SaveSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	got, err := st.QueryLinkSamples(ctx, "d", "p1", t0.Add(-time.Second))
	if err != nil || len(got) != 1 || got[0].RxErrors != 1<<63 || got[0].TxErrors != ^uint64(0) {
		t.Fatalf("samples = %+v, err = %v", got, err)
	}
}

func TestLoadStateEmptyEncodesNoNull(t *testing.T) {
	state, err := openTestStore(t).LoadState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(state)
	if strings.Contains(string(b), "null") {
		t.Fatalf("empty state encodes null: %s", b)
	}
}

func TestLoadStateMergesOverlappingTargets(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	router := &driver.Snapshot{TargetID: "router", Clients: []model.Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "phone", IPs: []string{"192.0.2.7"}, LastSeen: t0},
	}}
	ap := &driver.Snapshot{TargetID: "ap", Clients: []model.Client{
		{MAC: "aa:bb:cc:dd:ee:ff", Online: true, AttachedDeviceID: "ap1", IPs: []string{"2001:db8::7"}, LastSeen: t0.Add(time.Minute)},
	}}
	for _, s := range []*driver.Snapshot{router, ap} {
		if _, err := st.SaveSnapshot(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	state, err := st.LoadState(ctx)
	if err != nil || len(state.Clients) != 1 {
		t.Fatalf("clients = %+v err=%v", state.Clients, err)
	}
	c := state.Clients[0]
	if !c.Online || c.AttachedDeviceID != "ap1" || c.Hostname != "phone" || len(c.IPs) != 2 || !c.LastSeen.Equal(t0.Add(time.Minute)) {
		t.Fatalf("merged client = %+v", c)
	}
}

func TestMergeClientsPrefersFreshAssociation(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	got := mergeClients([]model.Client{
		{MAC: "aa:bb:cc:dd:ee:ff", AttachedDeviceID: "ap-a", LastSeen: t0},
		{MAC: "aa:bb:cc:dd:ee:ff", AttachedDeviceID: "ap-b", Online: true, LastSeen: t0.Add(5 * time.Minute)},
	})
	if len(got) != 1 || got[0].AttachedDeviceID != "ap-b" || !got[0].Online {
		t.Fatalf("merged = %+v", got)
	}
}

func TestPruneTargetsDropsStoredTopology(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.SaveSnapshot(ctx, &driver.Snapshot{TargetID: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTopology(ctx, time.Now(), map[string]any{"nodes": []string{"gone"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneTargets(ctx, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.LoadTopologyRaw(ctx); err != nil || ok {
		t.Fatalf("stored topology survived prune: ok=%v err=%v", ok, err)
	}
}

func TestMergeClientsThreeWay(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	got := mergeClients([]model.Client{
		{MAC: "aa:bb:cc:dd:ee:ff", AttachedDeviceID: "ap-a", LastSeen: t0},
		{MAC: "aa:bb:cc:dd:ee:ff", Online: true, LastSeen: t0.Add(2 * time.Minute)},
		{MAC: "aa:bb:cc:dd:ee:ff", AttachedDeviceID: "ap-b", Online: true, LastSeen: t0.Add(time.Minute)},
	})
	if len(got) != 1 || got[0].AttachedDeviceID != "ap-b" || !got[0].LastSeen.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("merged = %+v", got)
	}

}

func TestPruneTargetsKeepsTopologyWhenNothingIsStale(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if err := st.UpsertTarget(ctx, driver.TargetConfig{ID: "on", Driver: "openwrt", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTopology(ctx, time.Now(), map[string]any{"nodes": []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneTargets(ctx, map[string]bool{"on": true}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.LoadTopologyRaw(ctx); err != nil || !ok {
		t.Fatalf("topology wiped with nothing stale: ok=%v err=%v", ok, err)
	}
}

func TestNoBitrateAcrossLongGap(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	snap := func(at time.Time, rx uint64) *driver.Snapshot {
		return &driver.Snapshot{TargetID: "t1", CollectedAt: at, Links: []model.Link{{
			DeviceID: "d1", IfName: "lan1", Counters: &model.PortCounters{RxBytes: rx},
		}}}
	}
	if _, err := st.SaveSnapshot(ctx, snap(t0, 1000)); err != nil {
		t.Fatal(err)
	}
	s2 := snap(t0.Add(2*time.Hour), 1_000_000)
	if _, err := st.SaveSnapshot(ctx, s2); err != nil {
		t.Fatal(err)
	}
	if s2.Links[0].Rates.RxBps != 0 {
		t.Fatalf("rate across a 2h gap = %v, want 0", s2.Links[0].Rates.RxBps)
	}
	s3 := snap(t0.Add(2*time.Hour+30*time.Second), 1_000_000+3750)
	if _, err := st.SaveSnapshot(ctx, s3); err != nil {
		t.Fatal(err)
	}
	if s3.Links[0].Rates.RxBps != 1000 {
		t.Fatalf("rate after the gap = %v, want 1000", s3.Links[0].Rates.RxBps)
	}
}

func TestOverlappingTargetsKeepDeviceAndCounterState(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	save := func(target string, at time.Time, rx uint64, c32 bool, withDev bool) *driver.Snapshot {
		t.Helper()
		snap := &driver.Snapshot{TargetID: target, CollectedAt: at, Links: []model.Link{{
			DeviceID: "d1", IfName: "lan1", Counters: &model.PortCounters{RxBytes: rx, Counter32: c32},
		}}}
		if withDev {
			snap.Devices = []model.Device{{ID: "d1", Name: target, TargetID: target, LastSeen: at}}
		}
		if _, err := st.SaveSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
		return snap
	}
	// a sees d1 later than b, but b saves last: both lookups must pick a.
	save("a", t0.Add(time.Second), 1000, false, true)
	save("b", t0, 5000, true, true)
	dev, ok, err := st.GetDevice(ctx, "d1")
	if err != nil || !ok || dev.Name != "a" {
		t.Fatalf("GetDevice = (%+v, %v, %v), want a's record", dev, ok, err)
	}
	// b stops reporting d1; a still does.
	save("b", t0.Add(10*time.Second), 5000+1250, true, false)
	if dev, ok, err := st.GetDevice(ctx, "d1"); err != nil || !ok || dev.Name != "a" {
		t.Fatalf("GetDevice after b dropped d1 = (%+v, %v, %v)", dev, ok, err)
	}
	// Each target computes its rate from its own baseline despite differing counter widths.
	if s := save("a", t0.Add(11*time.Second), 1000+1250, false, true); s.Links[0].Rates.RxBps != 1000 {
		t.Fatalf("a rate = %v, want 1000", s.Links[0].Rates.RxBps)
	}
}

func TestDiagnosticRunsAgeOut(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	st.SetRetention(time.Hour)
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for id, at := range map[string]time.Time{"old": t0.Add(-2 * time.Hour), "new": t0.Add(-time.Minute)} {
		if err := st.SaveDiagnosticRun(ctx, model.DiagnosticResult{ID: id, Kind: model.DiagPing, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SaveSnapshot(ctx, &driver.Snapshot{TargetID: "t1", CollectedAt: t0}); err != nil {
		t.Fatal(err)
	}
	runs, err := st.ListDiagnosticRuns(ctx, "", 10)
	if err != nil || len(runs) != 1 || runs[0].ID != "new" {
		t.Fatalf("runs = (%+v, %v), want only new", runs, err)
	}
}

func TestSaveSnapshotCarriesUnavailableSections(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	first := &driver.Snapshot{TargetID: "u1", CollectedAt: t0, Clients: []model.Client{{MAC: "aa:aa:aa:aa:aa:01", Online: true}}}
	if _, err := st.SaveSnapshot(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &driver.Snapshot{TargetID: "u1", CollectedAt: t0.Add(time.Minute), Unavailable: []driver.Section{driver.SectionClients}}
	prev, err := st.SaveSnapshot(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	// The saved snapshot (and the one the poller diffs) still has the client.
	if len(second.Clients) != 1 || len(prev.Clients) != 1 {
		t.Fatalf("clients not carried: %v", second.Clients)
	}
	state, err := st.LoadState(ctx)
	if err != nil || len(state.Clients) != 1 {
		t.Fatalf("state clients = %v, %v", state.Clients, err)
	}
}

func TestSaveSnapshotCarryWindowFollowsPollInterval(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	if _, err := st.SaveSnapshot(ctx, &driver.Snapshot{TargetID: "u1", CollectedAt: t0, Clients: []model.Client{{MAC: "aa:aa:aa:aa:aa:01"}}}); err != nil {
		t.Fatal(err)
	}
	// 40 minutes later on a 20-minute interval: within three intervals, so carried.
	s := &driver.Snapshot{TargetID: "u1", CollectedAt: t0.Add(40 * time.Minute), PollInterval: 20 * time.Minute, Unavailable: []driver.Section{driver.SectionClients}}
	if _, err := st.SaveSnapshot(ctx, s); err != nil || len(s.Clients) != 1 {
		t.Fatalf("clients = %v, %v", s.Clients, err)
	}
	// Fresh again, then 61 minutes later on a 20-minute interval: past three intervals.
	if _, err := st.SaveSnapshot(ctx, &driver.Snapshot{TargetID: "u1", CollectedAt: t0.Add(50 * time.Minute), Clients: []model.Client{{MAC: "aa:aa:aa:aa:aa:01"}}}); err != nil {
		t.Fatal(err)
	}
	late := &driver.Snapshot{TargetID: "u1", CollectedAt: t0.Add(111 * time.Minute), PollInterval: 20 * time.Minute, Unavailable: []driver.Section{driver.SectionClients}}
	if _, err := st.SaveSnapshot(ctx, late); err != nil || len(late.Clients) != 0 {
		t.Fatalf("carried past three intervals: %v, %v", late.Clients, err)
	}
}

func TestMigrateLegacyV1Database(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// Simulate an existing DB created by standalone netplane with migration 0001 only (no link_samples.target_id).
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	initSQL, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.ExecContext(ctx, string(initSQL)); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (1, 1)`); err != nil {
		t.Fatal(err)
	}
	_ = rawDB.Close()

	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open legacy DB: %v", err)
	}
	defer func() { _ = st.Close() }()

	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	snap := &driver.Snapshot{TargetID: "t1", CollectedAt: t0, Links: []model.Link{{
		DeviceID: "d1", IfName: "lan1", Counters: &model.PortCounters{RxBytes: 100},
	}}}
	if _, err := st.SaveSnapshot(ctx, snap); err != nil {
		t.Fatalf("SaveSnapshot after v2 migration: %v", err)
	}
	got, err := st.QueryLinkSamples(ctx, "d1", "lan1", t0.Add(-time.Second))
	if err != nil || len(got) != 1 {
		t.Fatalf("QueryLinkSamples = (%+v, %v), want 1 sample", got, err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later
package poller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/events"
	"github.com/andaru-project/andaru/internal/netplane/model"
	"github.com/andaru-project/andaru/internal/netplane/store"
)

type mockDriver struct {
	kind  string
	calls atomic.Int32
	fn    func(ctx context.Context, cfg driver.TargetConfig, call int32) (*driver.Snapshot, error)
}

func (m *mockDriver) Kind() string                             { return m.kind }
func (m *mockDriver) ValidateConfig(driver.TargetConfig) error { return nil }
func (m *mockDriver) Poll(ctx context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	n := m.calls.Add(1)
	return m.fn(ctx, cfg, n)
}

func TestSchedulerConcurrentIsolationAndShutdown(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := driver.NewRegistry()
	goodDrv := &mockDriver{
		kind: "mock-good",
		fn: func(_ context.Context, cfg driver.TargetConfig, n int32) (*driver.Snapshot, error) {
			return &driver.Snapshot{
				TargetID:    cfg.ID,
				CollectedAt: time.Now().UTC(),
				Devices: []model.Device{{
					ID: "mock:00:11:22:33:44:55", Driver: "mock", TargetID: cfg.ID,
					Name: "LabSwitch", Role: model.RoleSwitch, PrimaryMAC: "00:11:22:33:44:55",
					Online: true, LastSeen: time.Now().UTC(),
				}},
				Links: []model.Link{{
					DeviceID: "mock:00:11:22:33:44:55", IfName: "lan1",
					LinkUp: n%2 == 1, Media: model.Media25GE,
				}},
			}, nil
		},
	}
	badDrv := &mockDriver{
		kind: "mock-bad",
		fn: func(_ context.Context, _ driver.TargetConfig, _ int32) (*driver.Snapshot, error) {
			return nil, errors.New("connection refused")
		},
	}
	_ = reg.Register(goodDrv)
	_ = reg.Register(badDrv)

	bus := events.NewBus()
	evCh, unsub := bus.Subscribe(32)
	defer unsub()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sched := New(reg, st, bus, logger)
	sched.SetMaxBackoff(80 * time.Millisecond)

	targets := []driver.TargetConfig{
		{ID: "t-good", Name: "Good", Driver: "mock-good", Enabled: true, PollInterval: 20 * time.Millisecond},
		{ID: "t-bad", Name: "Bad", Driver: "mock-bad", Enabled: true, PollInterval: 20 * time.Millisecond},
		{ID: "t-disabled", Name: "Disabled", Driver: "mock-good", Enabled: false, PollInterval: 20 * time.Millisecond},
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		sched.Run(runCtx, targets)
		close(done)
	}()
	// Runs before the store closes (cleanups run last-in first-out), also when a check fails.
	t.Cleanup(func() { cancel(); <-done })

	// Wait until the good target's second poll has committed its link transition.
	timeout := time.After(2 * time.Second)
	for gotDown := false; !gotDown; {
		select {
		case ev := <-evCh:
			gotDown = ev.Type == events.LinkDown && ev.TargetID == "t-good"
		case <-timeout:
			t.Fatalf("no link.down event for t-good after %d polls", goodDrv.calls.Load())
		}
	}

	stopStart := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("scheduler did not shut down within 100ms")
	}
	if elapsed := time.Since(stopStart); elapsed > 100*time.Millisecond {
		t.Fatalf("shutdown took %v, want <= 100ms", elapsed)
	}

	// Verify failure isolation: good target succeeded while bad target recorded errors.
	statuses := sched.Status()
	if statuses["t-good"].ConsecutiveFailures != 0 || statuses["t-good"].LastSuccess.IsZero() {
		t.Fatalf("unexpected good status: %+v", statuses["t-good"])
	}
	if statuses["t-bad"].ConsecutiveFailures == 0 || statuses["t-bad"].LastError == "" {
		t.Fatalf("expected error recorded on bad target: %+v", statuses["t-bad"])
	}
	if _, ok := statuses["t-disabled"]; ok {
		t.Fatal("disabled target should not have been polled")
	}

	// Verify topology snapshot saved and events emitted.
	if raw, ok, err := st.LoadTopologyRaw(ctx); err != nil || !ok || len(raw) == 0 {
		t.Fatalf("expected persisted topology, got (%s, %v, %v)", raw, ok, err)
	}
}

func TestPollOnceUnknownDriverAndBackoff(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()

	sched := New(driver.NewRegistry(), st, nil, nil)
	// A target whose driver is unknown (say, misspelled after a restart) still goes offline.
	if _, err := st.SaveSnapshot(ctx, &driver.Snapshot{TargetID: "t1", Devices: []model.Device{{ID: "d1", Online: true}}}); err != nil {
		t.Fatal(err)
	}
	for range offlineAfterFailures {
		if err := sched.PollOnce(ctx, driver.TargetConfig{ID: "t1", Driver: "nonexistent"}); err == nil {
			t.Fatal("expected error for unknown driver")
		}
	}
	if d, ok, err := st.GetDevice(ctx, "d1"); err != nil || !ok || d.Online {
		t.Fatalf("device after unknown-driver failures = (%+v, %v, %v), want offline", d, ok, err)
	}
	if got := backoffDuration(10*time.Second, 0, time.Minute); got != 10*time.Second {
		t.Fatalf("backoff(0) = %v", got)
	}
	if got := backoffDuration(10*time.Second, 3, time.Minute); got != 40*time.Second {
		t.Fatalf("backoff(3) = %v", got)
	}
	if got := backoffDuration(10*time.Second, 10, time.Minute); got != time.Minute {
		t.Fatalf("backoff(10) = %v, want 1m cap", got)
	}
}

func TestFailingTargetGoesOffline(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer st.Close()

	drv := &mockDriver{
		kind: "mock",
		fn: func(_ context.Context, cfg driver.TargetConfig, n int32) (*driver.Snapshot, error) {
			if n > 1 {
				return nil, errors.New("dial tcp: connection refused")
			}
			return &driver.Snapshot{
				TargetID: cfg.ID,
				Devices:  []model.Device{{ID: "mock:sw1", Name: "sw1", TargetID: cfg.ID, Online: true}},
			}, nil
		},
	}
	reg := driver.NewRegistry()
	if err := reg.Register(drv); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	evCh, unsub := bus.Subscribe(16)
	defer unsub()
	sched := New(reg, st, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg := driver.TargetConfig{ID: "t1", Driver: "mock", Enabled: true}

	online := func() bool {
		d, ok, err := st.GetDevice(ctx, "mock:sw1")
		if err != nil || !ok {
			t.Fatalf("GetDevice: ok=%v err=%v", ok, err)
		}
		return d.Online
	}
	_ = sched.PollOnce(ctx, cfg)
	_ = sched.PollOnce(ctx, cfg) // first failure: still online
	if !online() {
		t.Fatal("device offline after a single failure")
	}
	_ = sched.PollOnce(ctx, cfg)
	if online() {
		t.Fatal("device still online after consecutive failures")
	}
	_ = sched.PollOnce(ctx, cfg) // past the threshold: idempotent, no second event
	state, err := st.LoadState(ctx)
	if err != nil || len(state.Devices) != 1 || state.Devices[0].Online {
		t.Fatalf("state devices = %+v err=%v", state.Devices, err)
	}
	offline := 0
	for done := false; !done; {
		select {
		case ev := <-evCh:
			if ev.Type == events.DeviceOffline {
				offline++
			}
		default:
			done = true
		}
	}
	if offline != 1 {
		t.Fatalf("device.offline events = %d, want 1", offline)
	}
}

func TestCancelledPollIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := driver.NewRegistry()
	_ = reg.Register(&mockDriver{kind: "mock", fn: func(c context.Context, _ driver.TargetConfig, _ int32) (*driver.Snapshot, error) {
		cancel()
		return nil, c.Err()
	}})
	sched := New(reg, st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = sched.PollOnce(ctx, driver.TargetConfig{ID: "t1", Driver: "mock", Enabled: true})
	if st := sched.Status()["t1"]; st.ConsecutiveFailures != 0 {
		t.Fatalf("cancellation recorded as failure: %+v", st)
	}
}

func TestFailedOfflineRecomputeIsRetried(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	drv := &mockDriver{kind: "mock", fn: func(_ context.Context, cfg driver.TargetConfig, n int32) (*driver.Snapshot, error) {
		if n > 1 {
			return nil, errors.New("dial tcp: connection refused")
		}
		return &driver.Snapshot{TargetID: cfg.ID, Devices: []model.Device{{ID: "mock:sw1", Online: true}}}, nil
	}}
	reg := driver.NewRegistry()
	_ = reg.Register(drv)
	sched := New(reg, st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg := driver.TargetConfig{ID: "t1", Driver: "mock", Enabled: true}
	_ = sched.PollOnce(ctx, cfg)

	// Another target's unreadable snapshot makes the offline recompute fail.
	if _, err := raw.Exec(`INSERT INTO target_snapshots (target_id, collected_at, snapshot_json) VALUES ('bad', 0, '{')`); err != nil {
		t.Fatal(err)
	}
	for range offlineAfterFailures {
		_ = sched.PollOnce(ctx, cfg)
	}
	if _, err := raw.Exec(`DELETE FROM target_snapshots WHERE target_id = 'bad'`); err != nil {
		t.Fatal(err)
	}
	_ = sched.PollOnce(ctx, cfg) // nothing new goes offline, but the stale graph must be re-solved

	topo, _, err := st.LoadTopologyRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Nodes []struct {
			ID     string `json:"id"`
			Online bool   `json:"online"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(topo, &g); err != nil || len(g.Nodes) != 1 || g.Nodes[0].Online {
		t.Fatalf("stored topology nodes = %+v (err %v), want sw1 offline", g.Nodes, err)
	}
}

type slowDriver struct{ mockDriver }

func (*slowDriver) MinPollInterval() time.Duration { return 30 * time.Second }

func TestPollIntervalHonoursDriverFloor(t *testing.T) {
	reg := driver.NewRegistry()
	_ = reg.Register(&slowDriver{mockDriver{kind: "slow"}})
	_ = reg.Register(&mockDriver{kind: "fast"})
	s := New(reg, nil, nil, nil)
	for _, tc := range []struct {
		drv  string
		cfg  time.Duration
		want time.Duration
	}{
		{"slow", 10 * time.Second, 30 * time.Second},
		{"slow", time.Minute, time.Minute},
		{"fast", 0, 15 * time.Second},
	} {
		if got := s.pollInterval(driver.TargetConfig{Driver: tc.drv, PollInterval: tc.cfg}); got != tc.want {
			t.Errorf("%s/%v: got %v, want %v", tc.drv, tc.cfg, got, tc.want)
		}
	}
}

func TestBackoffNeverBelowBase(t *testing.T) {
	if got := backoffDuration(3*time.Minute, 1, 2*time.Minute); got != 3*time.Minute {
		t.Fatalf("backoff(base 3m, cap 2m) = %v, want 3m", got)
	}
}

func TestUnavailableSectionWarnsOnce(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := driver.NewRegistry()
	_ = reg.Register(&mockDriver{kind: "mock", fn: func(_ context.Context, cfg driver.TargetConfig, _ int32) (*driver.Snapshot, error) {
		return &driver.Snapshot{TargetID: cfg.ID, Unavailable: []driver.Section{driver.SectionClients}}, nil
	}})
	var buf bytes.Buffer
	sched := New(reg, st, nil, slog.New(slog.NewTextHandler(&buf, nil)))
	cfg := driver.TargetConfig{ID: "t1", Driver: "mock", Enabled: true}
	for i := 0; i < 3; i++ {
		if err := sched.PollOnce(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(buf.String(), "stopped refreshing"); n != 1 {
		t.Fatalf("warnings = %d, want 1:\n%s", n, buf.String())
	}
}

func TestCarriedSectionDropWarns(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	reg := driver.NewRegistry()
	_ = reg.Register(&mockDriver{kind: "mock", fn: func(_ context.Context, cfg driver.TargetConfig, n int32) (*driver.Snapshot, error) {
		snap := &driver.Snapshot{TargetID: cfg.ID, CollectedAt: t0.Add(time.Duration(n-1) * 10 * time.Minute)}
		if n == 1 {
			snap.Clients = []model.Client{{MAC: "aa:aa:aa:aa:aa:01"}}
		} else {
			snap.Unavailable = []driver.Section{driver.SectionClients}
		}
		return snap, nil
	}})
	var buf bytes.Buffer
	sched := New(reg, st, nil, slog.New(slog.NewTextHandler(&buf, nil)))
	cfg := driver.TargetConfig{ID: "t1", Driver: "mock", Enabled: true}
	for i := 0; i < 4; i++ { // fresh, carried (+10m), carried (+20m within 15m? no: chain expires), ...
		if err := sched.PollOnce(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}
	out := buf.String()
	if strings.Count(out, "stopped refreshing") != 1 || !strings.Contains(out, "keeping_last_data=true") || strings.Count(out, "dropped its last data") != 1 {
		t.Fatalf("log:\n%s", out)
	}
}

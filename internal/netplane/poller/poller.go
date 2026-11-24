// SPDX-License-Identifier: AGPL-3.0-or-later
package poller

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/events"
	"github.com/andaru-project/andaru/internal/netplane/store"
	"github.com/andaru-project/andaru/internal/netplane/topology"
)

// TargetStatus captures runtime health for one polled target.
type TargetStatus struct {
	TargetID            string    `json:"target_id"`
	Driver              string    `json:"driver"`
	LastAttempt         time.Time `json:"last_attempt,omitempty"`
	LastSuccess         time.Time `json:"last_success,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// Scheduler runs concurrent isolated polling loops for configured targets.
type Scheduler struct {
	registry   *driver.Registry
	store      *store.Store
	bus        *events.Bus
	logger     *slog.Logger
	maxBackoff time.Duration

	mu     sync.RWMutex
	topoMu sync.Mutex
	// topoStale (guarded by topoMu) records a failed topology save, so the next offline
	// mark re-solves even when nothing new went offline.
	topoStale bool
	status    map[string]TargetStatus

	// pollLocks serializes polls of one target (background loop vs manual API poll), so a
	// failed attempt can never mark offline a snapshot that a concurrent poll just saved.
	pollLocks sync.Map // target ID -> *sync.Mutex
}

// New creates a Scheduler wired to the given Registry, Store, and EventBus.
func New(reg *driver.Registry, st *store.Store, bus *events.Bus, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		registry:   reg,
		store:      st,
		bus:        bus,
		logger:     logger,
		maxBackoff: 2 * time.Minute,
		status:     make(map[string]TargetStatus),
	}
}

// SetMaxBackoff overrides the maximum backoff cap on consecutive target failures.
func (s *Scheduler) SetMaxBackoff(d time.Duration) {
	if d > 0 {
		s.maxBackoff = d
	}
}

// Run starts polling loops for all enabled targets and blocks until ctx is cancelled
// and all worker goroutines have exited.
func (s *Scheduler) Run(ctx context.Context, targets []driver.TargetConfig) {
	var wg sync.WaitGroup
	for _, cfg := range targets {
		if !cfg.Enabled {
			continue
		}
		wg.Add(1)
		go func(c driver.TargetConfig) {
			defer wg.Done()
			s.runTarget(ctx, c)
		}(cfg)
	}
	wg.Wait()
}

// offlineAfterFailures is how many consecutive failed polls mark a target's devices offline;
// one failure alone is often a transient timeout.
const offlineAfterFailures = 2

// PollOnce executes a single synchronous poll cycle for cfg, persisting state,
// emitting diff events, and updating the solved topology snapshot.
func (s *Scheduler) PollOnce(ctx context.Context, cfg driver.TargetConfig) error {
	lock, _ := s.pollLocks.LoadOrStore(cfg.ID, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	now := time.Now().UTC()
	var snap *driver.Snapshot
	var err error
	if drv, ok := s.registry.Get(cfg.Driver); ok {
		snap, err = drv.Poll(ctx, cfg)
	} else {
		// A failure like any other: the target's retained devices must still go offline.
		err = fmt.Errorf("unknown driver %q", cfg.Driver)
	}
	if err != nil && ctx.Err() != nil {
		return err // shutdown or caller cancellation, not a target failure
	}
	if err != nil {
		// Retried on every failure past the threshold, so a failed mark is recovered; it
		// re-solves the topology when something went offline or a previous save failed.
		if s.recordError(cfg, now, err) >= offlineAfterFailures {
			s.markOffline(ctx, cfg, now)
		}
		return err
	}
	if snap.TargetID == "" {
		snap.TargetID = cfg.ID
	}
	if snap.CollectedAt.IsZero() {
		snap.CollectedAt = now
	}

	snap.PollInterval = s.pollInterval(cfg)
	prev, err := s.store.SaveSnapshot(ctx, snap)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		s.recordError(cfg, now, err)
		return err
	}

	s.recordSuccess(cfg, now)
	// Log when a section stops refreshing and when its carried data runs out, not on every
	// poll of a permanent failure.
	for _, sec := range snap.Unavailable {
		_, carried := snap.Stale[sec]
		switch {
		case prev == nil || !slices.Contains(prev.Unavailable, sec):
			s.logger.Warn("target section stopped refreshing", "target_id", cfg.ID, "section", sec, "keeping_last_data", carried)
		case !carried && hasKey(prev.Stale, sec):
			s.logger.Warn("target section still not refreshing; dropped its last data", "target_id", cfg.ID, "section", sec)
		}
	}

	if s.bus != nil {
		for _, ev := range events.DiffSnapshots(prev, snap) {
			s.bus.Publish(ev)
		}
	}

	s.recomputeTopology(ctx, cfg.ID)
	return nil
}

// IngestSnapshot persists a pre-built snapshot (such as an in-process local host observation)
// through the same per-target lock, diff/event publication, and topology recomputation path as PollOnce.
func (s *Scheduler) IngestSnapshot(ctx context.Context, snap *driver.Snapshot) error {
	if snap == nil || snap.TargetID == "" {
		return fmt.Errorf("snapshot target_id is required")
	}
	lock, _ := s.pollLocks.LoadOrStore(snap.TargetID, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	now := time.Now().UTC()
	if snap.CollectedAt.IsZero() {
		snap.CollectedAt = now
	}
	snap.Normalize()

	cfg := driver.TargetConfig{ID: snap.TargetID, Driver: "andaru"}
	prev, err := s.store.SaveSnapshot(ctx, snap)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		s.recordError(cfg, now, err)
		return fmt.Errorf("save snapshot %s: %w", snap.TargetID, err)
	}
	s.recordSuccess(cfg, now)

	if s.bus != nil {
		for _, ev := range events.DiffSnapshots(prev, snap) {
			s.bus.Publish(ev)
		}
	}
	s.recomputeTopology(ctx, snap.TargetID)
	return nil
}

// markOffline flags a failing target's devices and clients offline, publishes the resulting
// device.offline / client.left events, and re-solves the topology.
func (s *Scheduler) markOffline(ctx context.Context, cfg driver.TargetConfig, now time.Time) {
	prev, curr, err := s.store.MarkTargetOffline(ctx, cfg.ID, now)
	if err != nil {
		s.logger.Warn("mark target offline failed", "target_id", cfg.ID, "err", err)
		return
	}
	if curr == nil {
		return
	}
	evs := events.DiffSnapshots(prev, curr)
	if s.bus != nil {
		for _, ev := range evs {
			s.bus.Publish(ev)
		}
	}
	s.topoMu.Lock()
	stale := s.topoStale
	s.topoMu.Unlock()
	if len(evs) > 0 || stale {
		s.recomputeTopology(ctx, cfg.ID)
	}
}

func (s *Scheduler) recomputeTopology(ctx context.Context, targetID string) {
	// Serialize LoadState -> Solve -> SaveTopology so concurrent target pollers
	// never overwrite a fresher topology snapshot with stale state; the generation time is
	// taken under the lock so the saved generated_at never goes backwards.
	s.topoMu.Lock()
	now := time.Now().UTC()
	state, loadErr := s.store.LoadState(ctx)
	if loadErr != nil {
		s.topoStale = true
		s.topoMu.Unlock()
		s.logger.Warn("load state for topology failed", "target_id", targetID, "err", loadErr)
		return
	}
	graph := topology.Solve(state, now)
	saveErr := s.store.SaveTopology(ctx, now, graph)
	s.topoStale = saveErr != nil
	s.topoMu.Unlock()
	if saveErr != nil {
		s.logger.Warn("save topology failed", "target_id", targetID, "err", saveErr)
	} else if s.bus != nil {
		s.bus.Publish(events.Event{
			Type:      events.TopologyUpdated,
			Severity:  events.SeverityInfo,
			Timestamp: now,
			TargetID:  targetID,
			Summary:   fmt.Sprintf("Topology updated (%d nodes, %d edges)", len(graph.Nodes), len(graph.Edges)),
		})
	}
}

// Status returns a copy of all target polling statuses.
func (s *Scheduler) Status() map[string]TargetStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]TargetStatus, len(s.status))
	for k, v := range s.status {
		out[k] = v
	}
	return out
}

func (s *Scheduler) runTarget(ctx context.Context, cfg driver.TargetConfig) {
	baseInterval := s.pollInterval(cfg)

	failures := 0
	for {
		if err := s.PollOnce(ctx, cfg); err != nil {
			failures++
			s.logger.Warn("target poll failed", "target", cfg, "failures", failures, "err", err)
		} else {
			failures = 0
		}

		wait := backoffDuration(baseInterval, failures, s.maxBackoff)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// pollInterval is the target's interval (default 15s), raised to its driver's floor.
func (s *Scheduler) pollInterval(cfg driver.TargetConfig) time.Duration {
	d := cfg.PollInterval
	if d <= 0 {
		d = 15 * time.Second
	}
	if drv, ok := s.registry.Get(cfg.Driver); ok {
		if m, ok := drv.(driver.MinIntervalDriver); ok {
			d = max(d, m.MinPollInterval())
		}
	}
	return d
}

func backoffDuration(base time.Duration, failures int, capDur time.Duration) time.Duration {
	if failures <= 0 {
		return base
	}
	capDur = max(capDur, base) // never retry sooner than the driver's floor allows
	d := base
	for i := 1; i < failures && d < capDur; i++ {
		d *= 2
	}
	if d > capDur {
		return capDur
	}
	return d
}

func (s *Scheduler) recordSuccess(cfg driver.TargetConfig, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[cfg.ID] = TargetStatus{
		TargetID:            cfg.ID,
		Driver:              cfg.Driver,
		LastAttempt:         at,
		LastSuccess:         at,
		LastError:           "",
		ConsecutiveFailures: 0,
	}
}

// recordError records a failed attempt and returns the consecutive failure count.
func (s *Scheduler) recordError(cfg driver.TargetConfig, at time.Time, err error) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status[cfg.ID]
	st.TargetID = cfg.ID
	st.Driver = cfg.Driver
	st.LastAttempt = at
	st.LastError = err.Error()
	st.ConsecutiveFailures++
	s.status[cfg.ID] = st
	return st.ConsecutiveFailures
}

func hasKey[K comparable, V any](m map[K]V, k K) bool {
	_, ok := m[k]
	return ok
}

// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	npapi "github.com/andaru-project/andaru/internal/netplane/api"
	npdriver "github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/driver/andaru"
	"github.com/andaru-project/andaru/internal/netplane/driver/openwrt"
	"github.com/andaru-project/andaru/internal/netplane/driver/opnsense"
	"github.com/andaru-project/andaru/internal/netplane/driver/pfsense"
	"github.com/andaru-project/andaru/internal/netplane/driver/rtlplayground"
	"github.com/andaru-project/andaru/internal/netplane/driver/snmp"
	"github.com/andaru-project/andaru/internal/netplane/driver/unifi"
	npevents "github.com/andaru-project/andaru/internal/netplane/events"
	nppoller "github.com/andaru-project/andaru/internal/netplane/poller"
	npstore "github.com/andaru-project/andaru/internal/netplane/store"
)

const (
	fleetAPIPrefix       = "/api/v1/fleet"
	fleetLocalInterval   = 15 * time.Second
	minFleetPollInterval = 5 * time.Second
	maxFleetTargetBodySz = 64 * 1024
	maxFleetTargetName   = 128
)

var validFleetTargetID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// fleetUnavailableHandler returns 503 on /api/v1/fleet/* when the fleet store failed to initialize,
// so a corrupt fleet DB or secrets file never prevents the core firewall engine from starting.
func fleetUnavailableHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusServiceUnavailable, "FLEET_UNAVAILABLE", "Fleet service is unavailable; inspect the engine journal.")
	})
}

type fleetService struct {
	store   *npstore.Store
	reg     *npdriver.Registry
	bus     *npevents.Bus
	poller  *nppoller.Scheduler
	secrets *fleetSecrets
	local   localHostSource
	api     *npapi.Server
	logger  *slog.Logger

	mutateMu    sync.Mutex // serializes target upsert/delete across store, secrets, map, and poller
	mu          sync.Mutex
	targets     map[string]npdriver.TargetConfig
	runCtx      context.Context
	pollMu      sync.Mutex
	pollCancel  context.CancelFunc
	pollDone    chan struct{}
	localCancel context.CancelFunc
	localDone   chan struct{}
}

type fleetTargetRequest struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Driver       string            `json:"driver"`
	Endpoint     string            `json:"endpoint"`
	Enabled      *bool             `json:"enabled,omitempty"`
	PollInterval string            `json:"poll_interval,omitempty"`
	Settings     map[string]any    `json:"settings,omitempty"`
	Secrets      map[string]string `json:"secrets,omitempty"`
}

func openFleetService(ctx context.Context, stateDir string, local localHostSource, logger *slog.Logger) (*fleetService, error) {
	if logger == nil {
		logger = slog.Default()
	}
	secrets, err := openFleetSecrets(stateDir)
	if err != nil {
		return nil, err
	}
	st, err := npstore.Open(ctx, filepath.Join(stateDir, "netplane.db"))
	if err != nil {
		return nil, fmt.Errorf("open fleet store: %w", err)
	}

	reg := npdriver.NewRegistry()
	for _, d := range []npdriver.Driver{
		&andaru.Driver{LocalDiagnostics: newLocalDiagnostics().Run},
		openwrt.New(),
		opnsense.New(),
		pfsense.New(),
		rtlplayground.New(),
		snmp.New(),
		unifi.New(),
	} {
		_ = reg.Register(d)
	}

	bus := npevents.NewBus()
	sched := nppoller.New(reg, st, bus, logger)

	storedTargets, err := st.ListTargets(ctx)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("load fleet targets: %w", err)
	}

	targets := make(map[string]npdriver.TargetConfig, len(storedTargets))
	for _, t := range storedTargets {
		if t.ID == fleetLocalTargetID {
			continue
		}
		t.ResolvedSecrets = secrets.Get(t.ID)
		targets[t.ID] = t
	}

	fs := &fleetService{
		store:   st,
		reg:     reg,
		bus:     bus,
		poller:  sched,
		secrets: secrets,
		local:   local,
		logger:  logger,
		targets: targets,
	}
	fs.api = npapi.New(npapi.Options{
		Version:           buildVersion,
		Store:             st,
		Registry:          reg,
		Poller:            sched,
		Bus:               bus,
		Targets:           fs.allTargetConfigsLocked(),
		SkipBrowserGuards: true,
		Logger:            logger,
	})
	return fs, nil
}

func (f *fleetService) Start(ctx context.Context) {
	f.mu.Lock()
	f.runCtx = ctx
	localCtx, localCancel := context.WithCancel(ctx)
	f.localCancel = localCancel
	f.localDone = make(chan struct{})
	f.mu.Unlock()

	f.reloadPoller()

	go func() {
		defer close(f.localDone)
		if err := f.refreshLocal(localCtx); err != nil && localCtx.Err() == nil {
			f.logger.Warn("initial local host fleet snapshot failed", "error", err)
		}
		ticker := time.NewTicker(fleetLocalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-localCtx.Done():
				return
			case <-ticker.C:
				if err := f.refreshLocal(localCtx); err != nil && localCtx.Err() == nil {
					f.logger.Warn("local host fleet snapshot failed", "error", err)
				}
			}
		}
	}()
}

func (f *fleetService) Close() error {
	f.mu.Lock()
	if f.localCancel != nil {
		f.localCancel()
	}
	localDone := f.localDone
	f.mu.Unlock()

	f.pollMu.Lock()
	if f.pollCancel != nil {
		f.pollCancel()
	}
	pollDone := f.pollDone
	f.pollCancel = nil
	f.pollDone = nil
	f.pollMu.Unlock()

	if localDone != nil {
		<-localDone
	}
	if pollDone != nil {
		<-pollDone
	}
	return f.store.Close()
}

func (f *fleetService) reloadPoller() {
	f.pollMu.Lock()
	defer f.pollMu.Unlock()
	if f.pollCancel != nil {
		f.pollCancel()
		if f.pollDone != nil {
			<-f.pollDone
		}
		f.pollCancel = nil
		f.pollDone = nil
	}
	f.mu.Lock()
	runCtx := f.runCtx
	remote := f.remoteTargetConfigsLocked()
	f.mu.Unlock()
	if runCtx == nil || runCtx.Err() != nil || len(remote) == 0 {
		return
	}
	pollCtx, cancel := context.WithCancel(runCtx)
	done := make(chan struct{})
	f.pollCancel = cancel
	f.pollDone = done
	go func() {
		defer close(done)
		f.poller.Run(pollCtx, remote)
	}()
}

func (f *fleetService) remoteTargetConfigsLocked() []npdriver.TargetConfig {
	out := make([]npdriver.TargetConfig, 0, len(f.targets))
	for _, t := range f.targets {
		out = append(out, t)
	}
	return out
}

func (f *fleetService) allTargetConfigsLocked() []npdriver.TargetConfig {
	out := make([]npdriver.TargetConfig, 0, len(f.targets)+1)
	out = append(out, npdriver.TargetConfig{
		ID:           fleetLocalTargetID,
		Name:         "Local Andaru Host",
		Driver:       "andaru",
		Endpoint:     andaru.LocalEndpoint,
		Enabled:      true,
		PollInterval: fleetLocalInterval,
	})
	for _, t := range f.targets {
		out = append(out, t)
	}
	return out
}

func (f *fleetService) refreshLocal(ctx context.Context) error {
	snap, err := buildLocalSnapshot(ctx, f.local, time.Now().UTC())
	if err != nil {
		return err
	}
	snap.PollInterval = fleetLocalInterval
	return f.poller.IngestSnapshot(ctx, snap)
}

func (f *fleetService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, fleetAPIPrefix)
	if rel == r.URL.Path || rel == "" {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}

	switch {
	case rel == "/targets" && r.Method == http.MethodPost:
		f.handleUpsertTarget(w, r)
		return
	case strings.HasPrefix(rel, "/targets/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(rel, "/targets/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
			return
		}
		f.handleDeleteTarget(w, r, id)
		return
	case rel == "/targets/"+fleetLocalTargetID+"/poll" && r.Method == http.MethodPost:
		if err := f.refreshLocal(r.Context()); err != nil {
			writeError(w, r, http.StatusBadGateway, "POLL_FAILED", err.Error())
			return
		}
		writeJSON(w, r, http.StatusOK, map[string]any{"target_id": fleetLocalTargetID, "ok": true})
		return
	}

	// Delegate all read, remote-poll, diagnostics, and SSE endpoints to internal/netplane/api.Server.
	rewritten := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = "/api/v1" + rel
	urlCopy.RawPath = ""
	rewritten.URL = &urlCopy
	f.api.ServeHTTP(w, rewritten)
}

func normalizeTargetSecrets(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k != "" && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (f *fleetService) handleUpsertTarget(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxFleetTargetBodySz)
	var req fleetTargetRequest
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("Invalid target payload: %v", err))
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Unexpected trailing data in target payload.")
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	req.Name = strings.TrimSpace(req.Name)
	req.Driver = strings.TrimSpace(req.Driver)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if !validFleetTargetID.MatchString(req.ID) || req.ID == fleetLocalTargetID {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Target id must match [A-Za-z0-9._-]{1,64} and not be 'local'.")
		return
	}
	if len(req.Name) > maxFleetTargetName {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Target name exceeds 128 characters.")
		return
	}
	if req.Driver == "" || req.Endpoint == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Target driver and endpoint are required.")
		return
	}
	// Reject unix:// endpoints in engine-hosted mode: the engine runs as root and the local Andaru
	// host is already built in as target "local".
	if strings.HasPrefix(strings.ToLower(req.Endpoint), "unix://") {
		writeError(w, r, http.StatusBadRequest, "INVALID_TARGET", "Unix socket endpoints are not permitted for engine-hosted fleet targets.")
		return
	}

	pollInterval := 30 * time.Second
	if req.PollInterval != "" {
		d, err := time.ParseDuration(req.PollInterval)
		if err != nil || d < minFleetPollInterval {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("poll_interval must be at least %s.", minFleetPollInterval))
			return
		}
		pollInterval = d
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	f.mutateMu.Lock()
	defer f.mutateMu.Unlock()

	// Preserve existing secrets if caller omitted the secrets map on update; otherwise normalize
	// (dropping empty entries) before validation so runtime and post-restart behavior match.
	var resolvedSecrets map[string]string
	if req.Secrets == nil {
		resolvedSecrets = f.secrets.Get(req.ID)
	} else {
		resolvedSecrets = normalizeTargetSecrets(req.Secrets)
	}

	cfg := npdriver.TargetConfig{
		ID:              req.ID,
		Name:            req.Name,
		Driver:          req.Driver,
		Endpoint:        req.Endpoint,
		Enabled:         enabled,
		PollInterval:    pollInterval,
		Settings:        req.Settings,
		ResolvedSecrets: resolvedSecrets,
	}
	drv, ok := f.reg.Get(cfg.Driver)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "UNKNOWN_DRIVER", fmt.Sprintf("Unknown driver %q.", cfg.Driver))
		return
	}
	if err := drv.ValidateConfig(cfg); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TARGET", err.Error())
		return
	}

	opCtx := context.WithoutCancel(r.Context())
	if err := f.store.UpsertTarget(opCtx, cfg); err != nil {
		f.logger.ErrorContext(r.Context(), "persist fleet target failed", "target", cfg.ID, "error", err)
		writeError(w, r, http.StatusInternalServerError, "STORE_ERROR", "Failed to persist target.")
		return
	}
	if !cfg.Enabled {
		if err := f.store.ClearTargetState(opCtx, cfg.ID); err != nil {
			f.logger.ErrorContext(r.Context(), "clear disabled fleet target state failed", "target", cfg.ID, "error", err)
			writeError(w, r, http.StatusInternalServerError, "STORE_ERROR", "Failed to clear disabled target state.")
			return
		}
	}
	if req.Secrets != nil {
		if err := f.secrets.Set(cfg.ID, resolvedSecrets); err != nil {
			f.logger.ErrorContext(r.Context(), "persist fleet target secrets failed", "target", cfg.ID, "error", err)
			writeError(w, r, http.StatusInternalServerError, "SECRET_STORE_ERROR", "Failed to store target credentials.")
			return
		}
	}

	f.mu.Lock()
	f.targets[cfg.ID] = cfg
	f.api.SetTargets(f.allTargetConfigsLocked())
	f.mu.Unlock()
	f.reloadPoller()

	f.logger.InfoContext(r.Context(), "fleet target saved", "target", cfg.ID, "driver", cfg.Driver, "enabled", cfg.Enabled)
	writeJSON(w, r, http.StatusOK, cfg) // ResolvedSecrets is json:"-"
}

func (f *fleetService) handleDeleteTarget(w http.ResponseWriter, r *http.Request, id string) {
	if id == fleetLocalTargetID {
		writeError(w, r, http.StatusConflict, "TARGET_PROTECTED", "The built-in local Andaru host target cannot be deleted.")
		return
	}

	f.mutateMu.Lock()
	defer f.mutateMu.Unlock()

	f.mu.Lock()
	_, exists := f.targets[id]
	f.mu.Unlock()
	if !exists {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("Target %q not found.", id))
		return
	}

	// Persist deletion before mutating memory, using WithoutCancel so a client disconnect cannot
	// leave the row surviving after reporting success.
	opCtx := context.WithoutCancel(r.Context())
	if err := f.store.DeleteTarget(opCtx, id); err != nil {
		f.logger.ErrorContext(r.Context(), "delete fleet target failed", "target", id, "error", err)
		writeError(w, r, http.StatusInternalServerError, "STORE_ERROR", "Failed to remove target state.")
		return
	}
	if err := f.secrets.Delete(id); err != nil {
		f.logger.WarnContext(r.Context(), "delete fleet target secrets failed", "target", id, "error", err)
	}

	f.mu.Lock()
	delete(f.targets, id)
	f.api.SetTargets(f.allTargetConfigsLocked())
	f.mu.Unlock()
	f.reloadPoller()

	f.logger.InfoContext(r.Context(), "fleet target deleted", "target", id)
	writeJSON(w, r, http.StatusOK, map[string]any{"deleted": id})
}

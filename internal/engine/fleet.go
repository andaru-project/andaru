// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
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
	maxFleetTargetBodySz = 64 * 1024
)

type fleetService struct {
	store   *npstore.Store
	reg     *npdriver.Registry
	bus     *npevents.Bus
	poller  *nppoller.Scheduler
	secrets *fleetSecrets
	local   localHostSource
	api     *npapi.Server
	logger  *slog.Logger

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
		andaru.New(),
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
		Endpoint:     "local://engine",
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
	snap.Normalize()
	if _, err := f.store.SaveSnapshot(ctx, snap); err != nil {
		return err
	}
	return nil
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

func (f *fleetService) handleUpsertTarget(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxFleetTargetBodySz)
	var req fleetTargetRequest
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("Invalid target payload: %v", err))
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	req.Driver = strings.TrimSpace(req.Driver)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.ID == "" || strings.Contains(req.ID, "/") || req.ID == fleetLocalTargetID {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Target id must be non-empty, contain no '/', and not be 'local'.")
		return
	}
	if req.Driver == "" || req.Endpoint == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Target driver and endpoint are required.")
		return
	}

	pollInterval := 30 * time.Second
	if req.PollInterval != "" {
		d, err := time.ParseDuration(req.PollInterval)
		if err != nil || d <= 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid poll_interval duration.")
			return
		}
		pollInterval = d
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// Preserve existing secrets if caller omitted the secrets map on update.
	resolvedSecrets := req.Secrets
	if resolvedSecrets == nil {
		resolvedSecrets = f.secrets.Get(req.ID)
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

	if err := f.store.UpsertTarget(r.Context(), cfg); err != nil {
		f.logger.ErrorContext(r.Context(), "persist fleet target failed", "target", cfg.ID, "error", err)
		writeError(w, r, http.StatusInternalServerError, "STORE_ERROR", "Failed to persist target.")
		return
	}
	if req.Secrets != nil {
		if err := f.secrets.Set(cfg.ID, req.Secrets); err != nil {
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

	f.mu.Lock()
	if _, ok := f.targets[id]; !ok {
		f.mu.Unlock()
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("Target %q not found.", id))
		return
	}
	delete(f.targets, id)
	keep := map[string]bool{fleetLocalTargetID: true}
	for tid, t := range f.targets {
		if t.Enabled {
			keep[tid] = true
		}
	}
	f.api.SetTargets(f.allTargetConfigsLocked())
	f.mu.Unlock()
	f.reloadPoller()

	if err := f.secrets.Delete(id); err != nil {
		f.logger.WarnContext(r.Context(), "delete fleet target secrets failed", "target", id, "error", err)
	}
	if err := f.store.PruneTargets(r.Context(), keep); err != nil && !errors.Is(err, context.Canceled) {
		f.logger.ErrorContext(r.Context(), "prune deleted fleet target failed", "target", id, "error", err)
		writeError(w, r, http.StatusInternalServerError, "STORE_ERROR", "Failed to remove target state.")
		return
	}

	f.logger.InfoContext(r.Context(), "fleet target deleted", "target", id)
	writeJSON(w, r, http.StatusOK, map[string]any{"deleted": id})
}

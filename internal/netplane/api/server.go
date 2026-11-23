// SPDX-License-Identifier: AGPL-3.0-or-later
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/events"
	"github.com/andaru-project/andaru/internal/netplane/model"
	"github.com/andaru-project/andaru/internal/netplane/poller"
	"github.com/andaru-project/andaru/internal/netplane/store"
	"github.com/andaru-project/andaru/internal/netplane/topology"
	"github.com/andaru-project/andaru/internal/netplane/vlanflow"
)

//go:embed openapi.json
var openAPIJSON []byte

// Options configures the HTTP/SSE API server.
type Options struct {
	Version             string
	Store               *store.Store
	Registry            *driver.Registry
	Poller              *poller.Scheduler
	Bus                 *events.Bus
	Targets             []driver.TargetConfig
	APIToken            string
	TrustIngressProxy   bool
	TrustedIngressCIDRs []string
	AllowedHosts        []string // extra Host names accepted without an API token
	TrustedOrigins      []string // extra origins allowed to send writes, as browsers serialize them
	WebFS               fs.FS    // optional embedded SPA root containing index.html
	Logger              *slog.Logger
}

// DeviceDetail aggregates a device with all its associated observed entities.
type DeviceDetail struct {
	Device            model.Device             `json:"device"`
	Links             []model.Link             `json:"links"`
	LagGroups         []model.LagGroup         `json:"lag_groups"`
	Segments          []model.Segment          `json:"segments"`
	Zones             []model.Zone             `json:"zones"`
	Gateways          []model.GatewayStatus    `json:"gateways"`
	Routes            []model.Route            `json:"routes"`
	VPNs              []model.VpnTunnel        `json:"vpns"`
	Firewall          *model.FirewallState     `json:"firewall,omitempty"`
	Radios            []model.WirelessRadio    `json:"radios"`
	WirelessUplinks   []model.WirelessUplink   `json:"wireless_uplinks"`
	Clients           []model.Client           `json:"clients"`
	RecentDiagnostics []model.DiagnosticResult `json:"recent_diagnostics"`
}

// Server serves the netplane REST API, SSE stream, OpenAPI contract, and embedded SPA.
type Server struct {
	opts           Options
	mux            *http.ServeMux
	handler        http.Handler
	trustedIngress []netip.Prefix
	csrf           *http.CrossOriginProtection
	targets        map[string]driver.TargetConfig // read-only after New
}

// New constructs a Server ready to mount on an http.Server.
func New(opts Options) *Server {
	if opts.Version == "" {
		opts.Version = "0.1.0-dev"
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	s := &Server{
		opts:           opts,
		mux:            http.NewServeMux(),
		trustedIngress: parseTrustedCIDRs(opts.TrustedIngressCIDRs),
		csrf:           http.NewCrossOriginProtection(),
		targets:        make(map[string]driver.TargetConfig, len(opts.Targets)),
	}
	for _, t := range opts.Targets {
		s.targets[t.ID] = t
	}

	s.registerRoutes()
	s.opts.AllowedHosts = make([]string, len(opts.AllowedHosts)) // never alias the caller's slice
	for i, h := range opts.AllowedHosts {                        // config.Parse validates; also accept callers that skip it
		s.opts.AllowedHosts[i] = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	}
	for _, o := range opts.TrustedOrigins {
		if err := s.csrf.AddTrustedOrigin(o); err != nil {
			opts.Logger.Warn("ignoring invalid trusted origin", "origin", o, "err", err) // config.Parse validates
		}
	}
	s.handler = s.withBrowserGuards(s.withIngress(s.withAuth(s.mux)))
	return s
}

// ServeHTTP delegates to the wrapped middleware chain, bounding unread request bodies on every route
// without affecting body-free SSE streams.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
	}
	s.handler.ServeHTTP(w, r)
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPI)
	s.mux.HandleFunc("GET /api/v1/state", s.handleState)
	s.mux.HandleFunc("GET /api/v1/devices", s.handleListDevices)
	s.mux.HandleFunc("GET /api/v1/devices/{id}", s.handleGetDevice)
	s.mux.HandleFunc("GET /api/v1/devices/{id}/links/{ifName}/samples", s.handleLinkSamples)
	s.mux.HandleFunc("GET /api/v1/topology", s.handleTopology)
	s.mux.HandleFunc("GET /api/v1/vlans/flow", s.handleVLANFlow)
	s.mux.HandleFunc("GET /api/v1/clients", s.handleClients)
	s.mux.HandleFunc("GET /api/v1/targets", s.handleListTargets)
	s.mux.HandleFunc("POST /api/v1/targets/{id}/poll", s.handlePollTarget)
	s.mux.HandleFunc("GET /api/v1/diagnostics", s.handleListDiagnostics)
	s.mux.HandleFunc("POST /api/v1/diagnostics", s.handleRunDiagnostic)
	s.mux.HandleFunc("GET /api/v1/events/stream", s.handleEventStream)
	s.mux.HandleFunc("/", s.handleSPA)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	setJSONHeaders(w)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeRawJSON writes pre-encoded JSON with a 200 status.
func writeRawJSON(w http.ResponseWriter, raw []byte) {
	setJSONHeaders(w)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func setJSONHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
}

// filter returns the elements of xs that keep accepts, never nil (so JSON emits []).
func filter[T any](xs []T, keep func(T) bool) []T {
	out := []T{}
	for _, x := range xs {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": msg,
		},
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{"status": "ok", "version": s.opts.Version}
	// Per-target errors can name internal hosts, so unauthenticated callers get liveness only.
	if s.opts.Poller != nil && s.authenticated(r) {
		body["targets"] = s.opts.Poller.Status()
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	writeRawJSON(w, openAPIJSON)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	state, err := s.opts.Store.LoadState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	state, err := s.opts.Store.LoadState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": state.Devices})
}

func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state, err := s.opts.Store.LoadState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	var found *model.Device
	for i := range state.Devices {
		if state.Devices[i].ID == id {
			found = &state.Devices[i]
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("device %q not found", id))
		return
	}

	onDev := func(devID string) bool { return devID == id }
	detail := DeviceDetail{
		Device:            *found,
		Links:             filter(state.Links, func(l model.Link) bool { return onDev(l.DeviceID) }),
		LagGroups:         filter(state.LagGroups, func(g model.LagGroup) bool { return onDev(g.DeviceID) }),
		Segments:          filter(state.Segments, func(g model.Segment) bool { return onDev(g.DeviceID) }),
		Zones:             filter(state.Zones, func(z model.Zone) bool { return onDev(z.DeviceID) }),
		Gateways:          filter(state.Gateways, func(g model.GatewayStatus) bool { return onDev(g.DeviceID) }),
		Routes:            filter(state.Routes, func(rt model.Route) bool { return onDev(rt.DeviceID) }),
		VPNs:              filter(state.VPNs, func(v model.VpnTunnel) bool { return onDev(v.DeviceID) }),
		Radios:            filter(state.Radios, func(r model.WirelessRadio) bool { return onDev(r.DeviceID) }),
		WirelessUplinks:   filter(state.WirelessUplinks, func(u model.WirelessUplink) bool { return onDev(u.DeviceID) }),
		Clients:           filter(state.Clients, func(c model.Client) bool { return onDev(c.AttachedDeviceID) }),
		RecentDiagnostics: []model.DiagnosticResult{},
	}
	if i := slices.IndexFunc(state.Firewalls, func(f model.FirewallState) bool { return onDev(f.DeviceID) }); i >= 0 {
		detail.Firewall = &state.Firewalls[i]
	}
	if runs, err := s.opts.Store.ListDiagnosticRuns(r.Context(), id, 10); err == nil {
		detail.RecentDiagnostics = runs
	}

	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleLinkSamples(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	ifName := r.PathValue("ifName")
	since := time.Now().UTC().Add(-1 * time.Hour)
	if raw := r.URL.Query().Get("since"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			since = time.Now().UTC().Add(-d)
		} else if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			since = ts.UTC()
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_PARAM", "since must be a duration (e.g. 1h) or RFC3339 timestamp")
			return
		}
	}

	samples, err := s.opts.Store.QueryLinkSamples(r.Context(), deviceID, ifName, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": samples})
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	if raw, ok, err := s.opts.Store.LoadTopologyRaw(r.Context()); err == nil && ok {
		writeRawJSON(w, raw)
		return
	}
	state, err := s.opts.Store.LoadState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, topology.Solve(state, time.Now().UTC()))
}

func (s *Server) handleVLANFlow(w http.ResponseWriter, r *http.Request) {
	state, err := s.opts.Store.LoadState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vlanflow.Analyze(state, topology.Solve(state, time.Now().UTC())))
}

func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	state, err := s.opts.Store.LoadState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	onlineOnly := r.URL.Query().Get("online") == "true"
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	out := []model.Client{}
	for _, c := range state.Clients {
		if onlineOnly && !c.Online {
			continue
		}
		if q != "" {
			match := strings.Contains(strings.ToLower(c.MAC), q) ||
				strings.Contains(strings.ToLower(c.Hostname), q) ||
				strings.Contains(strings.ToLower(c.SSID), q)
			for _, ip := range c.IPs {
				if strings.Contains(strings.ToLower(ip), q) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleListTargets(w http.ResponseWriter, _ *http.Request) {
	items := make([]driver.TargetConfig, 0, len(s.targets))
	for _, t := range s.targets {
		items = append(items, t) // ResolvedSecrets is json:"-"
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handlePollTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, ok := s.targets[id]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("target %q not found", id))
		return
	}
	if !cfg.Enabled {
		// Startup pruned its stored state; a manual poll would bring the devices back.
		writeError(w, http.StatusConflict, "TARGET_DISABLED", fmt.Sprintf("target %q is disabled", id))
		return
	}
	if s.opts.Poller == nil {
		writeError(w, http.StatusServiceUnavailable, "POLLER_UNAVAILABLE", "poller not initialized")
		return
	}
	if err := s.opts.Poller.PollOnce(r.Context(), cfg); err != nil {
		writeError(w, http.StatusBadGateway, "POLL_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.opts.Poller.Status()[id])
}

func (s *Server) handleListDiagnostics(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	runs, err := s.opts.Store.ListDiagnosticRuns(r.Context(), deviceID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs})
}

func (s *Server) handleRunDiagnostic(w http.ResponseWriter, r *http.Request) {
	var req model.DiagnosticRequest
	// Bound the body read in time, not only in size. The deadline is cleared only once the whole
	// body is read: an expired one would cancel a long diagnostic's context (via the server's
	// background read), and a cleared one would let the server block discarding an unread body.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(bodyReadTimeout))
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	err := json.NewDecoder(body).Decode(&req)
	if err == nil {
		_, err = io.Copy(io.Discard, body)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	_ = rc.SetReadDeadline(time.Time{})
	if req.Kind == "" || strings.TrimSpace(req.Target) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "kind and target are required")
		return
	}
	if !req.Kind.Valid() {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("unknown diagnostic kind %q", req.Kind))
		return
	}

	started := time.Now().UTC()

	// Built-in cross-vendor fdb_lookup resolves any MAC or IP against merged state.
	if req.Kind == model.DiagFdbLookup {
		res, err := s.runFdbLookup(r.Context(), req, started)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DIAG_ERROR", err.Error())
			return
		}
		s.saveAndWriteDiagnostic(w, r, res)
		return
	}

	if req.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "device_id is required for device-side diagnostics")
		return
	}
	dev, ok, err := s.opts.Store.GetDevice(r.Context(), req.DeviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("device %q not found", req.DeviceID))
		return
	}
	cfg, hasTarget := s.targets[dev.TargetID]
	if !hasTarget {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("target %q for device %q not found", dev.TargetID, req.DeviceID))
		return
	}
	drv, hasDrv := s.opts.Registry.Get(cfg.Driver)
	if !hasDrv {
		writeError(w, http.StatusNotImplemented, "DRIVER_UNAVAILABLE", fmt.Sprintf("driver %q not registered", cfg.Driver))
		return
	}
	diagDrv, ok := drv.(driver.DiagnosticDriver)
	if !ok {
		writeError(w, http.StatusNotImplemented, "UNSUPPORTED", fmt.Sprintf("driver %q does not support %s", cfg.Driver, req.Kind))
		return
	}

	res, err := diagDrv.RunDiagnostic(r.Context(), cfg, req)
	switch {
	case errors.Is(err, driver.ErrDiagnosticRequest):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	case errors.Is(err, driver.ErrDiagnosticUnsupported):
		writeError(w, http.StatusNotImplemented, "UNSUPPORTED", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, "DIAG_FAILED", err.Error())
		return
	}
	if res.ID == "" {
		res.ID = fmt.Sprintf("diag-%d", started.UnixNano())
	}
	if res.StartedAt.IsZero() {
		res.StartedAt = started
	}
	if res.DurationMs == 0 {
		res.DurationMs = time.Since(started).Milliseconds()
	}
	s.saveAndWriteDiagnostic(w, r, res)
}

// bodyReadTimeout bounds how long a client may take to send a request body.
var bodyReadTimeout = 10 * time.Second

// saveAndWriteDiagnostic persists a finished run and returns it; the run is not repeated
// when the history write fails, so the caller sees the failure instead of an unsaved ID.
func (s *Server) saveAndWriteDiagnostic(w http.ResponseWriter, r *http.Request, res *model.DiagnosticResult) {
	if err := s.opts.Store.SaveDiagnosticRun(r.Context(), *res); err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "diagnostic ran but was not saved: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// sameIP compares two addresses by value (2001:db8::1 equals 2001:0db8:0::1), falling back
// to a case-insensitive text match for anything that does not parse.
func sameIP(a, b string) bool {
	pa, errA := netip.ParseAddr(a)
	pb, errB := netip.ParseAddr(b)
	if errA != nil || errB != nil {
		return strings.EqualFold(a, b)
	}
	return pa.Unmap() == pb.Unmap()
}

func (s *Server) runFdbLookup(ctx context.Context, req model.DiagnosticRequest, started time.Time) (*model.DiagnosticResult, error) {
	state, err := s.opts.Store.LoadState(ctx)
	if err != nil {
		return nil, err
	}

	query := strings.TrimSpace(req.Target)
	targetMAC, macErr := model.NormalizeMAC(query)
	matchedIPs := []string{}
	if macErr != nil {
		// Treat query as IP or hostname; resolve MAC from Neighbors, DHCP, or Clients.
		qLower := strings.ToLower(query)
		for _, n := range state.Neighbors {
			if sameIP(n.IP, query) && n.MAC != "" {
				targetMAC = n.MAC
				matchedIPs = append(matchedIPs, n.IP)
				break
			}
		}
		if targetMAC == "" {
			for _, l := range state.DHCP {
				if (sameIP(l.IP, query) || strings.EqualFold(l.Hostname, qLower)) && l.MAC != "" {
					targetMAC = l.MAC
					matchedIPs = append(matchedIPs, l.IP)
					break
				}
			}
		}
		if targetMAC == "" {
		clients:
			for _, c := range state.Clients {
				if strings.EqualFold(c.Hostname, qLower) {
					targetMAC = c.MAC
					matchedIPs = append(matchedIPs, c.IPs...)
					break
				}
				for _, ip := range c.IPs {
					if sameIP(ip, query) {
						targetMAC = c.MAC
						matchedIPs = append(matchedIPs, ip)
						break clients
					}
				}
			}
		}
	}

	fdbHits := []model.FdbEntry{}
	clientHits := []model.Client{}
	if targetMAC != "" {
		for _, f := range state.FDB {
			if f.MAC == targetMAC {
				fdbHits = append(fdbHits, f)
			}
		}
		for _, c := range state.Clients {
			if c.MAC == targetMAC {
				clientHits = append(clientHits, c)
			}
		}
	}

	found := targetMAC != "" && (len(fdbHits) > 0 || len(clientHits) > 0)
	summary := fmt.Sprintf("No FDB/client match found for %q", query)
	if found {
		summary = fmt.Sprintf("MAC %s matched %d FDB entries and %d client records", targetMAC, len(fdbHits), len(clientHits))
	}

	return &model.DiagnosticResult{
		ID:         fmt.Sprintf("diag-%d", started.UnixNano()),
		Kind:       model.DiagFdbLookup,
		DeviceID:   req.DeviceID,
		Target:     query,
		OK:         found,
		StartedAt:  started,
		DurationMs: time.Since(started).Milliseconds(),
		Summary:    summary,
		Structured: map[string]any{
			"mac":     targetMAC,
			"ips":     matchedIPs,
			"fdb":     fdbHits,
			"clients": clientHits,
		},
	}, nil
}

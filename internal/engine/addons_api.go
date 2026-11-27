package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

const (
	addonsAPIPrefix      = "/api/v1/addons"
	addonStageTimeout    = 10 * time.Minute
	addonControlTimeout  = 45 * time.Second
	addonStatusTimeout   = 3 * time.Second
	addonLogTimeout      = 5 * time.Second
	defaultAddonLogLines = 200
	maxAddonLogLines     = 1000
	maxAddonLogBytes     = 256 << 10
)

var addonLogCursorPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_;:=-]{0,255}$`)

type addonSnapshotReader interface {
	Snapshot(ctx context.Context) (config.SnapshotView, error)
}

type addonsAPI struct {
	catalog     *addon.Catalog
	store       addonSnapshotReader
	stateDir    string
	systemRoot  string
	origins     []string
	runner      realizeRunner
	coordinator *securityCoordinator
	readUnit    func(ctx context.Context, unit string) (activeState, subState string, err error)
	readJournal func(ctx context.Context, unit string, lines int, cursor string) ([]string, string, bool, error)

	mu      sync.Mutex
	staging map[string]bool
}

func newAddonsAPI(catalog *addon.Catalog, store addonSnapshotReader, stateDir, root string, origins []string, runner realizeRunner, coordinator *securityCoordinator) *addonsAPI {
	return &addonsAPI{
		catalog:     catalog,
		store:       store,
		stateDir:    stateDir,
		systemRoot:  root,
		origins:     slices.Clone(origins),
		runner:      runner,
		coordinator: coordinator,
		readUnit:    defaultReadSystemdUnit,
		readJournal: defaultReadSystemdJournal,
		staging:     map[string]bool{},
	}
}

type addonSavedEntry struct {
	ID         string                 `json:"id"`
	Enabled    bool                   `json:"enabled"`
	Settings   map[string]any         `json:"settings,omitempty"`
	Interfaces []addonSavedInterface  `json:"interfaces,omitempty"`
	BusGrants  addonSavedBusGrants    `json:"busGrants,omitzero"`
}

type addonSavedInterface struct {
	Name string `json:"name"`
	Zone string `json:"zone,omitempty"`
}

type addonSavedBusGrants struct {
	Publish   map[string]string `json:"publish,omitempty"`
	Subscribe map[string]string `json:"subscribe,omitempty"`
}

type addonStagedView struct {
	Present  bool   `json:"present"`
	Digest   string `json:"digest"`
	StagedAt string `json:"stagedAt,omitempty"`
	Error    string `json:"error,omitempty"`
}

type addonRuntimeView struct {
	Applied         bool   `json:"applied"`
	ActiveState     string `json:"activeState"`
	SubState        string `json:"subState,omitempty"`
	Health          string `json:"health"`
	AppliedImage    string `json:"appliedImage,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
}

type addonWebUIView struct {
	Title          addon.Text `json:"title"`
	Path           string     `json:"path"`
	Hostname       string     `json:"hostname,omitempty"`
	URL            string     `json:"url,omitempty"`
	OriginConflict bool       `json:"originConflict"`
	ConflictReason string     `json:"conflictReason,omitempty"`
}

type addonSummary struct {
	ID                 string            `json:"id"`
	Slug               string            `json:"slug"`
	Name               string            `json:"name"`
	Description        *addon.Text       `json:"description,omitempty"`
	Version            string            `json:"version"`
	Application        addon.Application `json:"application"`
	CatalogImage       string            `json:"catalogImage"`
	Installed          bool              `json:"installed"`
	HasWebUI           bool              `json:"hasWebUI"`
	UnreviewedBusCount int               `json:"unreviewedBusCount"`
	Staged             addonStagedView   `json:"staged"`
	Runtime            addonRuntimeView  `json:"runtime"`
	Saved              *addonSavedEntry  `json:"saved,omitempty"`
}

type addonListResponse struct {
	Items    []addonSummary            `json:"items"`
	SafeMode generation.SafeModeStatus `json:"safeMode"`
}

type addonDetailResponse struct {
	addonSummary
	SafeMode generation.SafeModeStatus `json:"safeMode"`
	Manifest *addon.Manifest           `json:"manifest"`
	WebUI    *addonWebUIView           `json:"webui,omitempty"`
}

type addonLogsResponse struct {
	Name      string   `json:"name"`
	Format    string   `json:"format"`
	Lines     []string `json:"lines"`
	Cursor    string   `json:"cursor,omitempty"`
	Truncated bool     `json:"truncated"`
}

func (a *addonsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == addonsAPIPrefix {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		if !noQuery(w, r) || !requireNoBody(w, r) {
			return
		}
		a.handleList(w, r)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, addonsAPIPrefix+"/")
	if !ok || rest == "" {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	parts := strings.Split(rest, "/")
	slug := parts[0]
	if !addon.ValidSlug(slug) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	switch {
	case len(parts) == 1:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		if !noQuery(w, r) || !requireNoBody(w, r) {
			return
		}
		a.handleDetail(w, r, slug)
	case len(parts) == 2 && parts[1] == "stage":
		switch r.Method {
		case http.MethodPost:
			if !noQuery(w, r) || !requireNoBody(w, r) {
				return
			}
			a.handleStage(w, r, slug)
		case http.MethodDelete:
			if !noQuery(w, r) || !requireNoBody(w, r) {
				return
			}
			a.handleUnstage(w, r, slug)
		default:
			methodNotAllowed(w, r, "POST, DELETE")
		}
	case len(parts) == 2 && (parts[1] == "start" || parts[1] == "stop" || parts[1] == "restart"):
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, "POST")
			return
		}
		if !noQuery(w, r) || !requireNoBody(w, r) {
			return
		}
		a.handleControl(w, r, slug, parts[1])
	case len(parts) == 3 && parts[1] == "logs" && parts[2] != "":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		if !requireNoBody(w, r) {
			return
		}
		a.handleLogs(w, r, slug, parts[2])
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
	}
}

func (a *addonsAPI) readSavedMap(ctx context.Context) (map[string]addonSavedEntry, []byte) {
	out := map[string]addonSavedEntry{}
	if a.store == nil {
		return out, nil
	}
	snap, err := a.store.Snapshot(ctx)
	if err != nil {
		if !errors.Is(err, config.ErrUpgradePending) {
			slog.WarnContext(ctx, "addons: read saved snapshot", "error", err)
		}
		return out, nil
	}
	var doc struct {
		Addons []addonSavedEntry `json:"addons"`
	}
	if err := json.Unmarshal(snap.Value, &doc); err != nil {
		slog.WarnContext(ctx, "addons: decode saved addons", "error", err)
		return out, snap.Value
	}
	for _, entry := range doc.Addons {
		out[entry.ID] = entry
	}
	return out, snap.Value
}

func (a *addonsAPI) readLiveAppliedImages() map[string]string {
	out := map[string]string{}
	if a.stateDir == "" {
		return out
	}
	dir := generation.Dir(a.stateDir)
	p, err := generation.ReadPointers(dir)
	if err != nil || p.Live == nil {
		return out
	}
	g, err := generation.Read(dir, *p.Live)
	if err != nil {
		return out
	}
	contents, err := generation.Contents(dir, g)
	if err != nil {
		return out
	}
	for path, data := range contents {
		rest, ok := strings.CutPrefix(path, addonQuadletDir+"andaru-addon-")
		if !ok {
			continue
		}
		slug, ok := strings.CutSuffix(rest, ".container")
		if !ok || !addon.ValidSlug(slug) {
			continue
		}
		img := ""
		for line := range strings.SplitSeq(string(data), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Image="); ok && v != "" {
				img = v
				break
			}
		}
		out[slug] = img
	}
	return out
}

func (a *addonsAPI) buildStaged(m *addon.Manifest, st addon.StagedState, stErr error) addonStagedView {
	v := addonStagedView{Digest: m.Runtime.Image}
	if stErr != nil {
		v.Error = stErr.Error()
		return v
	}
	if rec, ok := st.Images[m.Runtime.Image]; ok {
		v.Present = true
		if !rec.StagedAt.IsZero() {
			v.StagedAt = rec.StagedAt.UTC().Format(time.RFC3339)
		}
	}
	return v
}

func (a *addonsAPI) buildRuntime(ctx context.Context, m *addon.Manifest, appliedImages map[string]string, safeMode generation.SafeModeStatus) addonRuntimeView {
	appliedImg, applied := appliedImages[m.Slug]
	if !applied {
		return addonRuntimeView{
			Applied:     false,
			ActiveState: "not-applied",
			Health:      "none",
		}
	}
	v := addonRuntimeView{
		Applied:         true,
		AppliedImage:    appliedImg,
		UpdateAvailable: appliedImg != "" && appliedImg != m.Runtime.Image,
		ActiveState:     "unknown",
		Health:          "unknown",
	}
	readUnit := a.readUnit
	if readUnit == nil {
		readUnit = defaultReadSystemdUnit
	}
	unitCtx, cancel := context.WithTimeout(ctx, addonStatusTimeout)
	active, sub, err := readUnit(unitCtx, "andaru-addon-"+m.Slug+".service")
	cancel()
	if err == nil && active != "" {
		v.ActiveState = active
		v.SubState = sub
	}
	switch {
	case safeMode.Active && v.ActiveState != "active":
		v.Health = "held"
	case v.ActiveState == "active":
		v.Health = "healthy"
	case v.ActiveState == "activating" || v.ActiveState == "reloading":
		v.Health = "starting"
	case v.ActiveState == "failed":
		v.Health = "unhealthy"
	case v.ActiveState == "inactive" || v.ActiveState == "deactivating":
		v.Health = "stopped"
	}
	return v
}

func countUnreviewedBusGrants(m *addon.Manifest, entry addonSavedEntry, installed bool) int {
	if !installed || m.Bus == nil {
		return 0
	}
	count := 0
	for _, spec := range m.Bus.Publish {
		if st := entry.BusGrants.Publish[spec.Topic]; st != "granted" && st != "denied" {
			count++
		}
	}
	for _, spec := range m.Bus.Subscribe {
		if st := entry.BusGrants.Subscribe[spec.Topic]; st != "granted" && st != "denied" {
			count++
		}
	}
	return count
}

func (a *addonsAPI) buildSummary(
	ctx context.Context,
	m *addon.Manifest,
	canReadConfig bool,
	savedMap map[string]addonSavedEntry,
	st addon.StagedState,
	stErr error,
	appliedImages map[string]string,
	safeMode generation.SafeModeStatus,
) addonSummary {
	saved, installed := savedMap[m.ID]
	s := addonSummary{
		ID:                 m.ID,
		Slug:               m.Slug,
		Name:               m.Name,
		Description:        m.Description,
		Version:            m.Version,
		Application:        m.Application,
		CatalogImage:       m.Runtime.Image,
		Installed:          installed,
		HasWebUI:           m.WebUI != nil,
		UnreviewedBusCount: countUnreviewedBusGrants(m, saved, installed),
		Staged:             a.buildStaged(m, st, stErr),
		Runtime:            a.buildRuntime(ctx, m, appliedImages, safeMode),
	}
	if canReadConfig && installed {
		copyEntry := saved
		s.Saved = &copyEntry
	}
	return s
}

func (a *addonsAPI) buildWebUI(m *addon.Manifest, saved addonSavedEntry, installed bool, snapRaw []byte) *addonWebUIView {
	if m.WebUI == nil {
		return nil
	}
	view := &addonWebUIView{
		Title: m.WebUI.Title,
		Path:  m.WebUI.Path,
	}
	if !installed || m.WebUI.HostnameSetting == "" {
		return view
	}
	propName := strings.TrimPrefix(m.WebUI.HostnameSetting, "/")
	hostname, _ := saved.Settings[propName].(string)
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return view
	}
	view.Hostname = hostname
	if config.MatchesManagementOrigin(a.origins, snapRaw, hostname) {
		view.OriginConflict = true
		view.ConflictReason = "Configured WebUI hostname matches an Andaru management host and was suppressed to protect your session."
		return view
	}
	idx := slices.IndexFunc(m.Endpoints, func(ep addon.EndpointSpec) bool { return ep.Name == m.WebUI.Endpoint })
	if idx < 0 {
		return view
	}
	ep := m.Endpoints[idx]
	if ep.Protocol != "http" && ep.Protocol != "https" {
		return view
	}
	port, ok := m.EndpointPort(ep.Name, saved.Settings)
	if !ok {
		return view
	}
	host := hostname
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	authority := host
	if !(ep.Protocol == "https" && port == 443) && !(ep.Protocol == "http" && port == 80) {
		authority = net.JoinHostPort(strings.Trim(hostname, "[]"), strconv.Itoa(port))
	}
	view.URL = fmt.Sprintf("%s://%s%s", ep.Protocol, authority, m.WebUI.Path)
	return view
}

func (a *addonsAPI) handleList(w http.ResponseWriter, r *http.Request) {
	actor, _ := actorFromRequest(r)
	canReadConfig := hasCapability(actor, authz.ConfigurationRead)
	safeMode := generation.ReadSafeMode(a.systemRoot, a.stateDir)
	savedMap, _ := a.readSavedMap(r.Context())
	var st addon.StagedState
	var stErr error
	if a.stateDir != "" {
		st, stErr = addon.ReadStagedState(a.stateDir)
	}
	appliedImages := a.readLiveAppliedImages()
	items := []addonSummary{}
	if a.catalog != nil {
		for _, m := range a.catalog.All() {
			items = append(items, a.buildSummary(r.Context(), m, canReadConfig, savedMap, st, stErr, appliedImages, safeMode))
		}
	}
	writeJSON(w, r, http.StatusOK, addonListResponse{Items: items, SafeMode: safeMode})
}

func (a *addonsAPI) handleDetail(w http.ResponseWriter, r *http.Request, slug string) {
	if a.catalog == nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	m, ok := a.catalog.BySlug(slug)
	if !ok {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	actor, _ := actorFromRequest(r)
	canReadConfig := hasCapability(actor, authz.ConfigurationRead)
	safeMode := generation.ReadSafeMode(a.systemRoot, a.stateDir)
	savedMap, snapRaw := a.readSavedMap(r.Context())
	var st addon.StagedState
	var stErr error
	if a.stateDir != "" {
		st, stErr = addon.ReadStagedState(a.stateDir)
	}
	appliedImages := a.readLiveAppliedImages()
	summary := a.buildSummary(r.Context(), m, canReadConfig, savedMap, st, stErr, appliedImages, safeMode)
	saved, installed := savedMap[m.ID]
	writeJSON(w, r, http.StatusOK, addonDetailResponse{
		addonSummary: summary,
		SafeMode:     safeMode,
		Manifest:     m,
		WebUI:        a.buildWebUI(m, saved, installed, snapRaw),
	})
}

func (a *addonsAPI) claimStage(slug string) (release func(), ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.staging[slug] {
		return nil, false
	}
	a.staging[slug] = true
	return func() {
		a.mu.Lock()
		delete(a.staging, slug)
		a.mu.Unlock()
	}, true
}

func (a *addonsAPI) recordAudit(ctx context.Context, r *http.Request, action audit.Action, slug string, result audit.Result) {
	if a.coordinator == nil || a.coordinator.audits == nil {
		return
	}
	actor, ok := actorFromRequest(r)
	if !ok {
		return
	}
	if _, err := a.coordinator.audits.Append(ctx, audit.Event{
		Actor:    actor.Actor,
		Source:   actor.Source,
		Action:   action,
		Target:   slug,
		Result:   result,
		Category: audit.CategoryGeneral,
	}); err != nil {
		slog.WarnContext(ctx, "addons: append audit event", "action", action, "slug", slug, "error", err)
	}
}

func (a *addonsAPI) handleStage(w http.ResponseWriter, r *http.Request, slug string) {
	if a.catalog == nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	m, ok := a.catalog.BySlug(slug)
	if !ok {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if a.runner == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Add-on image staging is unavailable.")
		return
	}
	release, ok := a.claimStage(slug)
	if !ok {
		writeError(w, r, http.StatusConflict, "STAGE_IN_PROGRESS", "Image staging is already running for this add-on.")
		return
	}
	defer release()

	stageCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), addonStageTimeout)
	defer cancel()
	if err := a.runner.Realize(stageCtx, "addon-stage", 0, slug); err != nil {
		a.recordAudit(stageCtx, r, audit.ActionAddonStaged, slug, audit.ResultFailure)
		slog.WarnContext(r.Context(), "addons: stage image failed", "slug", slug, "error", err)
		writeError(w, r, http.StatusBadGateway, "ADDON_STAGE_FAILED", "Failed to pull and verify the add-on container image.")
		return
	}
	a.recordAudit(stageCtx, r, audit.ActionAddonStaged, slug, audit.ResultSuccess)
	var st addon.StagedState
	var stErr error
	if a.stateDir != "" {
		st, stErr = addon.ReadStagedState(a.stateDir)
	}
	writeJSON(w, r, http.StatusOK, a.buildStaged(m, st, stErr))
}

func (a *addonsAPI) handleUnstage(w http.ResponseWriter, r *http.Request, slug string) {
	if a.catalog == nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	m, ok := a.catalog.BySlug(slug)
	if !ok {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if a.runner == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Add-on image staging is unavailable.")
		return
	}
	release, ok := a.claimStage(slug)
	if !ok {
		writeError(w, r, http.StatusConflict, "STAGE_IN_PROGRESS", "Image staging is already running for this add-on.")
		return
	}
	defer release()

	// Refuse removing an image referenced by the saved head or retained generations.
	savedMap, _ := a.readSavedMap(r.Context())
	if _, installed := savedMap[m.ID]; installed {
		writeError(w, r, http.StatusConflict, "ADDON_IMAGE_IN_USE", "This add-on image is still referenced by the saved configuration.")
		return
	}
	if a.stateDir != "" {
		refs, err := generation.ReferencedAddonImages(a.stateDir)
		if err != nil {
			slog.WarnContext(r.Context(), "addons: read referenced images", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Could not verify generation image references.")
			return
		}
		if refs[m.Runtime.Image] {
			writeError(w, r, http.StatusConflict, "ADDON_IMAGE_IN_USE", "This add-on image is still referenced by a retained generation.")
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), addonControlTimeout)
	defer cancel()
	if err := a.runner.Realize(ctx, "addon-unstage", 0, slug); err != nil {
		a.recordAudit(ctx, r, audit.ActionAddonUnstaged, slug, audit.ResultFailure)
		if strings.Contains(err.Error(), "still referenced") {
			writeError(w, r, http.StatusConflict, "ADDON_IMAGE_IN_USE", "This add-on image is still referenced by a retained generation.")
			return
		}
		slog.WarnContext(r.Context(), "addons: unstage image failed", "slug", slug, "error", err)
		writeError(w, r, http.StatusBadGateway, "ADDON_UNSTAGE_FAILED", "Failed to remove the staged add-on container image.")
		return
	}
	a.recordAudit(ctx, r, audit.ActionAddonUnstaged, slug, audit.ResultSuccess)
	w.WriteHeader(http.StatusNoContent)
}

func (a *addonsAPI) handleControl(w http.ResponseWriter, r *http.Request, slug, action string) {
	if a.catalog == nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	m, ok := a.catalog.BySlug(slug)
	if !ok {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if a.runner == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Add-on lifecycle control is unavailable.")
		return
	}
	safeMode := generation.ReadSafeMode(a.systemRoot, a.stateDir)
	if safeMode.Active {
		writeError(w, r, http.StatusConflict, "SAFE_MODE_ACTIVE", "Add-ons cannot be controlled while safe mode or settings hold is active.")
		return
	}
	appliedImages := a.readLiveAppliedImages()
	if _, applied := appliedImages[slug]; !applied {
		writeError(w, r, http.StatusConflict, "ADDON_NOT_APPLIED", "This add-on is not enabled in the applied generation.")
		return
	}
	auditAction := map[string]audit.Action{
		"start":   audit.ActionAddonStarted,
		"stop":    audit.ActionAddonStopped,
		"restart": audit.ActionAddonRestarted,
	}[action]

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), addonControlTimeout)
	defer cancel()
	if err := a.runner.Realize(ctx, "addon-ctl", 0, action, slug); err != nil {
		a.recordAudit(ctx, r, auditAction, slug, audit.ResultFailure)
		if strings.Contains(err.Error(), "SAFE_MODE_ACTIVE") {
			writeError(w, r, http.StatusConflict, "SAFE_MODE_ACTIVE", "Add-ons cannot be controlled while safe mode or settings hold is active.")
			return
		}
		if strings.Contains(err.Error(), "not enabled in the applied generation") {
			writeError(w, r, http.StatusConflict, "ADDON_NOT_APPLIED", "This add-on is not enabled in the applied generation.")
			return
		}
		slog.WarnContext(r.Context(), "addons: lifecycle control failed", "action", action, "slug", slug, "error", err)
		writeError(w, r, http.StatusBadGateway, "ADDON_CONTROL_FAILED", "Failed to "+action+" add-on unit.")
		return
	}
	a.recordAudit(ctx, r, auditAction, slug, audit.ResultSuccess)
	writeJSON(w, r, http.StatusOK, a.buildRuntime(r.Context(), m, appliedImages, generation.ReadSafeMode(a.systemRoot, a.stateDir)))
}

func (a *addonsAPI) handleLogs(w http.ResponseWriter, r *http.Request, slug, logName string) {
	if a.catalog == nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	m, ok := a.catalog.BySlug(slug)
	if !ok {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	idx := slices.IndexFunc(m.Logs, func(l addon.LogSpec) bool { return l.Name == logName })
	if idx < 0 {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	spec := m.Logs[idx]
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Malformed query string.")
		return
	}
	for k, vals := range q {
		if (k != "lines" && k != "cursor") || len(vals) > 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Unsupported query parameter.")
			return
		}
	}
	lines := defaultAddonLogLines
	if raw := q.Get("lines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxAddonLogLines {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("lines must be an integer between 1 and %d.", maxAddonLogLines))
			return
		}
		lines = n
	}
	cursor := q.Get("cursor")
	if cursor != "" {
		if spec.Source.Type != "runtime" || !addonLogCursorPattern.MatchString(cursor) {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid log cursor.")
			return
		}
	}
	format := spec.Format
	if format == "" {
		format = "text"
	}
	switch spec.Source.Type {
	case "runtime":
		readJournal := a.readJournal
		if readJournal == nil {
			readJournal = defaultReadSystemdJournal
		}
		jCtx, cancel := context.WithTimeout(r.Context(), addonLogTimeout)
		entries, nextCursor, truncated, err := readJournal(jCtx, "andaru-addon-"+slug+".service", lines, cursor)
		cancel()
		if err != nil {
			slog.WarnContext(r.Context(), "addons: read runtime journal", "slug", slug, "log", logName, "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "LOGS_UNAVAILABLE", "Add-on logs are temporarily unavailable.")
			return
		}
		if entries == nil {
			entries = []string{}
		}
		writeJSON(w, r, http.StatusOK, addonLogsResponse{
			Name:      spec.Name,
			Format:    format,
			Lines:     entries,
			Cursor:    nextCursor,
			Truncated: truncated,
		})
	case "file":
		entries, truncated, err := readBoundedAddonLogFile(a.stateDir, slug, spec.Source.Storage, spec.Source.Path, lines)
		if err != nil {
			slog.WarnContext(r.Context(), "addons: read file log", "slug", slug, "log", logName, "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "LOGS_UNAVAILABLE", "Add-on log file is unavailable.")
			return
		}
		writeJSON(w, r, http.StatusOK, addonLogsResponse{
			Name:      spec.Name,
			Format:    format,
			Lines:     entries,
			Truncated: truncated,
		})
	default:
		writeError(w, r, http.StatusServiceUnavailable, "LOGS_UNAVAILABLE", "Unsupported log source type.")
	}
}

func readBoundedAddonLogFile(stateDir, slug, storage, relPath string, maxLines int) ([]string, bool, error) {
	if stateDir == "" || !addon.ValidSlug(slug) || !filepath.IsLocal(relPath) {
		return nil, false, errors.New("invalid log file path")
	}
	storageDir := filepath.Join(stateDir, "addons", slug, storage)
	root, err := os.OpenRoot(storageDir)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	f, err := root.Open(relPath)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("log path is not a regular file")
	}
	truncated := false
	size := info.Size()
	if size > maxAddonLogBytes {
		if _, err := f.Seek(size-maxAddonLogBytes, io.SeekStart); err != nil {
			return nil, false, err
		}
		truncated = true
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxAddonLogBytes))
	if err != nil {
		return nil, false, err
	}
	if truncated {
		// Drop the leading partial line after seeking into the middle of the file.
		if nl := bytes.IndexByte(raw, '\n'); nl >= 0 {
			raw = raw[nl+1:]
		}
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return []string{}, truncated, nil
	}
	all := strings.Split(text, "\n")
	if len(all) > maxLines {
		all = all[len(all)-maxLines:]
		truncated = true
	}
	return all, truncated, nil
}

func defaultReadSystemdUnit(ctx context.Context, unit string) (string, string, error) {
	out, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "-p", "ActiveState", "-p", "SubState", "--value", unit).Output()
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	active := ""
	sub := ""
	if len(lines) >= 1 {
		active = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 2 {
		sub = strings.TrimSpace(lines[1])
	}
	return active, sub, nil
}

func defaultReadSystemdJournal(ctx context.Context, unit string, lines int, cursor string) ([]string, string, bool, error) {
	args := []string{"--no-pager", "--output=short-iso", "--show-cursor", "-n", strconv.Itoa(lines), "-u", unit}
	if cursor != "" {
		args = append(args, "--after-cursor="+cursor)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/journalctl", args...)
	var buf bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &limitedBuffer{buf: &buf, limit: maxAddonLogBytes + 4096}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// journalctl exits 0 even with empty matches, so non-zero is a real failure.
		return nil, "", false, fmt.Errorf("journalctl: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	raw := strings.TrimRight(buf.String(), "\n")
	if raw == "" {
		return []string{}, "", false, nil
	}
	truncated := buf.Len() > maxAddonLogBytes
	if truncated {
		raw = raw[:maxAddonLogBytes]
	}
	all := strings.Split(raw, "\n")
	nextCursor := ""
	if len(all) > 0 {
		if c, ok := strings.CutPrefix(all[len(all)-1], "-- cursor: "); ok {
			nextCursor = strings.TrimSpace(c)
			all = all[:len(all)-1]
		}
	}
	// journalctl prints "-- No entries --" when empty.
	if len(all) == 1 && all[0] == "-- No entries --" {
		all = []string{}
	}
	return all, nextCursor, truncated, nil
}

type limitedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	rem := l.limit - l.buf.Len()
	if rem > 0 {
		if len(p) <= rem {
			l.buf.Write(p)
		} else {
			l.buf.Write(p[:rem])
		}
	}
	return len(p), nil
}

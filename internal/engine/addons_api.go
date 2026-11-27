package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"unicode/utf8"

	"golang.org/x/sys/unix"

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
	addonAuditTimeout    = 5 * time.Second
	defaultAddonLogLines = 200
	maxAddonLogLines     = 1000
	maxAddonLogBytes     = 256 << 10
	maxAddonStderrBytes  = 4 << 10
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

	mu       sync.Mutex
	staging  map[string]bool
	stageErr map[string]string
	stageSeq uint64
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
		stageErr:    map[string]string{},
	}
}

type addonSavedEntry struct {
	ID         string                `json:"id"`
	Enabled    bool                  `json:"enabled"`
	Settings   map[string]any        `json:"settings,omitempty"`
	Interfaces []addonSavedInterface `json:"interfaces,omitempty"`
	BusGrants  addonSavedBusGrants   `json:"busGrants,omitzero"`
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
	Staging  bool   `json:"staging"`
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
		m, ok := a.lookupManifest(w, r, slug)
		if !ok {
			return
		}
		a.handleDetail(w, r, m)
	case len(parts) == 2 && parts[1] == "stage":
		switch r.Method {
		case http.MethodPost:
			if !noQuery(w, r) || !requireNoBody(w, r) {
				return
			}
			m, ok := a.lookupManifest(w, r, slug)
			if !ok {
				return
			}
			a.handleStage(w, r, m)
		case http.MethodDelete:
			if !noQuery(w, r) || !requireNoBody(w, r) {
				return
			}
			m, ok := a.lookupManifest(w, r, slug)
			if !ok {
				return
			}
			a.handleUnstage(w, r, m)
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
		m, ok := a.lookupManifest(w, r, slug)
		if !ok {
			return
		}
		a.handleControl(w, r, m, parts[1])
	case len(parts) == 3 && parts[1] == "logs" && parts[2] != "":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		if !requireNoBody(w, r) {
			return
		}
		m, ok := a.lookupManifest(w, r, slug)
		if !ok {
			return
		}
		a.handleLogs(w, r, m, parts[2])
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
	}
}

func (a *addonsAPI) lookupManifest(w http.ResponseWriter, r *http.Request, slug string) (*addon.Manifest, bool) {
	if a.catalog == nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return nil, false
	}
	m, ok := a.catalog.BySlug(slug)
	if !ok {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return nil, false
	}
	return m, true
}

func (a *addonsAPI) readSavedMap(ctx context.Context) (map[string]addonSavedEntry, []byte, error) {
	out := map[string]addonSavedEntry{}
	if a.store == nil {
		return out, nil, nil
	}
	snap, err := a.store.Snapshot(ctx)
	if err != nil {
		return out, nil, err
	}
	var doc struct {
		Addons []addonSavedEntry `json:"addons"`
	}
	if err := json.Unmarshal(snap.Value, &doc); err != nil {
		return out, snap.Value, err
	}
	for _, entry := range doc.Addons {
		out[entry.ID] = entry
	}
	return out, snap.Value, nil
}

func (a *addonsAPI) readStagedState() (addon.StagedState, error) {
	if a.stateDir == "" {
		return addon.StagedState{}, nil
	}
	return addon.ReadStagedState(a.stateDir)
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

func (a *addonsAPI) buildStaged(ctx context.Context, m *addon.Manifest, st addon.StagedState, stErr error, staging bool, lastErr string) addonStagedView {
	v := addonStagedView{
		Digest:  m.Runtime.Image,
		Staging: staging,
	}
	if stErr != nil {
		slog.WarnContext(ctx, "addons: read staged state", "slug", m.Slug, "error", stErr)
		v.Error = "Staged image state is unavailable."
		return v
	}
	if rec, ok := st.Images[m.Runtime.Image]; ok {
		v.Present = true
		if !rec.StagedAt.IsZero() {
			v.StagedAt = rec.StagedAt.UTC().Format(time.RFC3339)
		}
	}
	if !staging && !v.Present && lastErr != "" {
		v.Error = lastErr
	}
	return v
}

type stagedSnapshot struct {
	state    addon.StagedState
	err      error
	staging  map[string]bool
	stageErr map[string]string
}

// readStagedSnapshot reads staged.json and the in-memory staging map consistently: if a background
// stage completes while staged.json is being read, staged.json is re-read once so callers never
// observe staging=false with stale present=false.
func (a *addonsAPI) readStagedSnapshot() stagedSnapshot {
	for range 2 {
		a.mu.Lock()
		seqBefore := a.stageSeq
		a.mu.Unlock()

		st, stErr := a.readStagedState()

		a.mu.Lock()
		seqAfter := a.stageSeq
		if seqAfter == seqBefore {
			snap := stagedSnapshot{
				state:    st,
				err:      stErr,
				staging:  make(map[string]bool, len(a.staging)),
				stageErr: make(map[string]string, len(a.stageErr)),
			}
			for k, v := range a.staging {
				snap.staging[k] = v
			}
			for k, v := range a.stageErr {
				snap.stageErr[k] = v
			}
			a.mu.Unlock()
			return snap
		}
		a.mu.Unlock()
	}
	st, stErr := a.readStagedState()
	a.mu.Lock()
	snap := stagedSnapshot{
		state:    st,
		err:      stErr,
		staging:  make(map[string]bool, len(a.staging)),
		stageErr: make(map[string]string, len(a.stageErr)),
	}
	for k, v := range a.staging {
		snap.staging[k] = v
	}
	for k, v := range a.stageErr {
		snap.stageErr[k] = v
	}
	a.mu.Unlock()
	return snap
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
		v.Health = "running"
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
	snap stagedSnapshot,
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
		Staged:             a.buildStaged(ctx, m, snap.state, snap.err, snap.staging[m.Slug], snap.stageErr[m.Slug]),
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
	if !addon.ValidHostname(hostname) {
		view.OriginConflict = true
		view.ConflictReason = "Configured WebUI hostname is invalid and was suppressed."
		return view
	}
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
	authority := hostname
	if !(ep.Protocol == "https" && port == 443) && !(ep.Protocol == "http" && port == 80) {
		authority = net.JoinHostPort(hostname, strconv.Itoa(port))
	}
	assembled := fmt.Sprintf("%s://%s%s", ep.Protocol, authority, m.WebUI.Path)
	parsed, err := url.Parse(assembled)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		view.OriginConflict = true
		view.ConflictReason = "Configured WebUI URL is invalid and was suppressed."
		return view
	}
	if config.MatchesManagementOrigin(a.origins, snapRaw, parsed.Hostname()) {
		view.OriginConflict = true
		view.ConflictReason = "Configured WebUI hostname matches an Andaru management host and was suppressed to protect your session."
		return view
	}
	view.URL = parsed.String()
	return view
}

func (a *addonsAPI) handleList(w http.ResponseWriter, r *http.Request) {
	actor, _ := actorFromRequest(r)
	canReadConfig := hasCapability(actor, authz.ConfigurationRead)
	safeMode := generation.ReadSafeMode(a.systemRoot, a.stateDir)
	savedMap, _, err := a.readSavedMap(r.Context())
	if err != nil && !errors.Is(err, config.ErrUpgradePending) {
		slog.WarnContext(r.Context(), "addons: read saved snapshot", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Add-on configuration state is temporarily unavailable.")
		return
	}
	snap := a.readStagedSnapshot()
	appliedImages := a.readLiveAppliedImages()
	items := []addonSummary{}
	if a.catalog != nil {
		for _, m := range a.catalog.All() {
			items = append(items, a.buildSummary(r.Context(), m, canReadConfig, savedMap, snap, appliedImages, safeMode))
		}
	}
	writeJSON(w, r, http.StatusOK, addonListResponse{Items: items, SafeMode: safeMode})
}

func (a *addonsAPI) handleDetail(w http.ResponseWriter, r *http.Request, m *addon.Manifest) {
	actor, _ := actorFromRequest(r)
	canReadConfig := hasCapability(actor, authz.ConfigurationRead)
	safeMode := generation.ReadSafeMode(a.systemRoot, a.stateDir)
	savedMap, snapRaw, err := a.readSavedMap(r.Context())
	if err != nil && !errors.Is(err, config.ErrUpgradePending) {
		slog.WarnContext(r.Context(), "addons: read saved snapshot", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Add-on configuration state is temporarily unavailable.")
		return
	}
	snap := a.readStagedSnapshot()
	appliedImages := a.readLiveAppliedImages()
	summary := a.buildSummary(r.Context(), m, canReadConfig, savedMap, snap, appliedImages, safeMode)
	saved, installed := savedMap[m.ID]
	writeJSON(w, r, http.StatusOK, addonDetailResponse{
		addonSummary: summary,
		SafeMode:     safeMode,
		Manifest:     m,
		WebUI:        a.buildWebUI(m, saved, installed, snapRaw),
	})
}

func (a *addonsAPI) claimStage(slug string) (release func(stageErrMsg string), ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.staging[slug] {
		return nil, false
	}
	a.staging[slug] = true
	delete(a.stageErr, slug)
	a.stageSeq++
	return func(stageErrMsg string) {
		a.mu.Lock()
		delete(a.staging, slug)
		if stageErrMsg != "" {
			a.stageErr[slug] = stageErrMsg
		} else {
			delete(a.stageErr, slug)
		}
		a.stageSeq++
		a.mu.Unlock()
	}, true
}

// beginAddonAudit persists a durable audit intent before invoking the privileged realizer, failing closed
// with 503 AUDIT_UNAVAILABLE if audit storage cannot commit the intent. The returned finish callback records
// the outcome with a fresh timeout context so canceled or timed-out requests never drop their outcome.
func (a *addonsAPI) beginAddonAudit(w http.ResponseWriter, r *http.Request, action audit.Action, slug string) (finish func(audit.Result), ok bool) {
	if a.coordinator == nil || a.coordinator.audits == nil {
		return func(audit.Result) {}, true
	}
	actor, hasActor := actorFromRequest(r)
	if !hasActor {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return nil, false
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Security audit storage is unavailable.")
		return nil, false
	}
	opID := "addon-" + hex.EncodeToString(rnd[:])
	sum := sha256.Sum256([]byte(string(action) + "\x00" + slug))
	if _, err := a.coordinator.audits.Begin(r.Context(), audit.Intent{
		OperationID: opID,
		Actor:       actor.Actor,
		Source:      actor.Source,
		Action:      action,
		Target:      slug,
		RequestHash: hex.EncodeToString(sum[:]),
		Category:    audit.CategoryGeneral,
	}); err != nil {
		slog.WarnContext(r.Context(), "addons: begin audit intent", "action", action, "slug", slug, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Security audit storage is unavailable.")
		return nil, false
	}
	return func(result audit.Result) {
		auditCtx, cancel := context.WithTimeout(context.Background(), addonAuditTimeout)
		defer cancel()
		if err := a.coordinator.audits.Finish(auditCtx, audit.Outcome{
			OperationID: opID,
			Result:      result,
			Target:      slug,
		}); err != nil {
			slog.Warn("addons: finish audit outcome", "action", action, "slug", slug, "operationId", opID, "error", err)
		}
	}, true
}

// handleStage runs `addon-stage` asynchronously so slow image downloads never hit the 180s web
// proxy timeout or orphan the staging lock when a browser disconnects.
func (a *addonsAPI) handleStage(w http.ResponseWriter, r *http.Request, m *addon.Manifest) {
	if a.runner == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Add-on image staging is unavailable.")
		return
	}
	release, ok := a.claimStage(m.Slug)
	if !ok {
		writeError(w, r, http.StatusConflict, "STAGE_IN_PROGRESS", "Image staging is already running for this add-on.")
		return
	}
	finishAudit, ok := a.beginAddonAudit(w, r, audit.ActionAddonStaged, m.Slug)
	if !ok {
		release("")
		return
	}
	go func() {
		stageCtx, cancel := context.WithTimeout(context.Background(), addonStageTimeout)
		defer cancel()
		if err := a.runner.Realize(stageCtx, "addon-stage", 0, m.Slug); err != nil {
			finishAudit(audit.ResultFailure)
			slog.Warn("addons: stage image failed", "slug", m.Slug, "error", err)
			release("Failed to pull and verify the add-on container image.")
			return
		}
		finishAudit(audit.ResultSuccess)
		release("")
	}()
	snap := a.readStagedSnapshot()
	writeJSON(w, r, http.StatusAccepted, a.buildStaged(r.Context(), m, snap.state, snap.err, snap.staging[m.Slug], snap.stageErr[m.Slug]))
}

func (a *addonsAPI) handleUnstage(w http.ResponseWriter, r *http.Request, m *addon.Manifest) {
	if a.runner == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Add-on image staging is unavailable.")
		return
	}
	release, ok := a.claimStage(m.Slug)
	if !ok {
		writeError(w, r, http.StatusConflict, "STAGE_IN_PROGRESS", "Image staging is already running for this add-on.")
		return
	}
	defer release("")

	// Fail closed if saved configuration cannot be verified, and block removing an image
	// referenced by any saved add-on sharing this digest or by any retained generation.
	savedMap, _, err := a.readSavedMap(r.Context())
	if err != nil {
		if errors.Is(err, config.ErrUpgradePending) {
			writeError(w, r, http.StatusConflict, "ADDON_IMAGE_IN_USE", "Saved configuration awaits upgrade decisions; staged images cannot be removed yet.")
			return
		}
		slog.WarnContext(r.Context(), "addons: read saved snapshot for unstage", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "ADDON_UNAVAILABLE", "Could not verify saved add-on configuration.")
		return
	}
	if _, installed := savedMap[m.ID]; installed {
		writeError(w, r, http.StatusConflict, "ADDON_IMAGE_IN_USE", "This add-on image is still referenced by the saved configuration.")
		return
	}
	if a.catalog != nil {
		for _, other := range a.catalog.All() {
			if other.Runtime.Image == m.Runtime.Image {
				if _, installed := savedMap[other.ID]; installed {
					writeError(w, r, http.StatusConflict, "ADDON_IMAGE_IN_USE", "This add-on image is still referenced by the saved configuration.")
					return
				}
			}
		}
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

	finishAudit, ok := a.beginAddonAudit(w, r, audit.ActionAddonUnstaged, m.Slug)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), addonControlTimeout)
	defer cancel()
	if err := a.runner.Realize(ctx, "addon-unstage", 0, m.Slug); err != nil {
		finishAudit(audit.ResultFailure)
		if strings.Contains(err.Error(), "still referenced") {
			writeError(w, r, http.StatusConflict, "ADDON_IMAGE_IN_USE", "This add-on image is still referenced by a retained generation.")
			return
		}
		slog.WarnContext(r.Context(), "addons: unstage image failed", "slug", m.Slug, "error", err)
		writeError(w, r, http.StatusBadGateway, "ADDON_UNSTAGE_FAILED", "Failed to remove the staged add-on container image.")
		return
	}
	finishAudit(audit.ResultSuccess)
	w.WriteHeader(http.StatusNoContent)
}

func (a *addonsAPI) handleControl(w http.ResponseWriter, r *http.Request, m *addon.Manifest, action string) {
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
	if _, applied := appliedImages[m.Slug]; !applied {
		writeError(w, r, http.StatusConflict, "ADDON_NOT_APPLIED", "This add-on is not enabled in the applied generation.")
		return
	}
	auditAction := map[string]audit.Action{
		"start":   audit.ActionAddonStarted,
		"stop":    audit.ActionAddonStopped,
		"restart": audit.ActionAddonRestarted,
	}[action]

	finishAudit, ok := a.beginAddonAudit(w, r, auditAction, m.Slug)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), addonControlTimeout)
	defer cancel()
	if err := a.runner.Realize(ctx, "addon-ctl", 0, action, m.Slug); err != nil {
		finishAudit(audit.ResultFailure)
		if strings.Contains(err.Error(), "SAFE_MODE_ACTIVE") {
			writeError(w, r, http.StatusConflict, "SAFE_MODE_ACTIVE", "Add-ons cannot be controlled while safe mode or settings hold is active.")
			return
		}
		if strings.Contains(err.Error(), "not enabled in the applied generation") {
			writeError(w, r, http.StatusConflict, "ADDON_NOT_APPLIED", "This add-on is not enabled in the applied generation.")
			return
		}
		slog.WarnContext(r.Context(), "addons: lifecycle control failed", "action", action, "slug", m.Slug, "error", err)
		writeError(w, r, http.StatusBadGateway, "ADDON_CONTROL_FAILED", "Failed to "+action+" add-on unit.")
		return
	}
	finishAudit(audit.ResultSuccess)
	writeJSON(w, r, http.StatusOK, a.buildRuntime(r.Context(), m, appliedImages, generation.ReadSafeMode(a.systemRoot, a.stateDir)))
}

func (a *addonsAPI) handleLogs(w http.ResponseWriter, r *http.Request, m *addon.Manifest, logName string) {
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
		entries, nextCursor, truncated, err := readJournal(jCtx, "andaru-addon-"+m.Slug+".service", lines, cursor)
		cancel()
		if err != nil {
			slog.WarnContext(r.Context(), "addons: read runtime journal", "slug", m.Slug, "log", logName, "error", err)
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
		entries, truncated, err := readBoundedAddonLogFile(a.stateDir, m.Slug, spec.Source.Storage, spec.Source.Path, lines)
		if err != nil {
			slog.WarnContext(r.Context(), "addons: read file log", "slug", m.Slug, "log", logName, "error", err)
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

// readBoundedAddonLogFile opens a manifest-declared log file inside the add-on's storage directory using
// openat2(RESOLVE_IN_ROOT|RESOLVE_NO_SYMLINKS) with O_NOFOLLOW|O_NONBLOCK so symlinks and container-created
// FIFOs can neither escape the storage directory nor hang the engine.
func readBoundedAddonLogFile(stateDir, slug, storage, relPath string, maxLines int) ([]string, bool, error) {
	if stateDir == "" || !addon.ValidSlug(slug) || !filepath.IsLocal(relPath) {
		return nil, false, errors.New("invalid log file path")
	}
	storageDir := filepath.Join(stateDir, "addons", slug, storage)
	dirFD, err := unix.Open(storageDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return []string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer unix.Close(dirFD)

	fd, err := unix.Openat2(dirFD, relPath, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_SYMLINKS,
	})
	if errors.Is(err, unix.ENOENT) {
		return []string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	f := os.NewFile(uintptr(fd), relPath)
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
	readLimit := int64(maxAddonLogBytes)
	startsOnLineBoundary := false
	if size > maxAddonLogBytes {
		// Seek one byte earlier so we can see whether the retained window begins right after '\n'.
		if _, err := f.Seek(size-maxAddonLogBytes-1, io.SeekStart); err != nil {
			return nil, false, err
		}
		readLimit = maxAddonLogBytes + 1
		truncated = true
	}
	raw, err := io.ReadAll(io.LimitReader(f, readLimit))
	if err != nil {
		return nil, false, err
	}
	if truncated && len(raw) > 0 {
		startsOnLineBoundary = raw[0] == '\n'
		raw = raw[1:]
		if !startsOnLineBoundary {
			if nl := bytes.IndexByte(raw, '\n'); nl >= 0 {
				raw = raw[nl+1:]
			}
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
	stdout := &tailBuffer{limit: maxAddonLogBytes + 512}
	var stderrBuf bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &limitedBuffer{buf: &stderrBuf, limit: maxAddonStderrBytes}
	if err := cmd.Run(); err != nil {
		// journalctl exits 0 even with empty matches, so non-zero is a real failure.
		return nil, "", false, fmt.Errorf("journalctl: %w: %s", err, strings.TrimSpace(stderrBuf.String()))
	}
	entries, nextCursor, truncated := parseJournalOutput(stdout.Bytes(), stdout.Truncated(), maxAddonLogBytes)
	return entries, nextCursor, truncated, nil
}

// parseJournalOutput extracts the trailing "-- cursor: ..." line, preserves the newest bytes up to limit
// without splitting a UTF-8 rune, and strips any leading partial line when truncated.
func parseJournalOutput(rawBytes []byte, droppedHead bool, limit int) ([]string, string, bool) {
	truncated := droppedHead
	data := bytes.TrimRight(rawBytes, "\n")
	if len(data) == 0 {
		return []string{}, "", truncated
	}
	// Extract the trailing cursor line before enforcing the byte budget on log lines.
	nextCursor := ""
	if nl := bytes.LastIndexByte(data, '\n'); nl >= 0 {
		if c, ok := strings.CutPrefix(string(data[nl+1:]), "-- cursor: "); ok {
			nextCursor = strings.TrimSpace(c)
			data = data[:nl]
		}
	} else if c, ok := strings.CutPrefix(string(data), "-- cursor: "); ok {
		return []string{}, strings.TrimSpace(c), truncated
	}
	startsOnLineBoundary := false
	if len(data) > limit {
		startsOnLineBoundary = data[len(data)-limit-1] == '\n'
		data = data[len(data)-limit:]
		truncated = true
	}
	if truncated && !startsOnLineBoundary {
		// Advance past any partial leading UTF-8 rune or partial leading line.
		for len(data) > 0 && !utf8.RuneStart(data[0]) {
			data = data[1:]
		}
		if nl := bytes.IndexByte(data, '\n'); nl >= 0 {
			data = data[nl+1:]
		}
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return []string{}, nextCursor, truncated
	}
	all := strings.Split(text, "\n")
	// journalctl prints "-- No entries --" when empty.
	if len(all) == 1 && all[0] == "-- No entries --" {
		return []string{}, nextCursor, truncated
	}
	return all, nextCursor, truncated
}

// tailBuffer retains the most recent `limit` bytes written to it so large journal outputs keep their
// newest lines and trailing "-- cursor: ..." marker.
type tailBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	if t.limit <= 0 {
		if len(p) > 0 {
			t.truncated = true
		}
		return len(p), nil
	}
	if len(p) > t.limit {
		t.data = append(t.data[:0], p[len(p)-t.limit:]...)
		t.truncated = true
		return len(p), nil
	}
	if len(t.data)+len(p) > t.limit {
		drop := len(t.data) + len(p) - t.limit
		copy(t.data, t.data[drop:])
		t.data = t.data[:len(t.data)-drop]
		t.truncated = true
	}
	t.data = append(t.data, p...)
	return len(p), nil
}

func (t *tailBuffer) Bytes() []byte   { return t.data }
func (t *tailBuffer) Truncated() bool { return t.truncated }

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

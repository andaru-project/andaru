package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/identity"
	"github.com/andaru-project/andaru/internal/render"
)

const (
	appliesPath = "/api/v1/applies"
	// The timer outlasts the confirmation deadline by this much, so it can never fire while activation still runs.
	timerMargin = 60
	// Longer than a verb may take to get the lock and do its work.
	realizeLimit = 150 * time.Second
)

var applyID = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

var errNewAdminSessionRequired = errors.New("a new human administrator session is required")

var (
	errInventory = errors.New("host inventory unavailable")
	errRender    = errors.New("declaration could not be rendered")
	errLive      = errors.New("live generation unreadable")
)

// hostReader is what an apply observes of the host before changing it.
type hostReader struct {
	Links   func(context.Context) ([]host.LinkStatus, error)
	Facts   func() (host.RealizerFacts, error)
	Managed func(context.Context) ([]host.Managed, error)
	// BootUnit reports andaru-network-boot.service enabled.
	BootUnit func() bool
}

func isAppliesPath(path string) bool {
	return path == appliesPath || strings.HasPrefix(path, appliesPath+"/")
}

// gatherGate collects everything the apply gates judge: the host, the rendered files and what is live now.
func gatherGate(ctx context.Context, read hostReader, dir string, pointers generation.Pointers, value json.RawMessage, secrets map[string]string) (gateInput, error) {
	gate := gateInput{Declaration: value}
	var err error
	if gate.Links, err = read.Links(ctx); err == nil {
		gate.Facts, err = read.Facts()
	}
	if err == nil {
		gate.Managed, err = read.Managed(ctx)
	}
	if err != nil {
		return gate, errors.Join(errInventory, err)
	}
	gate.BootUnit = read.BootUnit()
	if gate.Result, err = render.Network(render.Input{Declaration: value, Secrets: secrets, Links: gate.Links, Facts: gate.Facts}); err != nil {
		return gate, errors.Join(errRender, err)
	}
	if pointers.Live != nil {
		live, err := generation.Read(dir, *pointers.Live)
		if err == nil {
			gate.Live = &live
			gate.LiveFiles, err = generation.Contents(dir, live)
		}
		if err != nil {
			return gate, errors.Join(errLive, err)
		}
	}
	return gate, nil
}

type applyFile struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// applyView is one generation as the API shows it; file content stays in the preview.
type applyView struct {
	ID                    string      `json:"id"`
	SavedVersion          string      `json:"savedVersion"`
	State                 string      `json:"state"`
	Previous              *string     `json:"previous"`
	Deadline              *time.Time  `json:"deadline,omitempty"`
	Reason                string      `json:"reason"`
	CreatedAt             time.Time   `json:"createdAt"`
	ConfirmTimeoutSeconds int         `json:"confirmTimeoutSeconds"`
	Files                 []applyFile `json:"files"`
}

type appliesView struct {
	Pointers struct {
		Confirmed     *string `json:"confirmed"`
		LastKnownGood *string `json:"lastKnownGood"`
		Pending       *string `json:"pending"`
		// Live is what runs now; null is the pre-Andaru baseline. It differs from confirmed after a boot fallback.
		Live *string `json:"live"`
	} `json:"pointers"`
	Items []applyView `json:"items"`
}

func idString(id *int) *string {
	if id == nil {
		return nil
	}
	return new(strconv.Itoa(*id))
}

func viewApply(g generation.Generation) applyView {
	v := applyView{
		ID: strconv.Itoa(g.ID), SavedVersion: g.SavedVersion, State: g.State.State, Previous: idString(g.Previous),
		Deadline: g.Deadline, Reason: g.Reason, CreatedAt: g.CreatedAt, ConfirmTimeoutSeconds: g.ConfirmTimeoutSeconds, Files: []applyFile{},
	}
	for _, f := range g.Files {
		v.Files = append(v.Files, applyFile{Path: f.Path, Mode: f.Mode})
	}
	return v
}

type applyIdentity interface {
	Revision(context.Context) (int64, error)
	GetAccount(context.Context, string) (identity.Account, error)
}

type appliesHandler struct {
	input       func(context.Context, *config.Version) (config.RealizationInput, error)
	read        hostReader
	runner      realizeRunner
	dir         string
	stateDir    string
	identities  applyIdentity
	coordinator *securityCoordinator
	// mu keeps two requests from preparing generations against the same host state.
	mu sync.Mutex
}

// newAppliesHandler serves applies: the engine renders, gates and stores; realize, run by PID 1, changes the host.
func newAppliesHandler(input func(context.Context, *config.Version) (config.RealizationInput, error), read hostReader, runner realizeRunner, stateDir string, identities applyIdentity, coordinator *securityCoordinator) http.Handler {
	return &appliesHandler{
		input: input, read: read, runner: runner, dir: generation.Dir(stateDir), stateDir: stateDir,
		identities: identities, coordinator: coordinator,
	}
}

func (h *appliesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, appliesPath)
	allow := func(methods ...string) bool {
		for _, m := range methods {
			if r.Method == m {
				return true
			}
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return false
	}
	if rest == "" {
		if !allow(http.MethodGet, http.MethodHead, http.MethodPost) {
			return
		}
		if r.Method == http.MethodPost {
			h.create(w, r)
			return
		}
		h.list(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if !strings.HasPrefix(rest, "/") || !applyID.MatchString(parts[0]) || len(parts) > 2 || (len(parts) == 2 && parts[1] != "confirmation" && parts[1] != "rollback") {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	id, _ := strconv.Atoi(parts[0])
	if len(parts) == 1 {
		if allow(http.MethodGet, http.MethodHead) {
			h.show(w, r, id)
		}
		return
	}
	if allow(http.MethodPost) {
		h.finish(w, r, id, parts[1])
	}
}

func (h *appliesHandler) unavailable(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), what, "error", err)
	writeError(w, r, http.StatusServiceUnavailable, "REALIZE_UNAVAILABLE", "The apply machinery is unavailable; inspect the engine journal.")
}

func (h *appliesHandler) list(w http.ResponseWriter, r *http.Request) {
	p, items, err := generation.List(h.dir)
	if err != nil {
		h.unavailable(w, r, "read generations", err)
		return
	}
	view := appliesView{Items: []applyView{}}
	view.Pointers.Confirmed, view.Pointers.LastKnownGood, view.Pointers.Pending, view.Pointers.Live = idString(p.Confirmed), idString(p.LastKnownGood), idString(p.Pending), idString(p.Live)
	for _, g := range items {
		view.Items = append(view.Items, viewApply(g))
	}
	writeJSON(w, r, http.StatusOK, view)
}

// show writes one apply and reports whether it existed.
func (h *appliesHandler) show(w http.ResponseWriter, r *http.Request, id int) {
	g, err := generation.Read(h.dir, id)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if err != nil {
		h.unavailable(w, r, "read generation", err)
		return
	}
	writeJSON(w, r, http.StatusOK, viewApply(g))
}

func sameGenerationPointer(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (h *appliesHandler) stageAuthorizationTransition(ctx context.Context, id int, phase string, check func(*authz.StateLock, generation.Generation) error) error {
	if os.Geteuid() != 0 {
		return nil
	}
	return generation.Observe(ctx, h.dir, func(generation.Observation) error {
		lock, err := authz.Acquire(ctx, h.stateDir)
		if err != nil {
			return err
		}
		defer lock.Close()
		if _, found, err := lock.Pending(); err != nil {
			return err
		} else if found {
			return authz.ErrTransitionInProgress
		}
		candidate, err := generation.Read(h.dir, id)
		if err != nil {
			return err
		}
		if phase == authz.TransitionActivate {
			revision, err := h.identities.Revision(ctx)
			if err != nil {
				return err
			}
			if revision != candidate.IdentityRevision {
				return identity.ErrConflict
			}
		}
		if check != nil {
			if err = check(lock, candidate); err != nil {
				return err
			}
		}
		return lock.SetPending(authz.PendingTransition{
			GenerationID: id, Phase: phase, IdentityRevision: candidate.IdentityRevision,
			AuthorizationDigest: candidate.AuthorizationDigest,
		})
	})
}

func (h *appliesHandler) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SavedVersion          *string `json:"savedVersion"`
		ConfirmTimeoutSeconds *int    `json:"confirmTimeoutSeconds"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err == nil && len(body) <= 4096 {
		err = decoder.Decode(&request)
	} else {
		err = errors.New("unreadable or oversized body")
	}
	timeout := 120
	if request.ConfirmTimeoutSeconds != nil {
		timeout = *request.ConfirmTimeoutSeconds
	}
	var version config.Version
	if err == nil && request.SavedVersion != nil {
		version, err = config.ParseVersion(*request.SavedVersion)
	}
	if err != nil || request.SavedVersion == nil || decoder.More() || timeout < 30 || timeout > 600 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "An apply needs savedVersion and may set confirmTimeoutSeconds (30 to 600).")
		return
	}
	// The change must not die with the connection that asked for it: that connection is what the change may cut.
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), realizeLimit)
	defer cancel()

	input, err := h.input(ctx, nil)
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	if input.SavedVersion != version {
		writeError(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "Only the saved head can be applied; it is version "+string(input.SavedVersion)+". Preview it, then apply that version.")
		return
	}
	pointers, _, err := generation.List(h.dir)
	if err != nil {
		h.unavailable(w, r, "read generations", err)
		return
	}
	if pointers.Pending != nil {
		writeError(w, r, http.StatusConflict, "APPLY_IN_PROGRESS", "Apply "+strconv.Itoa(*pointers.Pending)+" is awaiting confirmation; confirm it or roll it back first.")
		return
	}
	gate, err := gatherGate(ctx, h.read, h.dir, pointers, input.Value, input.Secrets)
	switch {
	case errors.Is(err, errInventory):
		slog.ErrorContext(ctx, "host inventory unavailable for apply", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
		return
	case errors.Is(err, errRender):
		slog.ErrorContext(ctx, "saved declaration could not be rendered", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Saved configuration is unavailable; inspect the engine journal.")
		return
	case err != nil:
		h.unavailable(w, r, "read the live generation", err)
		return
	}
	if findings := applyFindings(gate); len(findings) > 0 {
		writeJSON(w, r, http.StatusUnprocessableEntity, errorResponse{Error: errorBody{
			Code: "APPLY_REFUSED", Message: "This configuration cannot be applied to this host as it stands.", Details: []errorDetail{}, Findings: findings,
		}})
		return
	}

	authorization, err := authz.NewSnapshot(input.Authorization.Roles)
	if err == nil && authorization.Digest != input.Authorization.Digest {
		err = errors.New("candidate authorization digest does not match its roles")
	}
	if err != nil {
		h.unavailable(w, r, "resolve candidate authorization", err)
		return
	}
	manifest := generation.Manifest{
		SavedVersion: string(input.SavedVersion), AuthorizationDigest: authorization.Digest,
		CreatedAt: time.Now().UTC(), ConfirmTimeoutSeconds: timeout,
	}
	contents := map[string][]byte{}
	for _, f := range gate.Result.Files {
		manifest.Files = append(manifest.Files, generation.FileEntry{Path: f.Path, Mode: f.Mode})
		contents[f.Path] = []byte(f.Real())
	}
	var id int
	store := func(observed generation.Observation) error {
		if observed.Pending != nil || !sameGenerationPointer(observed.Live, pointers.Live) {
			return authz.ErrTransitionInProgress
		}
		return h.coordinator.withAuthorizationLock(ctx, func() error {
			revision, revisionErr := h.identities.Revision(ctx)
			if revisionErr != nil {
				return revisionErr
			}
			manifest.IdentityRevision = revision
			id, revisionErr = generation.Create(h.dir, manifest, contents)
			return revisionErr
		})
	}
	if os.Geteuid() != 0 {
		err = store(generation.Observation{Live: pointers.Live})
	} else {
		err = generation.Observe(ctx, h.dir, store)
	}
	if errors.Is(err, authz.ErrTransitionInProgress) {
		writeError(w, r, http.StatusConflict, "APPLY_IN_PROGRESS", "The live generation or authorization state changed while this apply was prepared; retry it.")
		return
	}
	if err != nil {
		h.unavailable(w, r, "store generation", err)
		return
	}
	if closure, cerr := config.LANClosure(input.Value, input.Secrets); cerr == nil {
		path := filepath.Join(h.dir, strconv.Itoa(id), "lan-closure.json")
		if werr := os.WriteFile(path, closure, 0o600); werr != nil {
			slog.ErrorContext(ctx, "write the LAN closure", "error", werr)
		}
	}
	// Armed before anything changes: from here the host returns to what it ran, with or without this engine.
	if err := h.runner.Arm(ctx, id, timerMargin+timeout); err != nil {
		h.unavailable(w, r, "arm rollback timer", err)
		return
	}
	if err = h.stageAuthorizationTransition(ctx, id, authz.TransitionActivate, nil); err != nil {
		h.runner.Disarm(ctx, id)
		if errors.Is(err, identity.ErrConflict) {
			writeError(w, r, http.StatusPreconditionFailed, "IDENTITY_REVISION_CHANGED", "Identity changed after this apply candidate was created; retry the apply.")
			return
		}
		h.unavailable(w, r, "authorize generation activation", err)
		return
	}
	runErr := h.runner.Realize(ctx, "activate", id)
	g, err := generation.Read(h.dir, id)
	if err != nil || (g.State.State == generation.Failed && g.Reason == "never started") {
		// Whether the unit ran is unknown, so the timer stays armed: it does nothing when there is nothing to undo.
		h.unavailable(w, r, "activate generation", errors.Join(runErr, err))
		return
	}
	if runErr != nil {
		slog.WarnContext(ctx, "activation did not succeed", "generation", id, "error", runErr)
	}
	// Only a generation realize has finished with may lose its timer; one still pending has nothing else to undo it.
	if g.State.State == generation.Failed {
		h.runner.Disarm(ctx, id)
	}
	writeJSON(w, r, http.StatusOK, viewApply(g))
}

// finish confirms or rolls back a pending apply. realize decides; the engine reports what it finds afterwards.
func (h *appliesHandler) finish(w http.ResponseWriter, r *http.Request, id int, action string) {
	actor, verified := actorFromRequest(r)
	if !verified {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Started after the wait for the mutex, so a queued request keeps its full time.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), realizeLimit)
	defer cancel()
	before, err := generation.Read(h.dir, id)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if err != nil {
		h.unavailable(w, r, "read generation", err)
		return
	}
	verb, want, extra := "confirm", generation.Confirmed, []string(nil)
	if action == "rollback" {
		verb, want, extra = "rollback", generation.RolledBack, []string{"requested"}
	}
	if err == nil && before.State.State != generation.Awaiting && before.State.State != generation.Activating {
		writeError(w, r, http.StatusConflict, "APPLY_NOT_AWAITING", "Apply "+strconv.Itoa(id)+" is "+before.State.State+" and can no longer be changed.")
		return
	}
	if err == nil && action == "confirmation" {
		previousDigest := ""
		if before.Previous != nil {
			previous, previousErr := generation.Read(h.dir, *before.Previous)
			if previousErr != nil {
				h.unavailable(w, r, "read previous authorization", previousErr)
				return
			}
			previousDigest = previous.AuthorizationDigest
		}
		if before.AuthorizationDigest != previousDigest {
			err = h.stageAuthorizationTransition(ctx, id, authz.TransitionConfirm, func(lock *authz.StateLock, candidate generation.Generation) error {
				state, stateErr := lock.Read()
				if stateErr != nil || state.LiveDigest != candidate.AuthorizationDigest {
					return errors.Join(errLiveAuthorizationUnavailable, stateErr)
				}
				if actor.Root {
					return nil
				}
				if actor.Actor.Kind != authz.ActorAccount || actor.AccountKind != identity.Human || actor.SessionSelector == "" || actor.AuthorizationEpoch != state.AuthorizationEpoch {
					return errNewAdminSessionRequired
				}
				account, accountErr := h.identities.GetAccount(ctx, actor.AccountID)
				if accountErr != nil {
					return accountErr
				}
				if !account.Enabled || account.Kind != identity.Human || !slices.Contains(account.RoleIDs, authz.AdministratorRoleID) {
					return errNewAdminSessionRequired
				}
				return nil
			})
			if errors.Is(err, errNewAdminSessionRequired) {
				writeError(w, r, http.StatusForbidden, "NEW_ADMIN_SESSION_REQUIRED", "Authorization changes require confirmation by root or a newly authenticated human administrator.")
				return
			}
			if err != nil {
				h.unavailable(w, r, "authorize generation confirmation", err)
				return
			}
		}
	}
	runErr := h.runner.Realize(ctx, verb, id, extra...)
	g, err := generation.Read(h.dir, id)
	if err != nil {
		h.unavailable(w, r, verb+" generation", errors.Join(runErr, err))
		return
	}
	if runErr != nil {
		slog.WarnContext(ctx, verb+" did not finish cleanly", "generation", id, "error", runErr)
	}
	if g.State.State != want {
		writeError(w, r, http.StatusConflict, "APPLY_NOT_AWAITING", "Apply "+strconv.Itoa(id)+" is "+g.State.State+"; it was not changed.")
		return
	}
	h.runner.Disarm(ctx, id)
	writeJSON(w, r, http.StatusOK, viewApply(g))
}

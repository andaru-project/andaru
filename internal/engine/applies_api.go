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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
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

// hostReader is what an apply observes of the host before changing it.
type hostReader struct {
	Links    func(context.Context) ([]host.LinkStatus, error)
	Facts    func() (host.RealizerFacts, error)
	Managed  func(context.Context) ([]host.Managed, error)
	Baseline func() (host.Baseline, error)
}

type applyFile struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// applyView is one generation as the API shows it; file content stays in the preview.
type applyView struct {
	ID                    string      `json:"id"`
	SavedVersion          string      `json:"savedVersion"`
	Takeover              bool        `json:"takeover"`
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
		ID: strconv.Itoa(g.ID), SavedVersion: g.SavedVersion, Takeover: g.Takeover, State: g.State.State, Previous: idString(g.Previous),
		Deadline: g.Deadline, Reason: g.Reason, CreatedAt: g.CreatedAt, ConfirmTimeoutSeconds: g.ConfirmTimeoutSeconds, Files: []applyFile{},
	}
	for _, f := range g.Files {
		v.Files = append(v.Files, applyFile{Path: f.Path, Mode: f.Mode})
	}
	return v
}

type appliesHandler struct {
	input  func(context.Context, *config.Version) (config.RealizationInput, error)
	read   hostReader
	runner realizeRunner
	dir    string
	// mu keeps two requests from preparing generations against the same host state.
	mu sync.Mutex
}

// newAppliesHandler serves applies: the engine renders, gates and stores; realize, run by PID 1, changes the host.
func newAppliesHandler(input func(context.Context, *config.Version) (config.RealizationInput, error), read hostReader, runner realizeRunner, stateDir string) http.Handler {
	return &appliesHandler{input: input, read: read, runner: runner, dir: generation.Dir(stateDir)}
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

func (h *appliesHandler) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SavedVersion          *string `json:"savedVersion"`
		Takeover              bool    `json:"takeover"`
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
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "An apply needs savedVersion and may set takeover and confirmTimeoutSeconds (30 to 600).")
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
	gate := gateInput{Takeover: request.Takeover, Declaration: input.Value}
	if gate.Links, err = h.read.Links(ctx); err == nil {
		gate.Facts, err = h.read.Facts()
	}
	if err == nil {
		gate.Managed, err = h.read.Managed(ctx)
	}
	if err == nil {
		gate.Baseline, err = h.read.Baseline()
	}
	if err != nil {
		slog.ErrorContext(ctx, "host inventory unavailable for apply", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
		return
	}
	if gate.Result, err = render.Network(render.Input{Declaration: input.Value, Secrets: input.Secrets, Links: gate.Links, Facts: gate.Facts}); err != nil {
		slog.ErrorContext(ctx, "saved declaration could not be rendered", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Saved configuration is unavailable; inspect the engine journal.")
		return
	}
	if pointers.Live != nil {
		live, err := generation.Read(h.dir, *pointers.Live)
		if err == nil {
			gate.Live = &live
			gate.LiveFiles, err = generation.Contents(h.dir, live)
		}
		if err != nil {
			h.unavailable(w, r, "read the live generation", err)
			return
		}
	}
	if findings := applyFindings(gate); len(findings) > 0 {
		writeJSON(w, r, http.StatusUnprocessableEntity, errorResponse{Error: errorBody{
			Code: "APPLY_REFUSED", Message: "This configuration cannot be applied to this host as it stands.", Details: []errorDetail{}, Findings: findings,
		}})
		return
	}

	manifest := generation.Manifest{SavedVersion: string(input.SavedVersion), CreatedAt: time.Now().UTC(), Takeover: request.Takeover, ConfirmTimeoutSeconds: timeout}
	contents := map[string][]byte{}
	for _, f := range gate.Result.Files {
		manifest.Files = append(manifest.Files, generation.FileEntry{Path: f.Path, Mode: f.Mode})
		contents[f.Path] = []byte(f.Real())
	}
	id, err := generation.Create(h.dir, manifest, contents)
	if err != nil {
		h.unavailable(w, r, "store generation", err)
		return
	}
	// Armed before anything changes: from here the host returns to what it ran, with or without this engine.
	if err := h.runner.Arm(ctx, id, timerMargin+timeout); err != nil {
		h.unavailable(w, r, "arm rollback timer", err)
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
	verb, want, extra := "confirm", generation.Confirmed, []string(nil)
	if action == "rollback" {
		verb, want, extra = "rollback", generation.RolledBack, []string{"requested"}
	}
	if err == nil && before.State.State != generation.Awaiting && before.State.State != generation.Activating {
		writeError(w, r, http.StatusConflict, "APPLY_NOT_AWAITING", "Apply "+strconv.Itoa(id)+" is "+before.State.State+" and can no longer be changed.")
		return
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

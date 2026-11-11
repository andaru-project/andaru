package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

const (
	proposalPath = "/api/v1/takeover/proposal"
	draftPath    = "/api/v1/preview/network/draft"
)

var (
	errInventory = errors.New("host inventory unavailable")
	errRender    = errors.New("declaration could not be rendered")
	errLive      = errors.New("live generation unreadable")
)

// gatherGate collects everything the apply gates judge: the host, the rendered files and what is live now.
// The apply and the draft preview both use it, so a draft is judged exactly as the apply will be.
func gatherGate(ctx context.Context, read hostReader, dir string, pointers generation.Pointers, value json.RawMessage, secrets map[string]string, takeover bool) (gateInput, error) {
	gate := gateInput{Takeover: takeover, Declaration: value}
	var err error
	if gate.Links, err = read.Links(ctx); err == nil {
		gate.Facts, err = read.Facts()
	}
	if err == nil {
		gate.Managed, err = read.Managed(ctx)
	}
	if err == nil {
		gate.Baseline, err = read.Baseline()
	}
	if err != nil {
		return gate, errors.Join(errInventory, err)
	}
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

// newLiveHandler serves the routes that look at or change the running host.
func newLiveHandler(applies, takeover http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == proposalPath || r.URL.Path == draftPath {
			takeover.ServeHTTP(w, r)
			return
		}
		applies.ServeHTTP(w, r)
	})
}

func isLivePath(path string) bool {
	return path == proposalPath || path == draftPath || path == appliesPath || strings.HasPrefix(path, appliesPath+"/")
}

type takeoverHandler struct {
	head  func(context.Context, *config.Version) (config.RealizationInput, error)
	draft func(context.Context, config.Changes) (config.RealizationInput, error)
	read  hostReader
	dir   string
}

// newTakeoverHandler serves the assistant's two read-only routes: what the host does now, and what a batch would do.
func newTakeoverHandler(head func(context.Context, *config.Version) (config.RealizationInput, error), draft func(context.Context, config.Changes) (config.RealizationInput, error), read hostReader, stateDir string) http.Handler {
	return &takeoverHandler{head: head, draft: draft, read: read, dir: generation.Dir(stateDir)}
}

func (h *takeoverHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	want := http.MethodGet
	if r.URL.Path == draftPath {
		want = http.MethodPost
	}
	if r.Method != want && !(want == http.MethodGet && r.Method == http.MethodHead) {
		allow := want
		if want == http.MethodGet {
			allow = "GET, HEAD"
		}
		w.Header().Set("Allow", allow)
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return
	}
	if r.URL.Path == draftPath {
		h.serveDraft(w, r)
		return
	}
	h.serveProposal(w, r)
}

type adapterView struct {
	Name         string `json:"name"`
	PermanentMac string `json:"permanentMac"`
	Summary      string `json:"summary"`
}

type proposalView struct {
	State        string             `json:"state"`
	SavedVersion config.Version     `json:"savedVersion"`
	Operations   []config.Operation `json:"operations"`
	Adapters     []adapterView      `json:"adapters"`
	Findings     []render.Finding   `json:"findings"`
}

func (h *takeoverHandler) serveProposal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := proposalView{Operations: []config.Operation{}, Adapters: []adapterView{}, Findings: []render.Finding{}}
	input, err := h.head(ctx, nil)
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	view.SavedVersion = input.SavedVersion
	pointers, _, err := generation.List(h.dir)
	if err != nil {
		slog.ErrorContext(ctx, "read generations", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "REALIZE_UNAVAILABLE", "The apply machinery is unavailable; inspect the engine journal.")
		return
	}
	var saved struct {
		Links []json.RawMessage `json:"links"`
	}
	_ = json.Unmarshal(input.Value, &saved)
	unsupported := func(path, message string) {
		view.State = "unsupported"
		view.Findings = append(view.Findings, render.Finding{Severity: "error", Code: "TAKEOVER_UNSUPPORTED_BASELINE", Path: path, Message: message})
	}
	switch {
	case pointers.Takeover || pointers.Pending != nil || pointers.Confirmed != nil:
		view.State = "taken-over"
	case len(saved.Links) > 0:
		view.State = "saved-not-applied"
	}
	if view.State != "" {
		writeJSON(w, r, http.StatusOK, view)
		return
	}
	baseline, err := h.read.Baseline()
	var links []host.LinkStatus
	var managed []host.Managed
	var route string
	if err == nil {
		links, err = h.read.Links(ctx)
	}
	if err == nil {
		managed, err = h.read.Managed(ctx)
	}
	if err == nil {
		route, err = h.read.Route(ctx)
	}
	if err != nil {
		slog.ErrorContext(ctx, "host inventory unavailable for the takeover proposal", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
		return
	}
	switch {
	case baseline.Ifupdown:
		unsupported("/sbin/ifup", "This host is configured with ifupdown; taking over an ifupdown host comes in a later release.")
	case !baseline.Netplan:
		unsupported("/usr/sbin/netplan", "Only a netplan-configured host can be taken over by the assistant.")
	}
	if view.State != "" {
		writeJSON(w, r, http.StatusOK, view)
		return
	}
	found, problems := host.ReadNetplan(h.read.Root, links, managed, route)
	for _, p := range problems {
		message := p.Message
		if p.Key != "" {
			message = p.Key + ": " + message
		}
		unsupported(p.File, message)
	}
	if len(found) == 0 && len(problems) == 0 {
		unsupported("/run/systemd/network", "netplan configures no interface here.")
	}
	if view.State != "" {
		writeJSON(w, r, http.StatusOK, view)
		return
	}
	view.State, view.Operations, view.Adapters = "ready", proposalOperations(found), adapters(found)
	writeJSON(w, r, http.StatusOK, view)
}

func create(collection, localID string, value any) config.Operation {
	raw, _ := json.Marshal(value)
	return config.Operation{Op: "create", Collection: collection, LocalID: localID, Value: raw}
}

func family(a host.Addressing) map[string]any {
	out := map[string]any{"mode": a.Mode}
	if a.Mode == "static" {
		out["addresses"] = a.Addresses
		if a.Gateway != "" {
			out["gateway"] = a.Gateway
		}
	}
	return out
}

// proposalOperations turns what netplan configures into one create-only change batch.
func proposalOperations(found []host.NetplanInterface) []config.Operation {
	zones := map[string]bool{}
	var links, segments []config.Operation
	for i, n := range found {
		zone := "lan"
		if n.DefaultRoute {
			zone = "wan"
		}
		zones[zone] = true
		link := fmt.Sprintf("link%d", i+1)
		links = append(links, create("links", link, map[string]any{"name": n.Name, "kind": "ethernet", "mtu": n.MTU, "enabled": true, "permanentMac": n.PermanentMAC}))
		addressing := map[string]any{"ipv4": family(n.IPv4), "ipv6": family(n.IPv6)}
		if len(n.DNS) > 0 {
			dns := map[string]any{"servers": n.DNS}
			if len(n.Domains) > 0 {
				dns["searchDomains"] = n.Domains
			}
			addressing["dns"] = dns
		}
		segments = append(segments, create("segments", fmt.Sprintf("seg%d", i+1), map[string]any{"name": n.Name, "link": "@" + link, "zone": "@" + zone, "addressing": addressing}))
	}
	var ops []config.Operation
	for _, zone := range []string{"wan", "lan"} {
		if zones[zone] {
			ops = append(ops, create("zones", zone, map[string]any{"name": zone}))
		}
	}
	return slices.Concat(ops, links, segments)
}

func describe(a host.Addressing) string {
	switch a.Mode {
	case "static":
		return "static " + strings.Join(a.Addresses, " ")
	case "dhcp", "dhcp6":
		return strings.ToUpper(a.Mode)
	case "slaac":
		return "SLAAC"
	}
	return "none"
}

func adapters(found []host.NetplanInterface) []adapterView {
	out := []adapterView{}
	for _, n := range found {
		summary := "IPv4 " + describe(n.IPv4) + ", IPv6 " + describe(n.IPv6)
		if n.DefaultRoute {
			summary += ", default route"
		}
		if len(n.DNS) > 0 {
			summary += ", DNS " + strings.Join(n.DNS, " ")
		}
		out = append(out, adapterView{Name: n.Name, PermanentMac: n.PermanentMAC, Summary: summary})
	}
	return out
}

type draftView struct {
	previewResponse
	ApplyFindings []render.Finding `json:"applyFindings"`
}

func (h *takeoverHandler) serveDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		writeError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Request body is too large.")
		return
	}
	changes, err := config.DecodeChanges(body)
	if err == nil {
		var input config.RealizationInput
		if input, err = h.draft(ctx, changes); err == nil {
			h.judge(w, r, input)
			return
		}
	}
	writeConfigurationError(w, r, err)
}

func (h *takeoverHandler) judge(w http.ResponseWriter, r *http.Request, input config.RealizationInput) {
	ctx := r.Context()
	pointers, _, err := generation.List(h.dir)
	if err == nil {
		var gate gateInput
		if gate, err = gatherGate(ctx, h.read, h.dir, pointers, input.Value, input.Secrets, !pointers.Takeover); err == nil {
			findings := applyFindings(gate)
			if findings == nil {
				findings = []render.Finding{}
			}
			writeJSON(w, r, http.StatusOK, draftView{previewResponse{State: "draft", SavedVersion: input.SavedVersion, Result: gate.Result}, findings})
			return
		}
	}
	slog.ErrorContext(ctx, "draft could not be judged", "error", err)
	if errors.Is(err, errInventory) {
		writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
		return
	}
	writeError(w, r, http.StatusServiceUnavailable, "REALIZE_UNAVAILABLE", "The apply machinery is unavailable; inspect the engine journal.")
}

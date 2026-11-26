package engine

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/aliasfeed"
)

const aliasesPath = "/api/v1/aliases/addresses"

// aliasesAPI shows the state of the address aliases fed from a URL or from host names, and asks for one's list now.
type aliasesAPI struct{ feeds *aliasFeeds }

// aliasStatus is one alias's state as the API shows it. A time never reached is null.
type aliasStatus struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Kind        string       `json:"kind"`
	LastSuccess *time.Time   `json:"lastSuccess"`
	LastAttempt *time.Time   `json:"lastAttempt"`
	Entries     int          `json:"entries"`
	LastError   string       `json:"lastError,omitempty"`
	Failures    int          `json:"failures"`
	Names       []nameStatus `json:"names,omitempty"`
}

type nameStatus struct {
	Name       string     `json:"name"`
	Addresses  []string   `json:"addresses"`
	Error      string     `json:"error,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt"`
}

func when(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (h aliasesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	if r.URL.Path == aliasesPath+"/status" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		h.status(w, r)
		return
	}
	id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, aliasesPath+"/"), "/refresh")
	if !ok || !strings.HasPrefix(r.URL.Path, aliasesPath+"/") || id == "" || strings.Contains(id, "/") {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, "POST")
		return
	}
	if !requireNoBody(w, r) {
		return
	}
	request, found, err := h.feeds.refresh(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "ask for an alias's list", "alias", id, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "ALIASES_UNAVAILABLE", "Aliases are temporarily unavailable.")
		return
	}
	if !found {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "No address alias fed from a URL or from host names has this ID.")
		return
	}
	writeJSON(w, r, http.StatusAccepted, map[string]string{"request": request})
}

func (h aliasesAPI) status(w http.ResponseWriter, r *http.Request) {
	h.feeds.mu.Lock()
	defer h.feeds.mu.Unlock()
	list, _, err := h.feeds.aliases(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "read the saved aliases", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "ALIASES_UNAVAILABLE", "Aliases are temporarily unavailable.")
		return
	}
	items := []aliasStatus{}
	for _, a := range list {
		// Read only: a state that does not decode is moved aside by the engine's next write, not by a read.
		s, err := aliasfeed.Read(h.feeds.stateDir, a.ID, sourceOf(a).hash)
		if err != nil {
			slog.WarnContext(r.Context(), "read an alias source's state", "alias", a.ID, "error", err)
		}
		item := aliasStatus{ID: a.ID, Name: a.Name, Kind: a.Kind, LastSuccess: when(s.LastSuccess), LastAttempt: when(s.LastAttempt),
			Entries: len(s.Members), LastError: s.LastError, Failures: s.Failures}
		for _, name := range a.Names {
			n := nameStatus{Name: name, Addresses: []string{}}
			for _, kept := range s.Names {
				if strings.EqualFold(kept.Name, name) {
					n.Addresses, n.Error, n.ResolvedAt = kept.Addresses, kept.Error, when(kept.ResolvedAt)
				}
			}
			item.Names = append(item.Names, n)
		}
		items = append(items, item)
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"items": items})
}

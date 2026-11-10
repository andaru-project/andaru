package engine

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

const previewPath = "/api/v1/preview/network"

// state is "preview" so no client can mistake rendered files for saved or live state.
type previewResponse struct {
	State        string         `json:"state"`
	SavedVersion config.Version `json:"savedVersion"`
	render.Result
}

// newSavedHandler serves everything that reads the saved store.
func newSavedHandler(store *config.Store, readLinks func(context.Context) ([]host.LinkStatus, error), readFacts func() (host.RealizerFacts, error)) http.Handler {
	configuration, preview := newConfigurationHandler(store), newPreviewHandler(store, readLinks, readFacts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == previewPath {
			preview.ServeHTTP(w, r)
			return
		}
		configuration.ServeHTTP(w, r)
	})
}

// newPreviewHandler renders a saved version for this host. It reads and never writes.
func newPreviewHandler(store *config.Store, readLinks func(context.Context) ([]host.LinkStatus, error), readFacts func() (host.RealizerFacts, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		var version *config.Version
		for key, values := range r.URL.Query() {
			parsed, err := config.ParseVersion(values[0])
			if key != "savedVersion" || len(values) != 1 || err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid preview query.")
				return
			}
			version = &parsed
		}
		// The store is read first so an unknown version is reported even when the host cannot be inspected.
		input, err := store.RealizationInput(r.Context(), version)
		if err != nil {
			writeConfigurationError(w, r, err)
			return
		}
		links, err := readLinks(r.Context())
		var facts host.RealizerFacts
		if err == nil {
			facts, err = readFacts()
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "host inventory unavailable for preview", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
			return
		}
		result, err := render.Network(render.Input{Declaration: input.Value, Secrets: input.Secrets, Links: links, Facts: facts})
		if err != nil {
			// The error describes JSON structure only; secret values are not part of the declaration.
			slog.ErrorContext(r.Context(), "saved declaration could not be rendered", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Saved configuration is unavailable; inspect the engine journal.")
			return
		}
		w.Header().Set("Andaru-Saved-Version", string(input.SavedVersion))
		writeJSON(w, r, http.StatusOK, previewResponse{State: "preview", SavedVersion: input.SavedVersion, Result: result})
	})
}

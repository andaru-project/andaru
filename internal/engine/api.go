package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
)

//go:embed openapi.json
var openAPIDocument []byte

type peerKey struct{}

type peerIdentity struct {
	uid      uint32
	verified bool
}

type linksResponse struct {
	Items []host.LinkStatus `json:"items"`
}

type errorDetail = config.FieldError

type errorBody struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details []errorDetail   `json:"details"`
	Current *config.Current `json:"current,omitempty"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

func newHandler(readLinks func(context.Context) ([]host.LinkStatus, error), configuration http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		peer, _ := r.Context().Value(peerKey{}).(peerIdentity)
		if !peer.verified || peer.uid != 0 {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Root Unix peer credentials are required.")
			return
		}
		if r.URL.Path == "/api/v1/schema" || r.URL.Path == "/api/v1/config" || strings.HasPrefix(r.URL.Path, "/api/v1/config/") || r.URL.Path == "/api/v1/versions" || strings.HasPrefix(r.URL.Path, "/api/v1/versions/") || r.URL.Path == previewPath {
			configuration.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/api/v1/status/links" && r.URL.Path != "/api/v1/openapi.json" {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		if r.URL.Path == "/api/v1/openapi.json" {
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(openAPIDocument)
			}
			return
		}
		links, err := readLinks(r.Context())
		if err != nil {
			slog.ErrorContext(r.Context(), "interface inventory unavailable", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
			return
		}
		if links == nil {
			links = []host.LinkStatus{}
		}
		writeJSON(w, r, http.StatusOK, linksResponse{Items: links})
	})
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, r, status, errorResponse{Error: errorBody{Code: code, Message: message, Details: []errorDetail{}}})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		if err := json.NewEncoder(w).Encode(body); err != nil {
			slog.ErrorContext(r.Context(), "write engine API response", "error", err)
		}
	}
}

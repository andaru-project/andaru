package engine

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/notify"
)

const notificationsPath = "/api/v1/notifications"

// notificationsAPI lists the active notifications an actor may see, and dismisses them. Without a layer, which did
// not open, a well-formed request is answered as unavailable.
type notificationsAPI struct{ layer *notify.Layer }

func (h notificationsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	if !noQuery(w, r) {
		return
	}
	// Seeing a notification takes notifications.read and the capability it requires, if any; one the actor may not
	// see is one they cannot dismiss either, and is answered as not there.
	sees := func(requires string) bool {
		return hasCapability(actor, authz.NotificationsRead) && (requires == "" || hasCapability(actor, authz.Capability(requires)))
	}
	if r.URL.Path == notificationsPath {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		if h.layer == nil {
			h.unavailable(w, r)
			return
		}
		list, err := h.layer.Active()
		if err != nil {
			slog.ErrorContext(r.Context(), "read notifications", "error", err)
			h.unavailable(w, r)
			return
		}
		shown := list[:0]
		for _, n := range list {
			if sees(n.Requires) {
				shown = append(shown, n)
			}
		}
		writeJSON(w, r, http.StatusOK, map[string]any{"items": shown})
		return
	}
	idText, dismiss := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, notificationsPath+"/"), "/dismiss")
	id, err := strconv.ParseInt(idText, 10, 64)
	if !dismiss || err != nil || id < 1 || strconv.FormatInt(id, 10) != idText {
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
	if h.layer == nil {
		h.unavailable(w, r)
		return
	}
	by := actor.AccountID
	if by == "" {
		by = actor.Actor.ID
	}
	found, err := h.layer.Dismiss(id, by, sees)
	if err != nil {
		slog.ErrorContext(r.Context(), "dismiss notification", "id", id, "error", err)
		h.unavailable(w, r)
		return
	}
	if !found {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (notificationsAPI) unavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", "Notifications are temporarily unavailable.")
}

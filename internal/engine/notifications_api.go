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

// notificationsAPI lists the active notifications an actor may see, and dismisses them.
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
	if r.URL.Path == notificationsPath {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, r, "GET, HEAD")
			return
		}
		list, err := h.visible(w, r, actor)
		if err != nil {
			return
		}
		writeJSON(w, r, http.StatusOK, map[string]any{"items": list})
		return
	}
	rest, _ := strings.CutPrefix(r.URL.Path, notificationsPath+"/")
	idText, dismiss := strings.CutSuffix(rest, "/dismiss")
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
	// A notification the actor may not see is one they cannot dismiss either, and is answered as not there.
	list, err := h.visible(w, r, actor)
	if err != nil {
		return
	}
	found := false
	for _, n := range list {
		found = found || n.ID == id
	}
	by := actor.AccountID
	if by == "" {
		by = actor.Actor.ID
	}
	if found {
		found, err = h.layer.Dismiss(id, by)
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "dismiss notification", "id", id, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", "Notifications are temporarily unavailable.")
		return
	}
	if !found {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// visible is the active list filtered to what actor may see; it answers the request itself on an error.
func (h notificationsAPI) visible(w http.ResponseWriter, r *http.Request, actor requestActor) ([]notify.Notification, error) {
	list, err := h.layer.Active()
	if err != nil {
		slog.ErrorContext(r.Context(), "read notifications", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", "Notifications are temporarily unavailable.")
		return nil, err
	}
	shown := list[:0]
	for _, n := range list {
		if n.Requires == "" || hasCapability(actor, authz.Capability(n.Requires)) {
			shown = append(shown, n)
		}
	}
	return shown, nil
}

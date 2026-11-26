package engine

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/notify"
)

const notificationsPath = "/api/v1/notifications"

// notificationsAPI lists the active notifications an actor may see, dismisses them, and sends a test to a channel.
// Without a layer, which did not open, a well-formed request for the list is answered as unavailable.
type notificationsAPI struct {
	layer    *notify.Layer
	channels channelReader
	events   *bus.Bus
	origin   string // the management origin a notification's link is sent from
}

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
	if rest, ok := strings.CutPrefix(r.URL.Path, notificationsPath+"/channels/"); ok {
		h.test(w, r, rest)
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

// test sends a test notification to the channel in path, {id}/test, enabled or not, through the same delivery path
// as any other. The notification is not recorded: it is the channel's alone.
func (h notificationsAPI) test(w http.ResponseWriter, r *http.Request, path string) {
	id, ok := strings.CutSuffix(path, "/test")
	if !ok || id == "" || strings.Contains(id, "/") {
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
	if h.channels == nil || h.events == nil {
		h.unavailable(w, r)
		return
	}
	list, err := h.channels(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "read notification channels", "error", err)
		h.unavailable(w, r)
		return
	}
	i := slices.IndexFunc(list, func(c config.NotificationChannel) bool { return c.ID == id })
	if i < 0 {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	c := list[i]
	if c.Unresolved {
		writeError(w, r, http.StatusConflict, "CHANNEL_SECRETS_UNRESOLVED", "The channel's secrets have no value, as after a restore without secrets; set them, then send a test.")
		return
	}
	raise := notify.Raise{Key: "test", Severity: notify.Info, Subject: "Test notification from Andaru",
		Detail: notify.Detail("A test of the notification channel " + c.Name + ".")}
	payload, err := bus.NewEvent(notify.RaisedType, "notifications", raise)
	var e bus.Event
	if err == nil {
		err = json.Unmarshal(payload, &e)
	}
	job := "test:" + e.ID + ":" + c.ID
	if err == nil {
		err = publishJob(h.events, c, h.origin, job, e.Source, e.Time, raise, payload)
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "send a test notification", "channel", c.ID, "error", err)
		h.unavailable(w, r)
		return
	}
	writeJSON(w, r, http.StatusAccepted, map[string]string{"job": job})
}

func (notificationsAPI) unavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", "Notifications are temporarily unavailable.")
}

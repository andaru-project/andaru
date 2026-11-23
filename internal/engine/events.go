package engine

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
)

const eventsPath = "/api/v1/events"

// eventsHeartbeat is how often an open event stream is written to and its session checked again: a stream ends
// within this long of its session ending or losing configuration.read.
var eventsHeartbeat = 25 * time.Second

// openBus starts the event bus with its store in the state directory and its socket beside the engine's, where
// only root reaches it: andaru realize publishes the state of each apply there.
func openBus(stateDir, engineSocket string) (*bus.Bus, func() error, error) {
	listener, closeListener, err := listenRootSocket(filepath.Join(filepath.Dir(engineSocket), "bus.sock"), "bus")
	if err != nil {
		return nil, nil, err
	}
	events, err := bus.Open(bus.Options{
		Path:   filepath.Join(stateDir, "bus.db"),
		Grants: map[string]bus.Grants{"realize": {Publish: []string{"apply/+"}}},
		// The socket is root's alone, so whoever connects is trusted with the grants of the name it gives.
		Authenticate: func(_, _ []byte) bool { return true },
		Listeners:    map[string]net.Listener{"root": listener},
		Logger:       slog.Default().With("component", "bus"),
	})
	if err != nil {
		return nil, nil, errors.Join(err, closeListener())
	}
	return events, func() error { return errors.Join(events.Close(), closeListener()) }, nil
}

// publishHead tells subscribers of each new saved head. It is not durable: a subscriber reads the head when it
// starts and again whenever it reconnects.
func publishHead(events *bus.Bus) func(config.Version) {
	return func(version config.Version) {
		payload, err := bus.NewEvent("config.head.changed", "engine", map[string]string{"version": string(version)})
		if err == nil {
			err = events.Publish("config/head", payload, false)
		}
		if err != nil {
			slog.Warn("publish saved head", "version", version, "error", err)
		}
	}
}

// eventStream sends the browser the events that change what the shell shows, as Server-Sent Events: a new saved
// head and each apply's state. The browser reads that state again when the stream opens and at every event, so
// the events carry nothing it must keep. The stream ends when its session does, and the browser opens it again.
func (h *webAPI) eventStream(w http.ResponseWriter, r *http.Request, source string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, "GET")
		return
	}
	if !noQuery(w, r) || !requireNoBody(w, r) {
		return
	}
	principal, _, _, _, _, ok := h.cookiePrincipal(w, r, source)
	if !ok {
		return
	}
	allowed := func(actor requestActor) bool { return hasCapability(actor, authz.ConfigurationRead) }
	actor := requestActor{Actor: principal.Actor, Source: source, AccountID: principal.Account.ID, Capabilities: principal.Capabilities}
	if !allowed(actor) {
		h.deny(w, r, actor)
		return
	}
	// A browser too slow to take its events loses the stream rather than holding up the publisher; it opens the
	// stream again and reads the state anew.
	queue := make(chan []byte, 16)
	behind := make(chan struct{})
	var once sync.Once
	deliver := func(_ string, payload []byte) {
		select {
		case queue <- payload:
		default:
			once.Do(func() { close(behind) })
		}
	}
	for _, filter := range []string{"config/head", "apply/+"} {
		stop, err := h.events.Subscribe(filter, deliver)
		if err != nil {
			slog.ErrorContext(r.Context(), "subscribe to events", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "EVENTS_UNAVAILABLE", "Events are temporarily unavailable.")
			return
		}
		defer stop()
	}
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if controller.Flush() != nil {
		return
	}
	heartbeat := time.NewTicker(eventsHeartbeat)
	defer heartbeat.Stop()
	for {
		var message []byte
		select {
		case <-r.Context().Done():
			return
		case <-behind:
			return
		case payload := <-queue:
			// Each line of the payload is a data line, so no payload can end the event early.
			for _, line := range bytes.Split(payload, []byte("\n")) {
				message = append(append(append(message, "data: "...), line...), '\n')
			}
			message = append(message, '\n')
		case <-heartbeat.C:
			current, _, _, _, _, err := h.readCookiePrincipal(r)
			if err != nil || !allowed(requestActor{Actor: current.Actor, Capabilities: current.Capabilities}) {
				return
			}
			message = []byte(":\n\n")
		}
		if _, err := w.Write(message); err != nil || controller.Flush() != nil {
			return
		}
	}
}

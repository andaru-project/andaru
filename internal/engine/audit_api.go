package engine

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
)

type auditAPI struct {
	coordinator *securityCoordinator
}

// auditActorView names the actor's fields as the API contract does. authz.Actor carries no JSON tags, and Go's
// decoders match names without regard to case, so only a browser notices "Kind" and "ID".
type auditActorView struct {
	Kind authz.ActorKind `json:"kind"`
	ID   string          `json:"id"`
}

type auditEventView struct {
	ID          uint64            `json:"id"`
	At          string            `json:"at"`
	Actor       auditActorView    `json:"actor"`
	Source      string            `json:"source"`
	Action      audit.Action      `json:"action"`
	Target      string            `json:"target"`
	Result      audit.Result      `json:"result"`
	OperationID string            `json:"operationId,omitempty"`
	Category    audit.Category    `json:"category"`
	Count       uint64            `json:"count"`
	Metadata    map[string]string `json:"metadata"`
}

type auditPage struct {
	Items      []auditEventView `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func newAuditAPI(coordinator *securityCoordinator) http.Handler {
	return &auditAPI{coordinator: coordinator}
}

func (h *auditAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/audit/events" && r.URL.Path != "/api/v1/audit/export" {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r, "GET, HEAD")
		return
	}
	if !requireNoBody(w, r) {
		return
	}
	cursor, limit, values, ok := parsePageQuery(w, r, 100, "order")
	if !ok {
		return
	}
	// order=desc reads newest first, and its cursor pages towards older events.
	list := h.coordinator.audits.ListEvents
	switch values.Get("order") {
	case "", "asc":
	case "desc":
		list = h.coordinator.audits.ListEventsBefore
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid audit order.")
		return
	}
	var after uint64
	if cursor != "" {
		parsed, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil || strconv.FormatUint(parsed, 10) != cursor {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid audit cursor.")
			return
		}
		after = parsed
	}
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	events, err := list(r.Context(), after, limit)
	if err != nil {
		h.recordQueryFailure(r, actor, err)
		writeAuditError(w, r, err)
		return
	}
	hasNext := false
	if len(events) == limit && len(events) > 0 {
		next, nextErr := list(r.Context(), events[len(events)-1].ID, 1)
		if nextErr != nil {
			h.recordQueryFailure(r, actor, nextErr)
			writeAuditError(w, r, nextErr)
			return
		}
		hasNext = len(next) != 0
	}
	if _, err = h.coordinator.audits.Append(r.Context(), audit.Event{
		Actor: actor.Actor, Source: actor.Source, Action: audit.ActionAuditQueried,
		Result: audit.ResultSuccess, Category: audit.CategoryGeneral,
	}); err != nil {
		writeAuditError(w, r, err)
		return
	}
	page := auditPage{Items: make([]auditEventView, 0, len(events))}
	for _, event := range events {
		metadata := cloneStringMap(event.Metadata)
		page.Items = append(page.Items, auditEventView{
			ID: event.ID, At: event.At.UTC().Format(time.RFC3339Nano), Actor: auditActorView{Kind: event.Actor.Kind, ID: event.Actor.ID},
			Source: event.Source, Action: event.Action, Target: event.Target, Result: event.Result,
			OperationID: event.OperationID, Category: event.Category, Count: event.Count, Metadata: metadata,
		})
	}
	if hasNext {
		page.NextCursor = strconv.FormatUint(events[len(events)-1].ID, 10)
	}
	if r.URL.Path == "/api/v1/audit/export" {
		w.Header().Set("Content-Disposition", `attachment; filename="andaru-audit.json"`)
	}
	writeJSON(w, r, http.StatusOK, page)
}

func (h *auditAPI) recordQueryFailure(r *http.Request, actor requestActor, err error) {
	// A query refused as invalid is the caller's mistake, not the store failing.
	if errors.Is(err, audit.ErrInvalid) {
		return
	}
	_, _ = h.coordinator.audits.Append(r.Context(), audit.Event{
		Actor: actor.Actor, Source: actor.Source, Action: audit.ActionAuditQueried,
		Result: audit.ResultUnavailable, Category: audit.CategoryGeneral,
		Metadata: map[string]string{audit.MetadataReasonCode: audit.ReasonStorageFailure},
	})
}

func writeAuditError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, audit.ErrInvalid) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The audit query is invalid.")
		return
	}
	writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "The audit store is unavailable; inspect the engine journal.")
}

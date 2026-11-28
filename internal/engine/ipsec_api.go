package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

const (
	ipsecStatusPath = "/api/v1/status/vpn/ipsec"
	ipsecAPIPrefix  = "/api/v1/vpn/ipsec/"
	ipsecWait       = 40 * time.Second
)

// ipsecAPI serves the IPsec connections' status and their initiate and terminate actions.
type ipsecAPI struct {
	store    *config.Store
	audits   *audit.Store
	stateDir string
	// status runs andaru realize ipsec-status into file, which the engine created inside stateDir.
	status func(ctx context.Context, file string) error
	// control runs andaru realize ipsec-ctl action id.
	control func(ctx context.Context, action, id string) error
}

func (a *ipsecAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	only := func(methods ...string) bool {
		if slices.Contains(methods, r.Method) {
			return true
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return false
	}
	if r.URL.Path == ipsecStatusPath {
		if only(http.MethodGet, http.MethodHead) {
			a.statusView(w, r)
		}
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, ipsecAPIPrefix), "/")
	if len(parts) == 2 && parts[0] != "" && (parts[1] == "initiate" || parts[1] == "terminate") {
		if only(http.MethodPost) {
			a.act(w, r, parts[0], parts[1])
		}
		return
	}
	writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
}

type ipsecSAStatus struct {
	UniqueID       string                    `json:"uniqueId"`
	State          string                    `json:"state"`
	LocalHost      string                    `json:"localHost"`
	RemoteHost     string                    `json:"remoteHost"`
	EstablishedSec int64                     `json:"establishedSec"`
	ChildSAs       []generation.IPsecChildSA `json:"childSAs"`
}

type ipsecStatus struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Mode    string          `json:"mode"`
	Applied bool            `json:"applied"`
	Active  bool            `json:"active"`
	IKESAs  []ipsecSAStatus `json:"ikeSAs"`
}

// statusView joins strongSwan's SAs with the saved connections: each saved one with the SAs of its name, active
// while one of them is established.
func (a *ipsecAPI) statusView(w http.ResponseWriter, r *http.Request) {
	conns, err := a.store.IPsecConnections(r.Context())
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	runtime, err := a.runtime(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "IPsec status unavailable", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "STATUS_UNAVAILABLE", "The IPsec status is unavailable; inspect the engine journal.")
		return
	}
	items := []ipsecStatus{}
	for _, c := range conns {
		item := ipsecStatus{
			ID:      c.ID,
			Name:    c.Name,
			Mode:    c.Mode,
			Applied: slices.Contains(runtime.Connections, c.ID),
			IKESAs:  []ipsecSAStatus{},
		}
		for _, sa := range runtime.SAs {
			if sa.Name != c.ID {
				continue
			}
			children := sa.ChildSAs
			if children == nil {
				children = []generation.IPsecChildSA{}
			}
			s := ipsecSAStatus{UniqueID: sa.UniqueID, State: sa.State, LocalHost: sa.LocalHost, RemoteHost: sa.RemoteHost, EstablishedSec: sa.EstablishedSec, ChildSAs: children}
			item.Active = item.Active || sa.State == "ESTABLISHED"
			item.IKESAs = append(item.IKESAs, s)
		}
		items = append(items, item)
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"serviceActive": runtime.ServiceActive, "items": items})
}

// act initiates or terminates a saved, enabled connection the running strongSwan holds; it is audited.
func (a *ipsecAPI) act(w http.ResponseWriter, r *http.Request, id, action string) {
	conns, err := a.store.IPsecConnections(r.Context())
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	i := slices.IndexFunc(conns, func(c config.IPsecConnection) bool { return c.ID == id })
	if i < 0 {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "The connection does not exist.")
		return
	}
	switch {
	case !conns[i].Enabled:
		writeError(w, r, http.StatusConflict, "IPSEC_DISABLED", "The connection is disabled.")
		return
	case action == "initiate" && conns[i].Mode == "road-warrior":
		writeError(w, r, http.StatusConflict, "IPSEC_NOT_INITIATOR", "A road-warrior connection waits for its clients; it cannot be initiated.")
		return
	}
	runtime, err := a.runtime(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "IPsec status unavailable", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "STATUS_UNAVAILABLE", "The IPsec status is unavailable; inspect the engine journal.")
		return
	}
	switch {
	case !runtime.ServiceActive:
		writeError(w, r, http.StatusConflict, "IPSEC_SERVICE_STOPPED", "strongSwan is not running; apply an enabled connection first.")
		return
	case !slices.Contains(runtime.Connections, id):
		writeError(w, r, http.StatusConflict, "IPSEC_NOT_APPLIED", "The running strongSwan does not hold this connection; apply it first.")
		return
	}
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	auditAction := map[string]audit.Action{"initiate": audit.ActionIPsecInitiated, "terminate": audit.ActionIPsecTerminated}[action]
	// The intent is durable before strongSwan is told; an action audit storage cannot record does not happen.
	finish, err := beginAudit(r.Context(), a.audits, actor, auditAction, id)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Security audit storage is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ipsecWait)
	defer cancel()
	if err = a.control(ctx, action, id); err != nil {
		finish(audit.ResultFailure)
		slog.ErrorContext(r.Context(), "IPsec "+action+" failed", "connection", id, "error", err)
		writeError(w, r, http.StatusBadGateway, "IPSEC_CONTROL_FAILED", "strongSwan could not "+action+" the connection; inspect the journal.")
		return
	}
	finish(audit.ResultSuccess)
	writeJSON(w, r, http.StatusOK, map[string]bool{"ok": true})
}

// runtime asks andaru realize for strongSwan's state, through a file of the engine's own in its state directory.
func (a *ipsecAPI) runtime(ctx context.Context) (generation.IPsecStatus, error) {
	var status generation.IPsecStatus
	if a.status == nil || a.stateDir == "" {
		return status, errors.New("no realizer")
	}
	tmp, err := os.CreateTemp(a.stateDir, "ipsec-status-*.json")
	if err != nil {
		return status, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)
	ctx, cancel := context.WithTimeout(ctx, ipsecWait)
	defer cancel()
	if err = a.status(ctx, path); err != nil {
		return status, err
	}
	raw, err := readFileNoFollow(path, 4<<20)
	if err == nil {
		err = json.Unmarshal(raw, &status)
	}
	return status, err
}

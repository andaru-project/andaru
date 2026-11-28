package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

//go:embed openapi.json
var openAPIDocument []byte

type peerKey struct{}

type peerIdentity struct {
	uid      uint32
	verified bool
	web      bool
}

type linksResponse struct {
	Items []host.LinkStatus `json:"items"`
}

type lldpResponse struct {
	Items []host.LLDPNeighbor `json:"items"`
}

type offloadsResponse struct {
	Items []host.DeviceOffloads `json:"items"`
}

type errorDetail = config.FieldError

type errorBody struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details []errorDetail   `json:"details"`
	Current *config.Current `json:"current,omitempty"`
	// Findings explain a refused apply in the preview's own vocabulary.
	Findings []render.Finding `json:"findings,omitempty"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

// prefixesRoot is where the prefix view reads the host's files; tests point it elsewhere.
var prefixesRoot = ""

// leasesRoot is where the lease view reads the host's files; tests point it elsewhere.
var leasesRoot = ""

// leasesNow supplies the clock used to evaluate lease expiry; tests pin it.
var leasesNow = time.Now

// readLLDP reads live LLDP neighbors from systemd-networkd; tests swap it.
var readLLDP = host.ReadLLDPNeighbors

// readOffloads reads the live offloads of the Ethernet devices; tests swap it.
var readOffloads = host.ReadOffloads

// offloadsRoot is where the offload view reads recorded defaults and .link files; tests point it elsewhere.
var offloadsRoot = ""

// systemRoot is where the system description reads the kernel's files; tests point it elsewhere.
var systemRoot = ""

// systemResponse is the box's description beside the Andaru version that serves it.
type systemResponse struct {
	Version  string                    `json:"version"`
	SafeMode generation.SafeModeStatus `json:"safeMode"`
	host.SystemInfo
}

func newHandler(readLinks func(context.Context) ([]host.LinkStatus, error), configuration http.Handler) http.Handler {
	return newHandlerWithAPIs(readLinks, configuration, nil, nil)
}

func newHandlerWithAPIs(readLinks func(context.Context) ([]host.LinkStatus, error), configuration, identityAPI, auditAPI http.Handler) http.Handler {
	return newHandlerWithFleet(readLinks, configuration, identityAPI, auditAPI, nil)
}

func newHandlerWithFleet(readLinks func(context.Context) ([]host.LinkStatus, error), configuration, identityAPI, auditAPI, fleetAPI http.Handler) http.Handler {
	return newHandlerWithServices(readLinks, configuration, identityAPI, auditAPI, fleetAPI, nil)
}

func newHandlerWithServices(readLinks func(context.Context) ([]host.LinkStatus, error), configuration, identityAPI, auditAPI, fleetAPI, updateAPI http.Handler) http.Handler {
	return newHandlerWithAllServices(readLinks, configuration, identityAPI, auditAPI, fleetAPI, updateAPI, nil, nil, nil, nil)
}

type handlerDeps struct {
	readLinks        func(context.Context) ([]host.LinkStatus, error)
	configuration    http.Handler
	identityAPI      http.Handler
	auditAPI         http.Handler
	fleetAPI         http.Handler
	updateAPI        http.Handler
	setupCodeAPI     http.Handler
	hostnameAPI      http.Handler
	notificationsAPI http.Handler
	aliasesAPI       http.Handler
	addonsAPI        http.Handler
	pkiAPI           http.Handler
	acmeAPI          http.Handler
	wireguardAPI     http.Handler
	stateDir         string
}

func newHandlerWithAllServices(readLinks func(context.Context) ([]host.LinkStatus, error), configuration, identityAPI, auditAPI, fleetAPI, updateAPI, setupCodeAPI, hostnameAPI, notificationsAPI, aliasesAPI http.Handler) http.Handler {
	return newHandlerWithDeps(handlerDeps{
		readLinks:        readLinks,
		configuration:    configuration,
		identityAPI:      identityAPI,
		auditAPI:         auditAPI,
		fleetAPI:         fleetAPI,
		updateAPI:        updateAPI,
		setupCodeAPI:     setupCodeAPI,
		hostnameAPI:      hostnameAPI,
		notificationsAPI: notificationsAPI,
		aliasesAPI:       aliasesAPI,
	})
}

func newHandlerWithDeps(d handlerDeps) http.Handler {
	readLinks, configuration, identityAPI, auditAPI, fleetAPI, updateAPI, setupCodeAPI, hostnameAPI, notificationsAPI, aliasesAPI, addonsAPI, stateDir :=
		d.readLinks, d.configuration, d.identityAPI, d.auditAPI, d.fleetAPI, d.updateAPI, d.setupCodeAPI, d.hostnameAPI, d.notificationsAPI, d.aliasesAPI, d.addonsAPI, d.stateDir
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, ok := authorizeRequest(w, r); !ok {
			return
		}
		if (r.URL.Path == "/api/v1/capabilities" || r.URL.Path == "/api/v1/roles" || strings.HasPrefix(r.URL.Path, "/api/v1/users") || strings.HasPrefix(r.URL.Path, "/api/v1/tokens")) && identityAPI != nil {
			identityAPI.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/v1/setup/code" && setupCodeAPI != nil {
			setupCodeAPI.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/v1/system/hostname" && hostnameAPI != nil {
			hostnameAPI.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/audit/") && auditAPI != nil {
			auditAPI.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, fleetAPIPrefix+"/") && fleetAPI != nil {
			fleetAPI.ServeHTTP(w, r)
			return
		}
		if (r.URL.Path == notificationsPath || strings.HasPrefix(r.URL.Path, notificationsPath+"/")) && notificationsAPI != nil {
			notificationsAPI.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, aliasesPath+"/") && aliasesAPI != nil {
			aliasesAPI.ServeHTTP(w, r)
			return
		}
		if (r.URL.Path == updateAPIPrefix || strings.HasPrefix(r.URL.Path, updateAPIPrefix+"/")) && updateAPI != nil {
			updateAPI.ServeHTTP(w, r)
			return
		}
		if (r.URL.Path == addonsAPIPrefix || strings.HasPrefix(r.URL.Path, addonsAPIPrefix+"/")) && addonsAPI != nil {
			addonsAPI.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, pkiAPIPrefix+"/") && d.pkiAPI != nil {
			d.pkiAPI.ServeHTTP(w, r)
			return
		}
		if (r.URL.Path == acmeStatusPath || strings.HasPrefix(r.URL.Path, acmeOrdersPrefix)) && d.acmeAPI != nil {
			d.acmeAPI.ServeHTTP(w, r)
			return
		}
		if (r.URL.Path == wireguardStatusPath || strings.HasPrefix(r.URL.Path, wireguardAPIPrefix)) && d.wireguardAPI != nil {
			d.wireguardAPI.ServeHTTP(w, r)
			return
		}
		saved := r.URL.Path == "/api/v1/schema" || r.URL.Path == "/api/v1/config" || strings.HasPrefix(r.URL.Path, "/api/v1/config/") || r.URL.Path == "/api/v1/versions" || strings.HasPrefix(r.URL.Path, "/api/v1/versions/") || r.URL.Path == previewPath || r.URL.Path == "/api/v1/kept/recovery" || isBackupPath(r.URL.Path)
		if saved || isAppliesPath(r.URL.Path) {
			configuration.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/v1/status/links", "/api/v1/status/prefixes", "/api/v1/status/leases", "/api/v1/status/lldp", "/api/v1/status/offloads", "/api/v1/status/system", "/api/v1/status/usage", "/api/v1/status/traffic", "/api/v1/openapi.json":
		default:
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
		if r.URL.Path == "/api/v1/status/prefixes" {
			view, err := generation.ReadPrefixView(prefixesRoot)
			if err != nil {
				slog.ErrorContext(r.Context(), "delegated prefixes unavailable", "error", err)
				writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "The delegated prefixes are unavailable; inspect the engine journal.")
				return
			}
			writeJSON(w, r, http.StatusOK, view)
			return
		}
		if r.URL.Path == "/api/v1/status/leases" {
			view, err := generation.ReadLeaseView(leasesRoot, leasesNow())
			if err != nil {
				slog.ErrorContext(r.Context(), "DHCP leases unavailable", "error", err)
				writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "The DHCP leases are unavailable; inspect the engine journal.")
				return
			}
			writeJSON(w, r, http.StatusOK, view)
			return
		}
		if r.URL.Path == "/api/v1/status/lldp" {
			neighbors, err := readLLDP(r.Context())
			if err != nil {
				if r.Context().Err() == nil {
					slog.ErrorContext(r.Context(), "LLDP neighbors unavailable", "error", err)
				}
				writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "LLDP neighbors are unavailable; inspect the engine journal.")
				return
			}
			if neighbors == nil {
				neighbors = []host.LLDPNeighbor{}
			}
			writeJSON(w, r, http.StatusOK, lldpResponse{Items: neighbors})
			return
		}
		if r.URL.Path == "/api/v1/status/offloads" {
			devices, err := readOffloads(r.Context())
			if err != nil {
				if r.Context().Err() == nil {
					slog.ErrorContext(r.Context(), "device offloads unavailable", "error", err)
				}
				writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Device offloads are unavailable; inspect the engine journal.")
				return
			}
			if devices == nil {
				devices = []host.DeviceOffloads{}
			}
			if r.Method != http.MethodHead {
				generation.FillOffloadDefaults(offloadsRoot, devices)
			}
			writeJSON(w, r, http.StatusOK, offloadsResponse{Items: devices})
			return
		}
		if r.URL.Path == "/api/v1/status/traffic" {
			// since is the time of the newest reading a client holds: it is sent what came after. It is the one
			// parameter, once, with a value. ParseQuery, not URL.Query, which drops what it cannot parse.
			query, err := url.ParseQuery(r.URL.RawQuery)
			var since time.Time
			if values, given := query["since"]; err == nil && given {
				if len(values) == 1 {
					since, err = time.Parse(time.RFC3339Nano, values[0])
				} else {
					err = errors.New("since given more than once")
				}
				delete(query, "since")
			}
			if err != nil || len(query) != 0 {
				writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Only since is supported, once, as a time in the form the readings carry.")
				return
			}
			writeJSON(w, r, http.StatusOK, traffic.view(since))
			return
		}
		if r.URL.Path == "/api/v1/status/usage" {
			usage, err := host.ReadUsage(systemRoot, time.Now())
			if err != nil {
				slog.ErrorContext(r.Context(), "system usage unavailable", "error", err)
				writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "The system's usage is unavailable; inspect the engine journal.")
				return
			}
			writeJSON(w, r, http.StatusOK, usage)
			return
		}
		if r.URL.Path == "/api/v1/status/system" {
			info, err := host.ReadSystemInfo(systemRoot, time.Now())
			if err != nil {
				slog.ErrorContext(r.Context(), "system description unavailable", "error", err)
				writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "The system description is unavailable; inspect the engine journal.")
				return
			}
			writeJSON(w, r, http.StatusOK, systemResponse{Version: buildVersion, SafeMode: generation.ReadSafeMode(systemRoot, stateDir), SystemInfo: info})
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

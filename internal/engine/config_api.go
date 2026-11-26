package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/kept"
)

func newConfigurationHandler(store *config.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
		route := ""
		collection, id := "", ""
		var allowed []string
		switch {
		case len(parts) == 2 && parts[0] == "kept" && parts[1] == "recovery":
			if r.Method != http.MethodPost {
				writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST is required.")
				return
			}
			if _, err := os.Lstat(store.Directory()); err != nil {
				writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "The state directory is missing.")
				return
			}
			if err := writeRecovery(r.Context(), store); errors.Is(err, config.ErrUpgradePending) {
				writeConfigurationError(w, r, err)
				return
			} else if err != nil {
				slog.Error("store the recovery network", "error", err)
				writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "The recovery network was not stored.")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case len(parts) == 1 && parts[0] == "schema":
			route = "schema"
			allowed = []string{"GET", "HEAD"}
		case len(parts) == 1 && parts[0] == "config":
			route = "snapshot"
			allowed = []string{"GET", "HEAD"}
		case len(parts) == 2 && parts[0] == "config" && parts[1] == "changes":
			route = "changes"
			allowed = []string{"POST"}
		case len(parts) == 2 && parts[0] == "config" && parts[1] == "upgrade":
			// Finishing an upgrade: the answers saved, and below the questions with the choices given so far.
			route = "upgrade"
			allowed = []string{"POST"}
		case len(parts) == 3 && parts[0] == "config" && parts[1] == "upgrade" && parts[2] == "preview":
			route = "upgrade-preview"
			allowed = []string{"POST"}
		case len(parts) == 3 && parts[0] == "config" && parts[1] == "nat" && parts[2] == "outbound",
			len(parts) == 2 && parts[0] == "config" && (parts[1] == "management" || parts[1] == "dns"):
			// A singleton: one object without an ID, version-checked like any other.
			collection = strings.Join(parts[1:], ".")
			route, id = "singleton", collection
			allowed = []string{"GET", "HEAD", "PUT", "DELETE"}
		case len(parts) >= 2 && parts[0] == "config" && (parts[1] == "rules" ||
			len(parts) >= 3 && parts[1] == "nat" && (parts[2] == "port-forwards" || parts[2] == "one-to-one")):
			// An ordered list: its entries, and their order as a resource of its own.
			collection = "rules"
			rest := parts[2:]
			if parts[1] == "nat" {
				collection, rest = map[string]string{"port-forwards": "nat.portForwards", "one-to-one": "nat.oneToOne"}[parts[2]], parts[3:]
			}
			switch {
			case len(rest) == 0:
				route = "collection"
				allowed = []string{"GET", "HEAD", "POST"}
			case len(rest) == 1 && rest[0] == "order":
				route, id = "order", "order"
				allowed = []string{"PUT"}
			case len(rest) == 1 && rest[0] != "":
				route, id = "object", rest[0]
				allowed = []string{"GET", "HEAD", "PUT", "DELETE"}
			}
		case len(parts) == 3 && parts[0] == "config" && parts[1] == "aliases" && parts[2] == "search":
			route = "aliases-search"
			allowed = []string{"GET", "HEAD"}
		case len(parts) >= 3 && parts[0] == "config" && (parts[1] == "aliases" && (parts[2] == "addresses" || parts[2] == "ports") ||
			parts[1] == "notifications" && parts[2] == "channels"):
			collection = parts[1] + "." + parts[2]
			if len(parts) == 3 {
				route = "collection"
				allowed = []string{"GET", "HEAD", "POST"}
			} else if len(parts) == 4 && parts[3] != "" {
				route, id = "object", parts[3]
				allowed = []string{"GET", "HEAD", "PUT", "DELETE"}
			}
		case len(parts) >= 2 && parts[0] == "config" && slices.Contains([]string{"links", "segments", "zones", "roles", "dhcp", "dhcp6", "virtualAddresses"}, parts[1]):
			collection = parts[1]
			if len(parts) == 2 {
				route = "collection"
				allowed = []string{"GET", "HEAD", "POST"}
			} else if len(parts) == 3 && parts[2] != "" {
				route = "object"
				id = parts[2]
				allowed = []string{"GET", "HEAD", "PUT", "DELETE"}
			}
		case len(parts) == 1 && parts[0] == "versions":
			route = "history"
			allowed = []string{"GET", "HEAD"}
		case len(parts) == 2 && parts[0] == "versions" && parts[1] != "":
			route = "version"
			id = parts[1]
			allowed = []string{"GET", "HEAD"}
		}
		if route == "" {
			writeError(w, r, 404, "NOT_FOUND", "Resource not found.")
			return
		}
		if !slices.Contains(allowed, r.Method) {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeError(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		send := func(status int, value any, e error) {
			if e != nil {
				writeConfigurationError(w, r, e)
				return
			}
			writeJSON(w, r, status, value)
		}
		if route == "schema" {
			w.Header().Set("Content-Type", "application/schema+json; charset=utf-8")
			w.WriteHeader(200)
			if r.Method != "HEAD" {
				_, _ = w.Write(config.SchemaBundle)
			}
			return
		}
		if r.Method == "GET" || r.Method == "HEAD" {
			switch route {
			case "snapshot":
				v, e := store.Snapshot(r.Context())
				send(200, v, e)
			case "collection":
				v, e := store.List(r.Context(), collection)
				if e == nil && v.Version != "" {
					w.Header().Set("ETag", `"order.`+string(v.Version)+`"`)
				}
				send(200, v, e)
			case "object", "singleton":
				v, head, e := store.Get(r.Context(), collection, id)
				if e == nil {
					w.Header().Set("ETag", `"`+id+"."+string(v.Version)+`"`)
					w.Header().Set("Andaru-Saved-Version", string(head))
				}
				send(200, v, e)
			case "aliases-search":
				values, parseErr := url.ParseQuery(r.URL.RawQuery)
				if parseErr != nil {
					writeError(w, r, 400, "INVALID_REQUEST", "Invalid alias search query encoding.")
					return
				}
				q, msg := parseAliasSearchQuery(values)
				if msg != "" {
					writeError(w, r, 400, "INVALID_REQUEST", msg)
					return
				}
				v, e := store.SearchAliases(r.Context(), q)
				send(200, v, e)
			case "history":
				limit := 50
				var before *config.Version
				for key, values := range r.URL.Query() {
					if (key != "limit" && key != "before") || len(values) != 1 {
						writeError(w, r, 400, "INVALID_REQUEST", "Invalid history query.")
						return
					}
				}
				if v, ok := r.URL.Query()["limit"]; ok {
					n, e := strconv.Atoi(v[0])
					if e != nil {
						writeError(w, r, 400, "INVALID_REQUEST", "Invalid history limit.")
						return
					}
					limit = n
				}
				if v, ok := r.URL.Query()["before"]; ok {
					n, e := config.ParseVersion(v[0])
					if e != nil {
						writeError(w, r, 400, "INVALID_REQUEST", "Invalid history cursor.")
						return
					}
					before = &n
				}
				v, e := store.History(r.Context(), before, limit)
				send(200, v, e)
			case "version":
				version, e := config.ParseVersion(id)
				if e != nil {
					writeError(w, r, 400, "INVALID_REQUEST", "Invalid saved version.")
					return
				}
				v, e := store.Version(r.Context(), version)
				send(200, v, e)
			}
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if e != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(e, &tooLarge) {
				writeError(w, r, 413, "REQUEST_TOO_LARGE", "Request body exceeds its size limit.")
			} else {
				writeError(w, r, 400, "INVALID_REQUEST", "Could not read request body.")
			}
			return
		}
		if route == "upgrade" || route == "upgrade-preview" {
			var request struct {
				Choices              map[string]string `json:"choices"`
				ExpectedSavedVersion config.Version    `json:"expectedSavedVersion"`
			}
			if !strictJSON(raw, &request, "choices", "expectedSavedVersion") {
				writeError(w, r, 400, "INVALID_REQUEST", "Expected choices and, to save them, expectedSavedVersion.")
				return
			}
			if route == "upgrade-preview" {
				v, e := store.UpgradePreview(r.Context(), request.Choices)
				send(200, v, e)
				return
			}
			actor, ok := actorFromRequest(r)
			if !ok {
				writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
				return
			}
			result, e := store.Upgrade(r.Context(), actor.Actor, request.ExpectedSavedVersion, request.Choices)
			if e == nil {
				w.Header().Set("Andaru-Saved-Version", string(result.SavedVersion))
			}
			send(200, result, e)
			return
		}
		var changes config.Changes
		if route == "changes" {
			changes, e = config.DecodeChanges(raw)
		} else {
			op := "create"
			var version *config.Version
			if route != "collection" {
				op = map[string]string{"PUT": "replace", "DELETE": "delete"}[r.Method]
				if route == "order" {
					op = "order"
				}
				version, e = objectPrecondition(id, r.Header.Values("If-Match"), route != "object")
			}
			target := id
			if route == "singleton" || route == "order" {
				target = "" // the store versions these by collection
			}
			if e == nil {
				changes, e = config.ObjectChange(op, collection, target, version, raw)
			}
			if e == nil && route == "collection" && r.URL.Query().Has("before") {
				changes, e = changes.Before(r.URL.Query().Get("before"))
			}
		}
		if e != nil {
			writeConfigurationError(w, r, e)
			return
		}
		actor, ok := actorFromRequest(r)
		if !ok {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
			return
		}
		// The channels are the notifications' settings: whichever route carries a change to them, it takes
		// notifications.manage, and configuration.read too to create or replace one, since a channel is sent the applies
		// and upgrade decisions only that capability sees. shortcut: checked when a channel is saved, not when its
		// saver later loses the capability, nor for other capabilities a notification may require; check per raise if
		// channels get owners.
		channelOp := func(o config.Operation) bool { return o.Collection == "notifications.channels" }
		sends := func(o config.Operation) bool { return channelOp(o) && o.Op != "delete" } // deleting one sends nothing
		if slices.ContainsFunc(changes.Operations, channelOp) && !hasCapability(actor, authz.NotificationsManage) ||
			slices.ContainsFunc(changes.Operations, sends) && !hasCapability(actor, authz.ConfigurationRead) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Changing notification channels requires notifications.manage, and configuration.read unless deleting.")
			return
		}
		result, e := store.Save(r.Context(), actor.Actor, changes)
		if e != nil {
			writeConfigurationError(w, r, e)
			return
		}
		if err := recordLiveLAN(r.Context(), store, actor.Actor); err != nil {
			slog.Error("record the last LAN", "error", err)
		}
		w.Header().Set("Andaru-Saved-Version", string(result.SavedVersion))
		if route == "changes" {
			send(200, result, nil)
			return
		}
		if route == "order" {
			// Built from this save alone: re-reading could return a later save's list.
			var order []string
			for _, item := range result.Items {
				if item.Collection == collection && item.ID == collection && json.Unmarshal(raw, &order) == nil {
					w.Header().Set("ETag", `"order.`+string(item.Version)+`"`)
					send(200, struct {
						State        string         `json:"state"`
						SavedVersion config.Version `json:"savedVersion"`
						Version      config.Version `json:"version"`
						Order        []string       `json:"order"`
					}{"saved", result.SavedVersion, item.Version, order}, nil)
					return
				}
			}
			writeError(w, r, 503, "STORE_UNAVAILABLE", "Saved response is unavailable; read current state before retrying.")
			return
		}
		if r.Method == "DELETE" {
			send(200, struct {
				State        string         `json:"state"`
				SavedVersion config.Version `json:"savedVersion"`
			}{"saved", result.SavedVersion}, nil)
			return
		}
		status := 200
		if r.Method == "POST" {
			status = 201
			id = result.Created["object"]
			// The path the client used, which for the NAT lists is not the dotted collection name.
			w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+id)
		}
		for _, item := range result.Items {
			if item.Collection == collection && item.ID == id {
				send(status, config.ObjectView{Value: item.Value, Version: item.Version}, nil)
				return
			}
		}
		writeError(w, r, 503, "STORE_UNAVAILABLE", "Saved response is unavailable; read current state before retrying.")
	})
}
func objectPrecondition(id string, tags []string, whole bool) (*config.Version, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	if len(tags) != 1 {
		return nil, &config.Fault{Code: "INVALID_REQUEST"}
	}
	tag := tags[0]
	if tag == "" {
		return nil, nil
	}
	if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return nil, &config.Fault{Code: "INVALID_REQUEST"}
	}
	inner := tag[1 : len(tag)-1]
	if strings.ContainsAny(inner, "\" ,\r\n\t") {
		return nil, &config.Fault{Code: "INVALID_REQUEST"}
	}
	// Singleton tags are named after their collection, which may itself contain a dot.
	at := strings.LastIndex(inner, ".")
	identity, number, ok := inner[:max(at, 0)], inner[at+1:], at > 0
	if !ok || identity == "" {
		return nil, &config.Fault{Code: "INVALID_REQUEST"}
	}
	v, e := config.ParseVersion(number)
	if e != nil {
		return nil, &config.Fault{Code: "INVALID_REQUEST"}
	}
	if identity != id {
		if whole {
			// A singleton's or list's tag names that resource alone; another's must not pass as "unset".
			return nil, &config.Fault{Code: "PRECONDITION_FAILED"}
		}
		v = "0"
	}
	return &v, nil
}
func writeConfigurationError(w http.ResponseWriter, r *http.Request, e error) {
	var f *config.Fault
	if !errors.As(e, &f) {
		writeError(w, r, 503, "STORE_UNAVAILABLE", "Configuration storage is unavailable; inspect the engine journal.")
		return
	}
	status := 503
	switch f.Code {
	case "INVALID_REQUEST":
		status = 400
	case "FORBIDDEN":
		status = 403
	case "NOT_FOUND":
		status = 404
	case "PRECONDITION_FAILED":
		status = 412
	case "SETTINGS_UPGRADE_PENDING":
		status = 409
	case "REQUEST_TOO_LARGE":
		status = 413
	case "VALIDATION_FAILED":
		status = 422
	case "PRECONDITION_REQUIRED":
		status = 428
	}
	details := f.Details
	if details == nil {
		details = []config.FieldError{}
	}
	writeJSON(w, r, status, errorResponse{Error: errorBody{Code: f.Code, Message: http.StatusText(status), Details: details, Current: f.Current}})
}

func recordLiveLAN(ctx context.Context, store *config.Store, actor authz.Actor) error {
	dir := generation.Dir(store.Directory())
	p, err := generation.ReadPointers(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if p.Live == nil {
		return nil
	}
	g, err := generation.Read(dir, *p.Live)
	if err != nil {
		return err
	}
	body, err := store.ManagementLAN(ctx, g.SavedVersion)
	return kept.RecordLiveLAN(ctx, kept.Root, store.Directory(), actor, body, err)
}

// recoveryKeptRoot is the test seam. Production leaves it empty.
var recoveryKeptRoot string

func writeRecovery(ctx context.Context, store *config.Store) error {
	input, err := store.ApplyInput(ctx, nil)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Declaration json.RawMessage   `json:"declaration"`
		Secrets     map[string]string `json:"secrets"`
	}{input.Value, input.Secrets})
	if err != nil {
		return err
	}
	var keptStore *kept.Store
	if recoveryKeptRoot != "" {
		keptStore, err = kept.OpenIn(recoveryKeptRoot, store.Directory())
	} else {
		keptStore, err = kept.Open(store.Directory())
	}
	if err != nil {
		return err
	}
	return keptStore.Put(ctx, "recovery", body, true)
}

func parseAliasSearchQuery(values url.Values) (config.AliasSearchQuery, string) {
	allowed := map[string]bool{
		"type": true, "q": true, "kind": true, "family": true,
		"excludeGroups": true, "exclude": true, "limit": true,
	}
	for key, vals := range values {
		if !allowed[key] || len(vals) != 1 {
			return config.AliasSearchQuery{}, "Invalid alias search query."
		}
	}
	out := config.AliasSearchQuery{
		Type:    values.Get("type"),
		Query:   values.Get("q"),
		Kind:    values.Get("kind"),
		Family:  values.Get("family"),
		Exclude: values.Get("exclude"),
		Limit:   20,
	}
	if out.Type != "addresses" && out.Type != "ports" {
		return config.AliasSearchQuery{}, "Alias search type must be addresses or ports."
	}
	if !utf8.ValidString(out.Query) || utf8.RuneCountInString(out.Query) > 256 {
		return config.AliasSearchQuery{}, "Alias search query is invalid or too long."
	}
	if !utf8.ValidString(out.Exclude) || utf8.RuneCountInString(out.Exclude) > 128 {
		return config.AliasSearchQuery{}, "Alias search exclude ID is invalid or too long."
	}
	if out.Kind != "" {
		validKinds := []string{"host", "network", "range", "group", "url", "hostnames"}
		if out.Type == "ports" {
			validKinds = []string{"tcp", "udp", "icmp", "icmpv6", "group"}
		}
		if !slices.Contains(validKinds, out.Kind) {
			return config.AliasSearchQuery{}, "Invalid alias kind for this search type."
		}
	}
	if out.Family != "" {
		if out.Type != "addresses" || (out.Family != "ipv4" && out.Family != "ipv6") {
			return config.AliasSearchQuery{}, "Invalid alias family filter."
		}
	}
	if raw, ok := values["excludeGroups"]; ok {
		switch raw[0] {
		case "true":
			out.ExcludeGroups = true
		case "false":
			out.ExcludeGroups = false
		default:
			return config.AliasSearchQuery{}, "Invalid excludeGroups flag."
		}
	}
	if raw, ok := values["limit"]; ok {
		n, err := strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > 50 {
			return config.AliasSearchQuery{}, "Alias search limit must be between 1 and 50."
		}
		out.Limit = n
	}
	return out, ""
}

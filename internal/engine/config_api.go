package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/config"
)

func newConfigurationHandler(store *config.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
		route := ""
		collection, id := "", ""
		var allowed []string
		switch {
		case len(parts) == 1 && parts[0] == "schema":
			route = "schema"
			allowed = []string{"GET", "HEAD"}
		case len(parts) == 1 && parts[0] == "config":
			route = "snapshot"
			allowed = []string{"GET", "HEAD"}
		case len(parts) == 2 && parts[0] == "config" && parts[1] == "changes":
			route = "changes"
			allowed = []string{"POST"}
		case len(parts) == 3 && parts[0] == "config" && parts[1] == "nat" && parts[2] == "outbound",
			len(parts) == 2 && parts[0] == "config" && (parts[1] == "management" || parts[1] == "dns"):
			// A singleton: one object without an ID, version-checked like any other.
			collection = strings.Join(parts[1:], ".")
			route, id = "singleton", collection
			allowed = []string{"GET", "HEAD", "PUT", "DELETE"}
		case len(parts) >= 3 && parts[0] == "config" && (parts[1] == "policy" && (parts[2] == "input" || parts[2] == "forward") ||
			parts[1] == "nat" && (parts[2] == "port-forwards" || parts[2] == "one-to-one")):
			collection = map[string]string{"input": "policy.input", "forward": "policy.forward", "port-forwards": "nat.portForwards", "one-to-one": "nat.oneToOne"}[parts[2]]
			switch {
			case len(parts) == 3:
				route = "collection"
				allowed = []string{"GET", "HEAD", "POST"}
			case len(parts) == 4 && parts[3] == "order":
				route, id = "order", "order"
				allowed = []string{"PUT"}
			case len(parts) == 4 && parts[3] != "":
				route, id = "object", parts[3]
				allowed = []string{"GET", "HEAD", "PUT", "DELETE"}
			}
		case len(parts) >= 2 && parts[0] == "config" && slices.Contains([]string{"links", "segments", "zones", "addresses", "services", "dhcp"}, parts[1]):
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
		peer, _ := r.Context().Value(peerKey{}).(peerIdentity)
		result, e := store.Save(r.Context(), fmt.Sprintf("unix:uid:%d", peer.uid), changes)
		if e != nil {
			writeConfigurationError(w, r, e)
			return
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

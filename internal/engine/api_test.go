package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

func rootRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	return r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 0, verified: true}))
}

func TestAPILinksContractAndFailures(t *testing.T) {
	h := newHandler(func(context.Context) ([]host.LinkStatus, error) {
		return []host.LinkStatus{{
			IfIndex: 2, Name: "eth0", LinkType: "ether", MTU: 1500, OperState: "UP",
			Flags: []string{"UP"}, LinkAddress: "02:00:00:00:00:02",
			Addresses: []host.AddressStatus{{Family: "ipv4", Address: "192.0.2.7", PrefixLength: 24, Scope: "global"}},
		}}, nil
	}, http.NotFoundHandler())
	status := httptest.NewRecorder()
	h.ServeHTTP(status, rootRequest(http.MethodGet, "/api/v1/status/links"))
	if status.Code != http.StatusOK || status.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(status.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("wrong success contract: %d %v %s", status.Code, status.Header(), status.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("missing items array: %#v", body)
	}
	link := items[0].(map[string]any)
	if link["name"] != "eth0" || link["ifIndex"] != float64(2) || link["mtu"] != float64(1500) || link["linkAddress"] != "02:00:00:00:00:02" {
		t.Fatalf("incorrect observed metadata: %#v", link)
	}
	address := link["addresses"].([]any)[0].(map[string]any)
	if address["address"] != "192.0.2.7" || address["prefixLength"] != float64(24) {
		t.Fatalf("address was network-masked or lost its prefix: %#v", address)
	}

	doc := httptest.NewRecorder()
	h.ServeHTTP(doc, rootRequest(http.MethodGet, "/api/v1/openapi.json"))
	if doc.Code != http.StatusOK {
		t.Fatalf("contract unavailable: %d %s", doc.Code, doc.Body)
	}
	var openapi struct {
		OpenAPI    string                     `json:"openapi"`
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc.Body.Bytes(), &openapi); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(openapi.OpenAPI, "3.1.") || openapi.Paths["/api/v1/status/links"] == nil || openapi.Paths["/api/v1/openapi.json"] == nil {
		t.Fatalf("published paths/version do not describe this API: %s", doc.Body)
	}
	for _, path := range []string{"/api/v1/status/links", "/api/v1/openapi.json"} {
		raw := openapi.Paths[path]
		var operations map[string]json.RawMessage
		if err := json.Unmarshal(raw, &operations); err != nil {
			t.Fatal(err)
		}
		if operations["get"] == nil || operations["head"] == nil || operations["post"] != nil {
			t.Fatalf("incorrect operations for %s: %s", path, raw)
		}
	}
	assertRequiredSchema(t, openapi.Components.Schemas["LinksResponse"], body, []string{"items"})
	assertRequiredSchema(t, openapi.Components.Schemas["LinkStatus"], link, []string{"ifIndex", "name", "linkType", "mtu", "operState", "flags", "addresses"})
	assertRequiredSchema(t, openapi.Components.Schemas["AddressStatus"], address, []string{"family", "address", "prefixLength", "scope"})

	for _, path := range []string{"/api/v1/status/links", "/api/v1/openapi.json"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, rootRequest(http.MethodHead, path))
		if w.Code != http.StatusOK || w.Body.Len() != 0 {
			t.Fatalf("HEAD did not follow GET without a body: %s %d %s", path, w.Code, w.Body)
		}
	}

	for _, tc := range []struct {
		method, path, code string
		status             int
	}{
		{http.MethodPost, "/api/v1/status/links", "METHOD_NOT_ALLOWED", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/v1/openapi.json", "METHOD_NOT_ALLOWED", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/absent", "NOT_FOUND", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, rootRequest(tc.method, tc.path))
		assertAPIError(t, w, tc.status, tc.code)
		if tc.status == http.StatusMethodNotAllowed && w.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("missing supported methods: %v", w.Header())
		}
	}
	failed := newHandler(func(context.Context) ([]host.LinkStatus, error) {
		return nil, errors.New("private /root/path and tool stderr")
	}, http.NotFoundHandler())
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := httptest.NewRecorder()
		failed.ServeHTTP(w, rootRequest(method, "/api/v1/status/links"))
		if method == http.MethodGet {
			assertAPIError(t, w, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE")
			if strings.Contains(w.Body.String(), "/root/") || strings.Contains(w.Body.String(), "tool stderr") || strings.Contains(w.Body.String(), "items") {
				t.Fatalf("private failure became leaked or partial success: %s", w.Body)
			}
		} else if w.Code != http.StatusServiceUnavailable || w.Body.Len() != 0 {
			t.Fatalf("failed HEAD lost failure status or emitted a body: %d %s", w.Code, w.Body)
		}
	}
}

func assertRequiredSchema(t *testing.T, raw json.RawMessage, value map[string]any, required []string) {
	t.Helper()
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	slices.Sort(schema.Required)
	want := slices.Clone(required)
	slices.Sort(want)
	if !reflect.DeepEqual(schema.Required, want) {
		t.Fatalf("wrong published required fields: got=%v want=%v", schema.Required, want)
	}
	for _, name := range required {
		field, exists := value[name]
		if !exists || field == nil {
			t.Fatalf("documented field %s missing/null: %#v", name, value)
		}
		switch schema.Properties[name].Type {
		case "integer":
			n, ok := field.(float64)
			if !ok || n != float64(int(n)) {
				t.Fatalf("field %s is not an integer: %#v", name, field)
			}
		case "string":
			if _, ok := field.(string); !ok {
				t.Fatalf("field %s is not a string: %#v", name, field)
			}
		case "array":
			if _, ok := field.([]any); !ok {
				t.Fatalf("field %s is not an array: %#v", name, field)
			}
		default:
			t.Fatalf("missing/unsupported published type for %s: %q", name, schema.Properties[name].Type)
		}
	}
}

func assertAPIError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("wrong status: %d, body=%s", w.Code, w.Body)
	}
	var body struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code || body.Error.Message == "" || body.Error.Details == nil {
		t.Fatalf("wrong error envelope: %s", w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("wrong error headers: %v", w.Header())
	}
}

func TestAPIDeniesUnverifiedPeerBeforeRead(t *testing.T) {
	for _, identity := range []struct {
		name  string
		value any
	}{
		{"absent", nil}, {"nonroot", peerIdentity{uid: 65534, verified: true}}, {"unverified zero", peerIdentity{}},
	} {
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/status/links"}, {http.MethodPost, "/api/v1/status/links"},
			{http.MethodGet, "/api/v1/unknown"}, {http.MethodGet, "/api/v1/openapi.json"},
		} {
			t.Run(identity.name+" "+route.method+" "+route.path, func(t *testing.T) {
				reads := 0
				h := newHandler(func(context.Context) ([]host.LinkStatus, error) { reads++; return []host.LinkStatus{}, nil }, http.NotFoundHandler())
				r := httptest.NewRequest(route.method, route.path, nil)
				if identity.value != nil {
					r = r.WithContext(context.WithValue(r.Context(), peerKey{}, identity.value))
				}
				r.Header.Set("X-UID", "0")
				r.Header.Set("X-User", "root")
				r.Header.Set("Authorization", "Bearer root")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				assertAPIError(t, w, http.StatusForbidden, "FORBIDDEN")
				if reads != 0 {
					t.Fatal("unauthorized peer triggered host inventory")
				}
			})
		}
	}
}

func TestAPIEmptySnapshotIsAnArray(t *testing.T) {
	h := newHandler(func(context.Context) ([]host.LinkStatus, error) { return nil, nil }, http.NotFoundHandler())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, rootRequest(http.MethodGet, "/api/v1/status/links"))
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || body.Items == nil || len(body.Items) != 0 {
		t.Fatalf("empty snapshot was not an array: %d %s", w.Code, w.Body)
	}
}

func TestAPIPropagatesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := newHandler(func(ctx context.Context) ([]host.LinkStatus, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("inventory received a detached context")
		}
		return nil, ctx.Err()
	}, http.NotFoundHandler())
	r := rootRequest(http.MethodGet, "/api/v1/status/links").WithContext(context.WithValue(ctx, peerKey{}, peerIdentity{uid: 0, verified: true}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertAPIError(t, w, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE")
}

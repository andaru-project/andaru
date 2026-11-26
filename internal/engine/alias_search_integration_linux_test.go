//go:build integration

package engine

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/andaru-project/andaru/internal/config"
)

func TestAliasSearchEndpoint(t *testing.T) {
	h, _ := configurationFixture(t)

	for _, body := range []string{
		`{"name":"web-servers","kind":"network","prefix":"192.168.10.0/24"}`,
		`{"name":"web","kind":"host","address":"192.168.10.20"}`,
		`{"name":"ipv6-lan","kind":"network","prefix":"2001:db8:1::/64"}`,
	} {
		if w := configCall(h, http.MethodPost, "/api/v1/config/aliases/addresses", body, ""); w.Code != http.StatusCreated {
			t.Fatalf("create address alias failed: %d %s", w.Code, w.Body.String())
		}
	}
	if w := configCall(h, http.MethodPost, "/api/v1/config/aliases/ports", `{"name":"HTTPS","protocol":"tcp","ports":["443"]}`, ""); w.Code != http.StatusCreated {
		t.Fatalf("create port alias failed: %d %s", w.Code, w.Body.String())
	}

	// Ranked similarity search on addresses.
	w := configCall(h, http.MethodGet, "/api/v1/config/aliases/search?type=addresses&q=web", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET search = %d %s", w.Code, w.Body.String())
	}
	var res config.AliasSearchView
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.State != "saved" || len(res.Items) != 2 || res.Items[0].Name != "web" || res.Items[1].Name != "web-servers" {
		t.Fatalf("unexpected address search result: %+v", res)
	}

	// Family filter excludes IPv4 aliases when family=ipv6.
	w = configCall(h, http.MethodGet, "/api/v1/config/aliases/search?type=addresses&family=ipv6", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET ipv6 search = %d %s", w.Code, w.Body.String())
	}
	res = config.AliasSearchView{}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "ipv6-lan" {
		t.Fatalf("unexpected ipv6 result: %+v", res)
	}

	// HEAD returns 200 with empty body.
	w = configCall(h, http.MethodHead, "/api/v1/config/aliases/search?type=ports&q=443", "", "")
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD search = %d len=%d", w.Code, w.Body.Len())
	}

	// POST returns 405 Method Not Allowed.
	w = configCall(h, http.MethodPost, "/api/v1/config/aliases/search?type=ports", `{}`, "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST search = %d, want 405", w.Code)
	}

	// Bad type returns 400.
	w = configCall(h, http.MethodGet, "/api/v1/config/aliases/search?type=invalid", "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid type = %d, want 400", w.Code)
	}
}

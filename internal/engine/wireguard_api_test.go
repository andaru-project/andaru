package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/config"
)

func TestWireGuardKeypair(t *testing.T) {
	a := &wireguardAPI{}
	call := func(method, body string) *httptest.ResponseRecorder {
		r := rootRequest(method, wireguardAPIPrefix+"keypair")
		r.Body = http.NoBody
		if body != "" {
			r = httptest.NewRequest(method, wireguardAPIPrefix+"keypair", strings.NewReader(body))
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct {
		body string
		psk  bool
	}{{"", false}, {`{}`, false}, {`{"includePsk":true}`, true}} {
		w := call(http.MethodPost, c.body)
		var got map[string]string
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("%q: %d %s", c.body, w.Code, w.Body)
		}
		if public, err := config.WireGuardPublicKey(got["privateKey"]); err != nil || public != got["publicKey"] {
			t.Fatalf("%q: pair does not match: %v %+v", c.body, err, got)
		}
		if _, has := got["presharedKey"]; has != c.psk || c.psk && len(got["presharedKey"]) != 44 {
			t.Fatalf("%q: psk %+v", c.body, got)
		}
	}
	if w := call(http.MethodGet, ""); w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatalf("GET: %d", w.Code)
	}
	if w := call(http.MethodPost, `{"bits":256}`); w.Code != 400 {
		t.Fatalf("unknown field: %d %s", w.Code, w.Body)
	}
}

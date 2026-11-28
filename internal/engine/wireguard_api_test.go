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
	if w := call(http.MethodPost, `{}}`); w.Code != 400 {
		t.Fatalf("trailing closing brace: %d %s", w.Code, w.Body)
	}
}

func TestFormatClientEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"":                         "<ENDPOINT>:51820",
		"vpn.example.com":          "vpn.example.com:51820",
		"vpn.example.com:443":      "vpn.example.com:443",
		"203.0.113.10":             "203.0.113.10:51820",
		"203.0.113.10:51821":       "203.0.113.10:51821",
		"2001:db8::1":              "[2001:db8::1]:51820",
		"[2001:db8::1]":            "[2001:db8::1]:51820",
		"[2001:db8::1]:443":        "[2001:db8::1]:443",
	} {
		got, ok := formatClientEndpoint(in, 51820)
		if !ok || got != want {
			t.Errorf("%q: got (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	for _, bad := range []string{
		"vpn.example.com:abc",
		"vpn.example.com:0",
		"vpn.example.com:65536",
		"vpn.example.com:+51820",
		"vpn.example.com:051820",
		":51820",
		"[2001:db8::1",
		"[192.0.2.1]:51820",
		"[vpn.example.com]:51820",
		"[::ffff:192.0.2.1]:51820",
		"10.0.0.1#comment",
		"a:b:c",
		"-bad.example.com",
	} {
		if got, ok := formatClientEndpoint(bad, 51820); ok {
			t.Errorf("bad endpoint %q accepted as %q", bad, got)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewHTTPClientRefusesCrossHostRedirect(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		if r.Header.Get("X-API-Key") != "" {
			t.Errorf("other host received X-API-Key")
		}
	}))
	defer other.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			http.Redirect(w, r, "/ok", http.StatusFound)
			return
		}
		if r.URL.Path == "/ok" {
			return
		}
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer target.Close()

	c := NewHTTPClient(TargetConfig{}, 5*time.Second)
	defer c.CloseIdleConnections()

	req, _ := http.NewRequest(http.MethodGet, target.URL+"/api", nil)
	req.Header.Set("X-API-Key", "secret")
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("expected cross-host redirect to be refused")
	}
	if n := otherHits.Load(); n != 0 {
		t.Fatalf("other host hit %d times", n)
	}

	resp, err := c.Get(target.URL + "/same")
	if err != nil {
		t.Fatalf("same-host redirect: %v", err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/ok" {
		t.Fatalf("same-host redirect not followed: %s", resp.Request.URL.Path)
	}
}

func TestNewHTTPClientRefusesHTTPSDowngrade(t *testing.T) {
	c := NewHTTPClient(TargetConfig{}, time.Second)
	from, _ := http.NewRequest(http.MethodGet, "https://192.0.2.1/api", nil)
	to, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/api", nil)
	if err := c.CheckRedirect(to, []*http.Request{from}); err == nil {
		t.Fatal("expected https -> http redirect to be refused")
	}
}

func TestNewHTTPClientRefusesDowngradeAfterUpgrade(t *testing.T) {
	c := NewHTTPClient(TargetConfig{}, time.Second)
	first, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/", nil)
	second, _ := http.NewRequest(http.MethodGet, "https://192.0.2.1/login", nil)
	third, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/api", nil)
	if err := c.CheckRedirect(second, []*http.Request{first}); err != nil {
		t.Fatalf("http -> https upgrade refused: %v", err)
	}
	if err := c.CheckRedirect(third, []*http.Request{first, second}); err == nil {
		t.Fatal("expected https -> http hop to be refused")
	}
}

func TestSameHost(t *testing.T) {
	for _, tc := range []struct {
		next, first string
		want        bool
	}{
		{"https://Router:443/login", "http://router/", true},
		{"https://router/login", "https://router:443/", true},
		{"http://router:8080/", "http://router/", false},
		{"https://evil/", "https://router/", false},
	} {
		n, _ := url.Parse(tc.next)
		f, _ := url.Parse(tc.first)
		if got := sameHost(n, f); got != tc.want {
			t.Errorf("sameHost(%s, %s) = %v, want %v", tc.next, tc.first, got, tc.want)
		}
	}
}

func TestCheckCredentialTransport(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		settings map[string]any
		wantErr  bool
	}{
		{"http://192.0.2.1", nil, true},
		{"HTTP://192.0.2.1", nil, true},
		{"http://192.0.2.1", map[string]any{"allow_insecure_http": true}, false},
		{"https://192.0.2.1", nil, false},
		{"unix:///run/andaru/engine.sock", nil, false},
	} {
		err := CheckCredentialTransport(TargetConfig{ID: "t", Endpoint: tc.endpoint, Settings: tc.settings})
		if (err != nil) != tc.wantErr {
			t.Errorf("%s %v: err = %v, wantErr %v", tc.endpoint, tc.settings, err, tc.wantErr)
		}
	}
}

func TestDoJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// OPNsense rejects an empty body sent as JSON: the header must follow the body.
		if (r.Method == http.MethodPost) != (r.Header.Get("Content-Type") == "application/json") {
			t.Errorf("%s %s: Content-Type %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"bad\nkey"}`))
			return
		}
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"v":1}`))
		case "/app":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errorMessage":"invalid"}`))
		case "/moved":
			w.WriteHeader(http.StatusNotModified)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<html>oops</html>`))
		}
	}))
	defer srv.Close()
	auth := func(r *http.Request) { r.Header.Set("X-Key", "k") }
	call := func(method, path string, auth func(*http.Request), body any) (int, error) {
		var out struct{ V int }
		err := DoJSON(context.Background(), srv.Client(), method, srv.URL+path, auth, body, &out)
		return out.V, err
	}
	if v, err := call(http.MethodGet, "/ok", auth, nil); err != nil || v != 1 {
		t.Fatalf("GET /ok = %d, %v", v, err)
	}
	if v, err := call(http.MethodPost, "/ok", auth, map[string]any{"a": 1}); err != nil || v != 1 {
		t.Fatalf("POST /ok = %d, %v", v, err)
	}
	for _, tc := range []struct {
		path string
		auth func(*http.Request)
		code int
		text string
	}{
		{"/ok", nil, 401, "HTTP 401: bad key"},
		{"/app", auth, 400, "HTTP 400: invalid"},
		{"/moved", auth, 304, "HTTP 304"},
		{"/html", auth, 500, "HTTP 500"},
	} {
		_, err := call(http.MethodGet, tc.path, tc.auth, nil)
		wrapped := fmt.Errorf("poll: %w", err)
		if HTTPStatus(wrapped) != tc.code || err == nil || err.Error() != tc.text {
			t.Errorf("%s: err = %v, status %d", tc.path, err, HTTPStatus(wrapped))
		}
	}
	if got := cleanMessage("bad\u2028key\u202e\u200bend"); got != "bad keyend" {
		t.Errorf("cleanMessage = %q", got)
	}
	if long := cleanMessage(strings.Repeat("x", 400)); len([]rune(long)) != 301 {
		t.Errorf("cleanMessage kept %d runes", len([]rune(long)))
	}
	if HTTPStatus(errors.New("dial")) != 0 {
		t.Error("HTTPStatus of a non-HTTP error is not 0")
	}
}

func TestSleepCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("Sleep = %v after %v", err, time.Since(start))
	}
}

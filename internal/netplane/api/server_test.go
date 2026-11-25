// SPDX-License-Identifier: AGPL-3.0-or-later
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/events"
	"github.com/andaru-project/andaru/internal/netplane/model"
	"github.com/andaru-project/andaru/internal/netplane/poller"
	"github.com/andaru-project/andaru/internal/netplane/store"
)

type apiMockDriver struct{}

func (apiMockDriver) Kind() string                             { return "mock" }
func (apiMockDriver) ValidateConfig(driver.TargetConfig) error { return nil }
func (apiMockDriver) Poll(_ context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	now := time.Now().UTC()
	return &driver.Snapshot{
		TargetID:    cfg.ID,
		CollectedAt: now,
		Devices: []model.Device{{
			ID: "mock:02:51:0a:00:00:01", Driver: "mock", TargetID: cfg.ID,
			Name: "LG510S", Role: model.RoleSwitch, PrimaryMAC: "02:51:0a:00:00:01",
			Online: true, LastSeen: now, Capabilities: []model.Capability{model.CapLinks, model.CapFDB, model.CapDiagPing},
		}},
		Links: []model.Link{{
			DeviceID: "mock:02:51:0a:00:00:01", Index: 1, IfName: "lan1",
			Media: model.Media25GE, AdminUp: true, LinkUp: true, SpeedMbps: 2500,
			Counters: &model.PortCounters{RxBytes: 4000, TxBytes: 8000},
		}},
		FDB: []model.FdbEntry{{
			DeviceID: "mock:02:51:0a:00:00:01", LinkName: "lan1", MAC: "de:ad:be:ef:00:01", VLAN: 1,
		}},
		Neighbors: []model.NeighborEntry{{
			DeviceID: "mock:02:51:0a:00:00:01", Family: model.IPv4, IP: "192.0.2.55", MAC: "de:ad:be:ef:00:01", State: "reachable",
		}},
		Clients: []model.Client{{
			MAC: "de:ad:be:ef:00:01", IPs: []string{"192.0.2.55"}, Hostname: "nas",
			Online: true, ConnectionType: "wired", AttachedDeviceID: "mock:02:51:0a:00:00:01", AttachedLink: "lan1",
		}},
	}, nil
}

func (apiMockDriver) RunDiagnostic(_ context.Context, _ driver.TargetConfig, req model.DiagnosticRequest) (*model.DiagnosticResult, error) {
	switch {
	case req.Kind == model.DiagCableTest:
		return nil, fmt.Errorf("%w: mock", driver.ErrDiagnosticUnsupported)
	case req.Target == "-bad":
		return nil, fmt.Errorf("%w: mock", driver.ErrDiagnosticRequest)
	case req.Target == "busy":
		return nil, fmt.Errorf("%w: mock", driver.ErrDiagnosticBusy)
	case req.Target == "unreachable":
		return nil, errors.New("connection refused")
	case req.Target == "unsaveable":
		return &model.DiagnosticResult{Kind: req.Kind, Structured: make(chan int)}, nil // JSON encoding fails
	}
	return &model.DiagnosticResult{
		Kind:       req.Kind,
		DeviceID:   req.DeviceID,
		Target:     req.Target,
		OK:         true,
		DurationMs: 7,
		Summary:    "3/3 received",
	}, nil
}

func newTestServer(t *testing.T, token string) (*Server, *events.Bus) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := driver.NewRegistry()
	_ = reg.Register(apiMockDriver{})
	bus := events.NewBus()
	sched := poller.New(reg, st, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	target := driver.TargetConfig{
		ID:              "t1",
		Name:            "Lab Switch",
		Driver:          "mock",
		Endpoint:        "http://192.0.2.10",
		Enabled:         true,
		PollInterval:    15 * time.Second,
		ResolvedSecrets: map[string]string{"password": "never-leak-this-secret"},
	}
	if err := sched.PollOnce(ctx, target); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	webFS := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html><head><title>netplane</title></head><body>ok</body></html>")},
		"app.js":     {Data: []byte("console.log('ok');")},
	}

	srv := New(Options{
		Version:           "0.1.0-test",
		Store:             st,
		Registry:          reg,
		Poller:            sched,
		Bus:               bus,
		Targets:           []driver.TargetConfig{target},
		APIToken:          token,
		TrustIngressProxy: true,
		AllowedHosts:      []string{"example.com"}, // httptest.NewRequest's default Host
		TrustedOrigins:    []string{"https://netplane.example"},
		WebFS:             webFS,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return srv, bus
}

func TestAPIEndpointsAndIngressAndAuth(t *testing.T) {
	srv, _ := newTestServer(t, "secret-api-tok")

	// Health is open without token.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("health = %d %s", rec.Code, rec.Body.String())
	}

	// Protected route rejects missing token with 401.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/devices", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// Authorized bearer token succeeds.
	req := httptest.NewRequest("GET", "/api/v1/devices", nil)
	req.Header.Set("Authorization", "Bearer secret-api-tok")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "LG510S") {
		t.Fatalf("devices = %d %s", rec.Code, rec.Body.String())
	}

	// Untrusted peer sending forged X-Ingress-Path MUST be rejected with 401.
	req = httptest.NewRequest("GET", "/api/hassio_ingress/abc123/api/v1/devices", nil)
	req.RemoteAddr = "198.51.100.9:54321"
	req.Header.Set("X-Ingress-Path", "/api/hassio_ingress/abc123")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged X-Ingress-Path from untrusted IP should return 401, got %d", rec.Code)
	}

	// Loopback peer (e.g. a same-host reverse proxy) is not trusted by default, and a trusted
	// peer must still send a Supervisor-shaped ingress path.
	for _, tc := range []struct{ addr, path string }{
		{"127.0.0.1:40000", "/api/hassio_ingress/abc123"},
		{"[::1]:40000", "/api/hassio_ingress/abc123"},
		{"172.30.32.2:40000", "/x"},
	} {
		req = httptest.NewRequest("GET", "/api/v1/devices", nil)
		req.RemoteAddr = tc.addr
		req.Header.Set("X-Ingress-Path", tc.path)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("ingress bypass from %s with %q should return 401, got %d", tc.addr, tc.path, rec.Code)
		}
	}

	// Health hides per-target status (errors may name internal hosts) from unauthenticated callers.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/health", nil))
	if strings.Contains(rec.Body.String(), `"targets"`) {
		t.Fatalf("unauthenticated health leaked targets: %s", rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer secret-api-tok")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"targets"`) {
		t.Fatalf("authenticated health missing targets: %s", rec.Body.String())
	}

	// The trusted ingress proxy counts as authenticated for health too.
	req = httptest.NewRequest("GET", "/api/hassio_ingress/abc123/api/v1/health", nil)
	req.RemoteAddr = "172.30.32.2:40000"
	req.Header.Set("X-Ingress-Path", "/api/hassio_ingress/abc123")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"targets"`) {
		t.Fatalf("ingress health missing targets: %s", rec.Body.String())
	}

	// Escaped slashes in an interface name still route after the ingress prefix is stripped.
	req = httptest.NewRequest("GET", "/api/hassio_ingress/abc123/api/v1/devices/mock:02:51:0a:00:00:01/links/Gi1%2F0%2F1/samples", nil)
	req.RemoteAddr = "172.30.32.2:40000"
	req.Header.Set("X-Ingress-Path", "/api/hassio_ingress/abc123")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingress escaped link name = %d %s", rec.Code, rec.Body.String())
	}

	// Browser UI cookie carries the token; a wrong cookie is rejected.
	for cookie, want := range map[string]int{"secret-api-tok": http.StatusOK, "wrong": http.StatusUnauthorized} {
		req = httptest.NewRequest("GET", "/api/v1/devices", nil)
		req.AddCookie(&http.Cookie{Name: "netplane_token", Value: cookie})
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("cookie %q: want %d, got %d", cookie, want, rec.Code)
		}
	}

	// Hostile X-Ingress-Path (protocol-relative or URL) MUST be rejected and never injected into <base href>.
	for _, evil := range []string{"//evil.example", "https://evil.example", "/api/../evil"} {
		req = httptest.NewRequest("GET", "/topology", nil)
		req.RemoteAddr = "172.30.32.2:40000"
		req.Header.Set("X-Ingress-Path", evil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "evil") || !strings.Contains(rec.Body.String(), `<base href="./">`) {
			t.Fatalf("hostile X-Ingress-Path %q leaked into base href: %s", evil, rec.Body.String())
		}
	}

	// Outside Ingress, the base climbs from a nested route back to the app root; an encoded slash
	// in a device ID is part of one path segment.
	for path, want := range map[string]string{"/": "./", "/vlans/flow": "../", "/devices/a%2Fb": "../", "/devices/a%2fb|c": "../", "/devices/x/": "../../"} {
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if !strings.Contains(rec.Body.String(), `<base href="`+want+`">`) {
			t.Fatalf("GET %s: want <base href=%q>, got %s", path, want, rec.Body.String())
		}
	}

	// Trusted Home Assistant Supervisor peer (172.30.32.2) with valid X-Ingress-Path succeeds.
	req = httptest.NewRequest("GET", "/api/hassio_ingress/abc123/api/v1/devices/mock:02:51:0a:00:00:01", nil)
	req.RemoteAddr = "172.30.32.2:40000"
	req.Header.Set("X-Ingress-Path", "/api/hassio_ingress/abc123")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"if_name":"lan1"`) {
		t.Fatalf("ingress device detail = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/hassio_ingress/abc123/topology", nil)
	req.RemoteAddr = "172.30.32.2:40000"
	req.Header.Set("X-Ingress-Path", "/api/hassio_ingress/abc123")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<base href="/api/hassio_ingress/abc123/">`) {
		t.Fatalf("ingress SPA = %d %s", rec.Code, rec.Body.String())
	}

	// Targets endpoint MUST NOT leak ResolvedSecrets.
	req = httptest.NewRequest("GET", "/api/v1/targets", nil)
	req.Header.Set("Authorization", "Bearer secret-api-tok")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "never-leak-this-secret") {
		t.Fatalf("targets leaked secret or failed: %d %s", rec.Code, rec.Body.String())
	}

	// OpenAPI, state, topology, clients, link samples, manual poll.
	for _, path := range []string{
		"/api/v1/openapi.json",
		"/api/v1/state",
		"/api/v1/topology",
		"/api/v1/vlans/flow",
		"/api/v1/clients?online=true&q=nas",
		"/api/v1/devices/mock:02:51:0a:00:00:01/links/lan1/samples?since=1h",
	} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer secret-api-tok")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, w.Code, w.Body.String())
		}
	}

	// Trigger immediate poll.
	r := httptest.NewRequest("POST", "/api/v1/targets/t1/poll", nil)
	r.Header.Set("Authorization", "Bearer secret-api-tok")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST poll = %d %s", w.Code, w.Body.String())
	}
}

func TestDiagnosticsFdbLookupAndDeviceRPC(t *testing.T) {
	srv, _ := newTestServer(t, "")

	// Cross-vendor built-in fdb_lookup by IP resolves to MAC + switch port.
	body := bytes.NewBufferString(`{"kind":"fdb_lookup","target":"192.0.2.55"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/diagnostics", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("fdb_lookup = %d %s", rec.Code, rec.Body.String())
	}
	var res model.DiagnosticResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res.OK {
		t.Fatalf("expected OK fdb_lookup result, got (%+v, %v)", res, err)
	}

	// Device-side ping delegates to DiagnosticDriver.
	body = bytes.NewBufferString(`{"kind":"ping","device_id":"mock:02:51:0a:00:00:01","target":"1.1.1.1"}`)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/diagnostics", body))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "3/3 received") {
		t.Fatalf("ping diag = %d %s", rec.Code, rec.Body.String())
	}

	// History lists both diagnostic runs.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/diagnostics?limit=10", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fdb_lookup") {
		t.Fatalf("list diagnostics = %d %s", rec.Code, rec.Body.String())
	}

	// Error & edge paths: static asset, unknown API route, 404 device, 404 target, bad since, bad JSON, missing fdb_lookup.
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/app.js", "", http.StatusOK},
		{"GET", "/api/v1/nope", "", http.StatusNotFound},
		{"GET", "/api/v1/devices/missing", "", http.StatusNotFound},
		{"POST", "/api/v1/targets/missing/poll", "", http.StatusNotFound},
		{"GET", "/api/v1/devices/mock:02:51:0a:00:00:01/links/lan1/samples?since=notaduration", "", http.StatusBadRequest},
		{"POST", "/api/v1/diagnostics", `{bad json`, http.StatusBadRequest},
		{"POST", "/api/v1/diagnostics", `{"kind":"ping","target":"1.1.1.1"}`, http.StatusBadRequest},
		{"POST", "/api/v1/diagnostics", `{"kind":"ping","device_id":"missing","target":"1.1.1.1"}`, http.StatusNotFound},
		{"POST", "/api/v1/diagnostics", `{"kind":"fdb_lookup","target":"nas"}`, http.StatusOK},
		{"POST", "/api/v1/diagnostics", `{"kind":"fdb_lookup","target":"00:00:00:00:00:99"}`, http.StatusOK},
		{"POST", "/api/v1/diagnostics", `{"kind":"cable_test","device_id":"mock:02:51:0a:00:00:01","target":"1"}`, http.StatusNotImplemented},
		{"POST", "/api/v1/diagnostics", `{"kind":"mtr","device_id":"mock:02:51:0a:00:00:01","target":"1"}`, http.StatusBadRequest},
		{"POST", "/api/v1/diagnostics", `{"kind":"ping","device_id":"mock:02:51:0a:00:00:01","target":"-bad"}`, http.StatusBadRequest},
		{"POST", "/api/v1/diagnostics", `{"kind":"ping","device_id":"mock:02:51:0a:00:00:01","target":"unreachable"}`, http.StatusBadGateway},
		{"POST", "/api/v1/diagnostics", `{"kind":"ping","device_id":"mock:02:51:0a:00:00:01","target":"busy"}`, http.StatusServiceUnavailable},
		// A run whose history write fails is reported, not returned as saved.
		{"POST", "/api/v1/diagnostics", `{"kind":"ping","device_id":"mock:02:51:0a:00:00:01","target":"unsaveable"}`, http.StatusInternalServerError},
	} {
		var rBody io.Reader
		if tc.body != "" {
			rBody = strings.NewReader(tc.body)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, rBody))
		if w.Code != tc.want {
			t.Errorf("%s %s = %d, want %d (body: %s)", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestSSEEventStream(t *testing.T) {
	srv, bus := newTestServer(t, "")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	// Read initial ": connected" line.
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "connected") {
		t.Fatalf("initial SSE line = (%q, %v)", line, err)
	}

	// Publish an event and verify it streams through.
	bus.Publish(events.Event{Type: events.LinkDown, Severity: events.SeverityWarn, Summary: "lan1 down"})

	gotEvent := false
	for i := 0; i < 6; i++ {
		l, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		if strings.Contains(l, "lan1 down") {
			gotEvent = true
			break
		}
	}
	if !gotEvent {
		t.Fatal("did not receive published SSE event")
	}
}

func TestFdbLookupComparesIPsByValueAndDisabledPoll(t *testing.T) {
	srv, _ := newTestServer(t, "")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/diagnostics",
		strings.NewReader(`{"kind":"fdb_lookup","target":"::ffff:192.0.2.55"}`)))
	var res model.DiagnosticResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res.OK {
		t.Fatalf("fdb_lookup by mapped IP = %d %s", rec.Code, rec.Body.String())
	}

	off := srv.opts.Targets[0]
	off.Enabled = false
	opts := srv.opts
	opts.Targets = []driver.TargetConfig{off}
	rec = httptest.NewRecorder()
	New(opts).ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/targets/t1/poll", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("poll disabled target = %d %s", rec.Code, rec.Body.String())
	}
}

func TestTrustedIngressMappedPrefix(t *testing.T) {
	s := &Server{opts: Options{TrustIngressProxy: true}, trustedIngress: parseTrustedCIDRs([]string{"::ffff:172.30.32.0/120", "::ffff:10.0.0.9"})}
	for addr, want := range map[string]bool{
		"172.30.32.2:1234":       true,
		"[::ffff:172.30.32.7]:1": true,
		"10.0.0.9:1":             true,
		"172.30.33.2:1234":       false,
	} {
		if got := s.isTrustedIngressPeer(addr); got != want {
			t.Errorf("isTrustedIngressPeer(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestDiagnosticBodyReadTimeout(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 100 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old }) // after ts.Close below has stopped every handler
	srv, _ := newTestServer(t, "")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Complete headers and a valid JSON value, then a stalled remainder of the declared body.
	_, _ = io.WriteString(conn, "POST /api/v1/diagnostics HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"kind\":\"fdb_lookup\",\"target\":\"nas\"}")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response to a stalled body: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestBrowserGuards(t *testing.T) {
	open, _ := newTestServer(t, "")
	for host, want := range map[string]int{
		"192.0.2.10:8099": http.StatusOK, "[2001:db8::1]:8099": http.StatusOK, "netplane": http.StatusOK,
		"netplane.lan": http.StatusOK, "nas.home.arpa": http.StatusOK, "Example.COM.": http.StatusOK,
		"NAS.LAN": http.StatusOK, "[2001:db8::1]": http.StatusOK, "netplane.localhost:8099": http.StatusOK,
		"evil.example.net": http.StatusMisdirectedRequest, "": http.StatusMisdirectedRequest,
	} {
		req := httptest.NewRequest("GET", "/api/v1/devices", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		open.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("token-less Host %q: got %d, want %d", host, rec.Code, want)
		}
	}

	// With a token, public names are fine (DNS rebinding cannot carry the token).
	tok, _ := newTestServer(t, "secret-api-tok")
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.Host = "netplane.example.net"
	rec := httptest.NewRecorder()
	tok.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public Host with a token: %d", rec.Code)
	}

	// Cross-origin writes are refused, with or without a token.
	type wcase struct{ host, origin, fetchSite string }
	for c, refused := range map[wcase]bool{
		{"192.0.2.10:8099", "http://evil.example", ""}:              true,
		{"192.0.2.10:8099", "null", ""}:                             true,
		{"192.0.2.10", "http://192.0.2.10:8080", ""}:                true, // another port
		{"192.0.2.10:8099", "http://192.0.2.10:8099", "cross-site"}: true,
		{"192.0.2.10:8099", "http://192.0.2.10:8099", ""}:           false,
		{"192.0.2.10:8099", "", ""}:                                 false, // non-browser client
		{"127.0.0.1:8099", "http://evil.example", "same-origin"}:    false,
		{"127.0.0.1:8099", "https://netplane.example", "same-site"}: false, // trusted origin
		{"127.0.0.1:8099", "https://netplane.example", ""}:          false, // trusted, older browser
		{"127.0.0.1:8099", "https://other.example", ""}:             true,
		{"192.0.2.10:80", "http://192.0.2.10", ""}:                  true, // exact Host compare without Sec-Fetch-Site
		{"127.0.0.1:8099", "https://example.com", ""}:               true, // allowed_hosts is not an origin list
	} {
		for _, srv := range []http.Handler{open, tok} {
			req := httptest.NewRequest("POST", "/api/v1/diagnostics", strings.NewReader(`{"kind":"fdb_lookup","target":"192.0.2.55"}`))
			req.Host = c.host
			req.Header.Set("Authorization", "Bearer secret-api-tok")
			if c.origin != "" {
				req.Header.Set("Origin", c.origin)
			}
			if c.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", c.fetchSite)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if got := rec.Code == http.StatusForbidden; got != refused {
				t.Errorf("POST %+v: got %d", c, rec.Code)
			}
		}
	}

	// The ingress exemption needs the trusted peer and a Supervisor ingress path.
	for path, want := range map[string]int{"/api/hassio_ingress/abc": http.StatusOK, "": http.StatusMisdirectedRequest} {
		req := httptest.NewRequest("GET", "/api/v1/devices", nil)
		req.Host = "evil.example.net"
		req.RemoteAddr = "172.30.32.2:40000"
		if path != "" {
			req.Header.Set("X-Ingress-Path", path)
		}
		rec := httptest.NewRecorder()
		open.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("trusted peer with ingress path %q: got %d, want %d", path, rec.Code, want)
		}
	}
}

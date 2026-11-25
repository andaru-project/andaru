// SPDX-License-Identifier: AGPL-3.0-or-later
package andaru

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

func TestAndaruRemoteDiagnostic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req model.DiagnosticRequest
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/fleet/diagnostics" || r.Header.Get("Authorization") != "Bearer tok" || json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		// The remote runs it on its own device, on the checked target.
		if req.DeviceID != "andaru:local" || req.Target != "192.0.2.1" || req.Kind != model.DiagPing || req.Params["count"] != "2" {
			t.Errorf("request %+v", req)
		}
		_, _ = w.Write([]byte(`{"id":"diag-1","kind":"ping","device_id":"andaru:local","target":"192.0.2.1","ok":true,"summary":"2/2 replies from 192.0.2.1 (0% loss)","structured":{"transmitted":2,"received":2,"loss_pct":0},"duration_ms":1004}`))
	}))
	defer srv.Close()
	cfg := driver.TargetConfig{ID: "fw2", Driver: "andaru", Endpoint: srv.URL, ResolvedSecrets: map[string]string{"api_token": "tok"}}
	cfg.Settings = map[string]any{"allow_insecure_http": true}
	res, err := New().RunDiagnostic(context.Background(), cfg, model.DiagnosticRequest{Kind: model.DiagPing, DeviceID: "andaru:fw2", Target: " 192.0.2.1", Params: map[string]string{"count": "2"}})
	if err != nil || !res.OK || res.ID != "" || res.DeviceID != "andaru:fw2" || res.DurationMs != 1004 || res.Summary != "2/2 replies from 192.0.2.1 (0% loss)" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestAndaruRemoteDiagnosticErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		is     error
		text   string
	}{
		{http.StatusBadRequest, driver.ErrDiagnosticRequest, "HTTP 400"},
		{http.StatusNotImplemented, driver.ErrDiagnosticUnsupported, "predates"},
		{http.StatusForbidden, nil, "configuration.write"},
		{http.StatusBadGateway, nil, "HTTP 502"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":{"code":"X","message":"no"}}`))
		}))
		_, err := New().RunDiagnostic(context.Background(), driver.TargetConfig{ID: "fw2", Endpoint: srv.URL}, model.DiagnosticRequest{Kind: model.DiagTraceroute, Target: "a.example"})
		srv.Close()
		if err == nil || (tc.is != nil && !errors.Is(err, tc.is)) || !strings.Contains(err.Error(), tc.text) {
			t.Errorf("HTTP %d: %v", tc.status, err)
		}
	}
}

func TestAndaruDiagnosticChecksBeforeCalling(t *testing.T) {
	never := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("engine called") }))
	defer never.Close()
	local := &Driver{LocalDiagnostics: func(context.Context, model.DiagnosticRequest) (*model.DiagnosticResult, error) {
		t.Error("local run for a bad request")
		return nil, nil
	}}
	for _, cfg := range []driver.TargetConfig{{ID: "fw2", Endpoint: never.URL}, {ID: "local", Endpoint: LocalEndpoint}} {
		for _, tc := range []struct {
			req  model.DiagnosticRequest
			want error
		}{
			{model.DiagnosticRequest{Kind: model.DiagCableTest, Target: "eth0"}, driver.ErrDiagnosticUnsupported},
			{model.DiagnosticRequest{Kind: model.DiagFdbLookup, Target: "192.0.2.1"}, driver.ErrDiagnosticUnsupported},
			{model.DiagnosticRequest{Kind: model.DiagPing, Target: "-f"}, driver.ErrDiagnosticRequest},
			{model.DiagnosticRequest{Kind: model.DiagTraceroute, Target: "a b"}, driver.ErrDiagnosticRequest},
			{model.DiagnosticRequest{Kind: model.DiagPing, Target: "192.0.2.1", Params: map[string]string{"count": "50"}}, driver.ErrDiagnosticRequest},
		} {
			if _, err := local.RunDiagnostic(context.Background(), cfg, tc.req); !errors.Is(err, tc.want) {
				t.Errorf("%s %+v: %v, want %v", cfg.Endpoint, tc.req, err, tc.want)
			}
		}
	}
}

func TestAndaruLocalDiagnostic(t *testing.T) {
	cfg := driver.TargetConfig{ID: "local", Endpoint: LocalEndpoint}
	req := model.DiagnosticRequest{Kind: model.DiagTraceroute, DeviceID: "andaru:local", Target: "192.0.2.1"}
	if _, err := New().RunDiagnostic(context.Background(), cfg, req); !errors.Is(err, driver.ErrDiagnosticUnsupported) {
		t.Fatalf("without a local runner: %v", err)
	}
	d := &Driver{LocalDiagnostics: func(_ context.Context, got model.DiagnosticRequest) (*model.DiagnosticResult, error) {
		return &model.DiagnosticResult{Kind: got.Kind, DeviceID: got.DeviceID, OK: true}, nil
	}}
	if res, err := d.RunDiagnostic(context.Background(), cfg, req); err != nil || !res.OK || res.DeviceID != "andaru:local" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

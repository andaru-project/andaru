// SPDX-License-Identifier: AGPL-3.0-or-later
package pfsense

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

func TestPfSensePing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/diagnostics/ping" || r.Header.Get("X-API-Key") != "k" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["host"] != "192.0.2.1" || body["count"] != float64(3) {
			t.Errorf("body = %v", body)
		}
		_, _ = w.Write([]byte(`{"code":200,"status":"ok","data":{"output":"3 packets transmitted, 3 packets received, 0.0% packet loss\nround-trip min/avg/max/stddev = 0.2/0.3/0.4/0.1 ms\n","result_code":0}}`))
	}))
	defer srv.Close()
	cfg := driver.TargetConfig{ID: "pf", Endpoint: srv.URL, Settings: map[string]any{"allow_insecure_http": true}, ResolvedSecrets: map[string]string{"api_key": "k"}}
	d := New()
	res, err := d.RunDiagnostic(context.Background(), cfg, model.DiagnosticRequest{Kind: model.DiagPing, Target: "192.0.2.1"})
	if err != nil || !res.OK || res.Summary != "3/3 replies from 192.0.2.1 (0% loss), avg 0.3 ms" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := d.RunDiagnostic(context.Background(), cfg, model.DiagnosticRequest{Kind: model.DiagTraceroute, Target: "192.0.2.1"}); !errors.Is(err, driver.ErrDiagnosticUnsupported) {
		t.Errorf("traceroute err = %v", err)
	}
	// pfrest only shell-quotes the host, so "-f" would run a flood ping: the driver must refuse
	// it before calling the device.
	for _, req := range []model.DiagnosticRequest{
		{Kind: model.DiagPing, Target: "-f"},
		{Kind: model.DiagPing, Target: "192.0.2.1", Params: map[string]string{"count": "50"}},
	} {
		if _, err := d.RunDiagnostic(context.Background(), cfg, req); !errors.Is(err, driver.ErrDiagnosticRequest) {
			t.Errorf("%+v: err = %v", req, err)
		}
	}
	if calls != 1 {
		t.Errorf("device calls = %d, want 1", calls)
	}
	// A REST API older than v2.7.0 has no ping route.
	cfg.ResolvedSecrets = map[string]string{"api_key": "other"}
	if _, err := d.RunDiagnostic(context.Background(), cfg, model.DiagnosticRequest{Kind: model.DiagPing, Target: "192.0.2.1"}); err == nil || !strings.Contains(err.Error(), "HTTP 404 (needs REST API v2.7.0") {
		t.Errorf("404 err = %v", err)
	}
}

func TestPfSensePingFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		status        int
		body, wantErr string
	}{
		"rejected host":  {400, `{"code":400,"status":"bad request","message":"Field host is invalid"}`, "invalid diagnostic request: pfsense diagnostics/ping: HTTP 400: Field host is invalid"},
		"silent failure": {200, `{"code":200,"data":{"output":"","result_code":68}}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			cfg := driver.TargetConfig{ID: "pf", Endpoint: srv.URL, Settings: map[string]any{"allow_insecure_http": true}, ResolvedSecrets: map[string]string{"api_key": "k"}}
			res, err := New().RunDiagnostic(context.Background(), cfg, model.DiagnosticRequest{Kind: model.DiagPing, Target: "nas.lan"})
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %s", err, tc.wantErr)
				}
				return
			}
			if err != nil || res.OK || res.Error != "ping exited with status 68 and no output" {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
}

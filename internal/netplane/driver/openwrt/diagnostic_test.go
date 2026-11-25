// SPDX-License-Identifier: AGPL-3.0-or-later
package openwrt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

const traceOut = `traceroute to example.com (93.184.215.14), 20 hops max, 46 byte packets
 1  192.0.2.1  0.512 ms
 2  *
 3  93.184.215.14  9.871 ms`

// execServer fakes rpcd: login, file.exec answered by exec for the command and params, and
// session.destroy, counted in *destroyed.
func execServer(t *testing.T, exec func(cmd string, params []string) string) *httptest.Server {
	t.Helper()
	destroyed := 0
	t.Cleanup(func() {
		if destroyed != 1 {
			t.Errorf("session.destroy calls = %d, want 1", destroyed)
		}
	})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var sid, obj, method string
		_ = json.Unmarshal(req.Params[0], &sid)
		_ = json.Unmarshal(req.Params[1], &obj)
		_ = json.Unmarshal(req.Params[2], &method)
		switch {
		case obj == "session" && method == "login":
			_, _ = w.Write([]byte(`{"result":[0,{"ubus_rpc_session":"diag-sid"}]}`))
		case obj == "file" && method == "exec" && sid == "diag-sid":
			var p struct {
				Command string   `json:"command"`
				Params  []string `json:"params"`
			}
			_ = json.Unmarshal(req.Params[3], &p)
			_, _ = w.Write([]byte(exec(p.Command, p.Params)))
		case obj == "session" && method == "destroy" && sid == "diag-sid":
			destroyed++
			_, _ = w.Write([]byte(`{"result":[0]}`))
		default:
			t.Errorf("unexpected call %s.%s (sid %s)", obj, method, sid)
		}
	}))
}

func execReply(code int, stdout string) string {
	b, _ := json.Marshal(map[string]any{"code": code, "stdout": stdout})
	return `{"result":[0,` + string(b) + `]}`
}

func diagCfg(url string) driver.TargetConfig {
	return driver.TargetConfig{ID: "ow", Endpoint: url, Settings: map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"username": "netplane", "password": "pw"}}
}

func TestOpenWrtPingFallsBackToUsrBin(t *testing.T) {
	var tried []string
	srv := execServer(t, func(cmd string, params []string) string {
		tried = append(tried, cmd)
		if cmd == "/bin/ping" {
			return `{"result":[4]}` // UBUS_STATUS_NOT_FOUND
		}
		if !slices.Equal(params, []string{"-c", "2", "-w", "4", "192.0.2.1"}) {
			t.Errorf("params = %v", params)
		}
		return execReply(0, "2 packets transmitted, 2 packets received, 0% packet loss\nround-trip min/avg/max = 1.0/1.5/2.0 ms\n")
	})
	defer srv.Close()
	res, err := New().RunDiagnostic(context.Background(), diagCfg(srv.URL), model.DiagnosticRequest{
		Kind: model.DiagPing, DeviceID: "openwrt:ow", Target: "192.0.2.1", Params: map[string]string{"count": "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Summary != "2/2 replies from 192.0.2.1 (0% loss), avg 1.5 ms" || !slices.Equal(tried, []string{"/bin/ping", "/usr/bin/ping"}) {
		t.Fatalf("res=%+v tried=%v", res, tried)
	}
}

func TestOpenWrtPathFallbackOnACLRefusal(t *testing.T) {
	for name, tc := range map[string]struct {
		bin, usrBin string // replies for /bin/ping and /usr/bin/ping
		wantErr     string
	}{
		"only /usr/bin granted": {`{"result":[6]}`, execReply(0, "1 packets transmitted, 1 packets received, 0% packet loss"), ""},
		"neither granted":       {`{"result":[6]}`, `{"result":[6]}`, "openwrt file.exec /usr/bin/ping: ubus call file.exec returned status 6: ubus permission denied (grant the account luci-mod-network-diagnostics)"},
		"missing then refused":  {`{"result":[4]}`, `{"result":[6]}`, "openwrt file.exec /usr/bin/ping: ubus call file.exec returned status 6: ubus permission denied (grant the account luci-mod-network-diagnostics)"},
		"refused then missing":  {`{"result":[6]}`, `{"result":[4]}`, "openwrt file.exec /bin/ping: ubus call file.exec returned status 6: ubus permission denied (grant the account luci-mod-network-diagnostics)"},
		"refused then broken":   {`{"result":[6]}`, `{"result":[2]}`, "openwrt file.exec /usr/bin/ping: ubus call file.exec returned status 2"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := execServer(t, func(cmd string, _ []string) string {
				if cmd == "/bin/ping" {
					return tc.bin
				}
				return tc.usrBin
			})
			defer srv.Close()
			res, err := New().RunDiagnostic(context.Background(), diagCfg(srv.URL), model.DiagnosticRequest{Kind: model.DiagPing, Target: "192.0.2.1"})
			if tc.wantErr == "" {
				if err != nil || !res.OK {
					t.Fatalf("res=%+v err=%v", res, err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err = %v\nwant %s", err, tc.wantErr)
			}
		})
	}
}

func TestOpenWrtTraceroute(t *testing.T) {
	srv := execServer(t, func(cmd string, params []string) string {
		if cmd != "/usr/bin/traceroute" || params[len(params)-1] != "example.com" || params[0] != "-n" {
			t.Errorf("exec %s %v", cmd, params)
		}
		return execReply(0, traceOut)
	})
	defer srv.Close()
	res, err := New().RunDiagnostic(context.Background(), diagCfg(srv.URL), model.DiagnosticRequest{Kind: model.DiagTraceroute, Target: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	hops := res.Structured.(map[string]any)["hops"].([]driver.TracerouteHop)
	if !res.OK || res.Summary != "example.com (93.184.215.14) reached in 3 hops" || len(hops) != 3 || hops[1].Address != "" || *hops[2].RTTMs != 9.871 {
		t.Fatalf("res=%+v hops=%+v", res, hops)
	}
}

func TestTracerouteResultNotReachedOrFailed(t *testing.T) {
	req := model.DiagnosticRequest{Kind: model.DiagTraceroute}
	partial := strings.Join(strings.Split(traceOut, "\n")[:3], "\n")
	if res := driver.TracerouteResult(req, "example.com", 0, partial); res.OK || res.Summary != "example.com (93.184.215.14) not reached within 2 hops" {
		t.Errorf("partial: %+v", res)
	}
	if res := driver.TracerouteResult(req, "nope.lan", 1, "traceroute: bad address 'nope.lan'"); res.OK || res.Error != "traceroute: bad address 'nope.lan'" {
		t.Errorf("failed: %+v", res)
	}
	// A run that exits non-zero after its header fails even when hops were printed.
	broken := partial + "\ntraceroute: sendto: Network unreachable"
	if res := driver.TracerouteResult(req, "example.com", 1, broken); res.OK || res.Structured != nil || res.Error != "traceroute: sendto: Network unreachable" {
		t.Errorf("non-zero exit: %+v", res)
	}
}

func TestOpenWrtDiagnosticErrors(t *testing.T) {
	// Requests refused before any call to the device.
	never := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("device called") }))
	defer never.Close()
	d, cfg := New(), diagCfg(never.URL)
	for _, tc := range []struct {
		req  model.DiagnosticRequest
		want error
	}{
		{model.DiagnosticRequest{Kind: model.DiagCableTest, Target: "1/2"}, driver.ErrDiagnosticUnsupported},
		{model.DiagnosticRequest{Kind: model.DiagPing, Target: "-f"}, driver.ErrDiagnosticRequest},
		{model.DiagnosticRequest{Kind: model.DiagPing, Target: "192.0.2.1", Params: map[string]string{"count": "50"}}, driver.ErrDiagnosticRequest},
	} {
		if _, err := d.RunDiagnostic(context.Background(), cfg, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%+v: err = %v, want %v", tc.req, err, tc.want)
		}
	}
	// uhttpd refusing file.exec outright stops at the first path.
	calls := 0
	srv := execServer(t, func(string, []string) string {
		calls++
		return `{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Access denied"}}`
	})
	defer srv.Close()
	_, err := d.RunDiagnostic(context.Background(), diagCfg(srv.URL), model.DiagnosticRequest{Kind: model.DiagPing, Target: "192.0.2.1"})
	if !errors.Is(err, errAccessDenied) || calls != 1 || !strings.Contains(err.Error(), "luci-mod-network-diagnostics") {
		t.Errorf("err = %v after %d calls", err, calls)
	}
}

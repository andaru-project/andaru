// SPDX-License-Identifier: AGPL-3.0-or-later
package opnsense

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// pingServer simulates a ping job that sends one probe per search_jobs read.
type pingServer struct {
	mu       sync.Mutex
	calls    []string
	setBody  map[string]any
	sent     int
	received func(sent int) int
	setReply string
	running  bool
	removeOK bool
	exits    bool // the ping exits at once, as when its name does not resolve
}

func (p *pingServer) handler(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/diagnostics/ping/")
	p.calls = append(p.calls, path)
	switch {
	case path == "set":
		_ = json.NewDecoder(r.Body).Decode(&p.setBody)
		_, _ = w.Write([]byte(p.setReply))
	case path == "start/job1":
		p.running = true
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	case path == "search_jobs":
		if p.running && !p.exits {
			p.sent++
		}
		rows := []map[string]any{{"id": "other", "status": "running", "send": 99}}
		status := "running"
		if p.exits {
			status = "stopped"
		}
		row := map[string]any{"id": "job1", "status": status, "send": p.sent, "received": p.received(p.sent), "loss": "0.00 %", "last_error": ""}
		if p.received(p.sent) > 0 {
			row["min"], row["avg"], row["max"] = 1.0, 2.5, 4.0
		} else {
			row["loss"], row["last_error"] = "100.00 %", "ping: sendto: No route to host"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": append(rows, row)})
	case path == "remove/job1" && p.removeOK:
		_, _ = w.Write([]byte(`{"status":"ok","stopped_processes":0}`))
	case path == "remove/job1":
		_, _ = w.Write([]byte(`{"status":"failed"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func runPing(t *testing.T, p *pingServer, host, count string) (*model.DiagnosticResult, error) {
	t.Helper()
	old := pingPoll
	pingPoll = time.Millisecond
	t.Cleanup(func() { pingPoll = old })
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	t.Cleanup(srv.Close)
	req := model.DiagnosticRequest{Kind: model.DiagPing, DeviceID: "opnsense:opn", Target: host, Params: map[string]string{}}
	if count != "" {
		req.Params["count"] = count
	}
	return New().RunDiagnostic(context.Background(), target(srv.URL), req)
}

func TestOpnsensePing(t *testing.T) {
	p := &pingServer{removeOK: true, setReply: `{"result":"ok","uuid":"job1"}`, received: func(s int) int { return s }}
	res, err := runPing(t, p, "2001:db8::1", "2")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := res.Structured.(driver.PingStats)
	if !res.OK || st.Transmitted != 2 || st.Received != 2 || st.RTTAvgMs == nil || *st.RTTAvgMs != 2.5 || !strings.HasPrefix(res.Summary, "2/2 replies from 2001:db8::1") {
		t.Fatalf("result = %+v", res)
	}
	settings := p.setBody["ping"].(map[string]any)["settings"].(map[string]any)
	if settings["hostname"] != "2001:db8::1" || settings["fam"] != "ip6" {
		t.Fatalf("job settings = %v", settings)
	}
	if got := strings.Join(p.calls, ","); got != "set,start/job1,search_jobs,search_jobs,remove/job1" {
		t.Fatalf("calls = %s", got)
	}

	// No replies: the job's own error explains it; the probe past count is not counted.
	p = &pingServer{removeOK: true, setReply: `{"result":"ok","uuid":"job1"}`, received: func(int) int { return 0 }}
	res, err = runPing(t, p, "192.0.2.99", "1")
	st, _ = res.Structured.(driver.PingStats)
	if err != nil || res.OK || res.Error != "ping: sendto: No route to host" || st.Transmitted != 1 || st.LossPct != 100 || st.RTTAvgMs != nil {
		t.Fatalf("result = %+v, err = %v", res, err)
	}

	// The last reply arrives a poll late: wait for it rather than count a loss.
	p = &pingServer{removeOK: true, setReply: `{"result":"ok","uuid":"job1"}`, received: func(s int) int { return min(s-1, 3) }}
	res, err = runPing(t, p, "192.0.2.1", "3")
	st, _ = res.Structured.(driver.PingStats)
	if err != nil || !res.OK || st.Transmitted != 3 || st.Received != 3 || st.LossPct != 0 {
		t.Fatalf("late reply: result = %+v, err = %v", res, err)
	}

	// A failed removal is reported after one retry, naming the job.
	p = &pingServer{setReply: `{"result":"ok","uuid":"job1"}`, received: func(s int) int { return s }}
	res, err = runPing(t, p, "192.0.2.1", "1")
	if err != nil || !strings.Contains(res.Error, "job1 may still be running") || strings.Count(strings.Join(p.calls, ","), "remove/job1") != 2 {
		t.Fatalf("failed removal: result = %+v, err = %v, calls = %v", res, err, p.calls)
	}

	// The ping exits before sending (unresolvable name): failed, with no statistics.
	p = &pingServer{removeOK: true, setReply: `{"result":"ok","uuid":"job1"}`, received: func(int) int { return 0 }, exits: true}
	res, err = runPing(t, p, "nowhere.invalid", "3")
	if err != nil || res.OK || res.Structured != nil || !strings.HasPrefix(res.Summary, "ping nowhere.invalid failed") {
		t.Fatalf("exited job: result = %+v, err = %v", res, err)
	}
}

func TestOpnsensePingRefusals(t *testing.T) {
	// Guards run before any call: OPNsense must never see an option-like target.
	for _, tc := range []struct{ target, count string }{{"-f", ""}, {"192.0.2.1 -c 5", ""}, {"192.0.2.1", "11"}} {
		p := &pingServer{removeOK: true, received: func(int) int { return 0 }}
		if _, err := runPing(t, p, tc.target, tc.count); !errors.Is(err, driver.ErrDiagnosticRequest) || len(p.calls) != 0 {
			t.Errorf("%q/%q: err = %v, calls = %v", tc.target, tc.count, err, p.calls)
		}
	}
	p := &pingServer{removeOK: true, setReply: `{"result":"failed","validations":{"ping.settings.hostname":"bad"}}`, received: func(int) int { return 0 }}
	if _, err := runPing(t, p, "example.com", ""); !errors.Is(err, driver.ErrDiagnosticRequest) || strings.Join(p.calls, ",") != "set" {
		t.Fatalf("validation failure: err = %v, calls = %v", err, p.calls)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":403,"message":"Forbidden"}`))
	}))
	defer srv.Close()
	_, err := New().RunDiagnostic(context.Background(), target(srv.URL), model.DiagnosticRequest{Kind: model.DiagPing, Target: "192.0.2.1"})
	if err == nil || !strings.Contains(err.Error(), "page-diagnostics-ping") {
		t.Fatalf("err = %v, want the privilege hint", err)
	}
	if _, err := New().RunDiagnostic(context.Background(), target(srv.URL), model.DiagnosticRequest{Kind: model.DiagTraceroute, Target: "192.0.2.1"}); !errors.Is(err, driver.ErrDiagnosticUnsupported) {
		t.Fatalf("traceroute: err = %v", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later
package opnsense

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// pingPoll is how often a running ping job's counters are read.
var pingPoll = time.Second

// pingJob is a search_jobs row. ping.py reports min, avg and max as numbers or null.
type pingJob struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"` // running or stopped
	Send      numStr   `json:"send"`
	Received  numStr   `json:"received"`
	Min       *float64 `json:"min"`
	Avg       *float64 `json:"avg"`
	Max       *float64 `json:"max"`
	LastError string   `json:"last_error"`
}

// RunDiagnostic runs ping through OPNsense's ping jobs: a job pings once a second until it is
// removed, so the driver reads its counters until count replies came back, or one probe past
// count went out (giving the last counted probe its second), then removes it. Creating a job saves an in-memory model, which a
// user with user-config-readonly is refused. A DNS name is pinged over IPv4.
func (d *Driver) RunDiagnostic(ctx context.Context, cfg driver.TargetConfig, req model.DiagnosticRequest) (res *model.DiagnosticResult, err error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if req.Kind != model.DiagPing {
		return nil, fmt.Errorf("%w: opnsense runs ping, not %s", driver.ErrDiagnosticUnsupported, req.Kind)
	}
	host, err := driver.DiagnosticHost(req)
	if err != nil {
		return nil, err
	}
	n, err := driver.DiagnosticCount(req)
	if err != nil {
		return nil, err
	}
	fam := "ip"
	if addr, err := netip.ParseAddr(host); err == nil && addr.Is6() {
		fam = "ip6"
	}

	a := d.newAPI(cfg, 15*time.Second)
	defer a.client.CloseIdleConnections()
	var set struct {
		Result      string            `json:"result"`
		UUID        string            `json:"uuid"`
		Validations map[string]string `json:"validations"`
	}
	body := map[string]any{"ping": map[string]any{"settings": map[string]any{"hostname": host, "fam": fam, "description": "netplane"}}}
	if err := a.do(ctx, http.MethodPost, "/api/diagnostics/ping/set", body, &set); err != nil {
		return nil, pingError("set", err)
	}
	if set.Result != "ok" || set.UUID == "" {
		return nil, fmt.Errorf("%w: opnsense refused the ping job: %v", driver.ErrDiagnosticRequest, set.Validations)
	}
	defer func() {
		// Always remove the job, even after a timeout: it would otherwise ping for a day.
		if rerr := removeJob(ctx, a, set.UUID); rerr != nil {
			if res != nil {
				res.Error = strings.TrimPrefix(res.Error+"; "+rerr.Error(), "; ")
			} else if err == nil {
				err = rerr
			}
		}
	}()
	var started struct {
		Status    string `json:"status"`
		StatusMsg string `json:"status_msg"`
	}
	if err := a.do(ctx, http.MethodPost, "/api/diagnostics/ping/start/"+set.UUID, map[string]any{}, &started); err != nil {
		return nil, pingError("start", err)
	}
	if started.Status != "ok" {
		return nil, fmt.Errorf("opnsense ping start: %s", started.StatusMsg)
	}

	// Generous: the firewall may first spend seconds resolving a name.
	deadline := time.Now().Add(time.Duration(n+10) * time.Second)
	var job pingJob
	for {
		if err := driver.Sleep(ctx, pingPoll); err != nil {
			return nil, err
		}
		var jobs struct {
			Rows []pingJob `json:"rows"`
		}
		if err := a.do(ctx, http.MethodGet, "/api/diagnostics/ping/search_jobs", nil, &jobs); err != nil {
			return nil, pingError("search_jobs", err)
		}
		i := slices.IndexFunc(jobs.Rows, func(j pingJob) bool { return j.ID == set.UUID })
		if i < 0 {
			return nil, fmt.Errorf("opnsense ping job %s disappeared", set.UUID)
		}
		job = jobs.Rows[i]
		// Done once every reply is in, or a probe past count went out (so the last counted
		// probe had its second), or the ping exited.
		sent, got := job.Send.uint(), job.Received.uint()
		if got >= uint64(n) || sent > uint64(n) || job.Status != "running" || time.Now().After(deadline) {
			break
		}
	}

	// The job's loss text differs between IPv4 ("0.00 %") and IPv6 ("100.0%"), so loss is
	// computed here.
	// The job reports only totals, so which probe a reply answered is unknown. When a probe
	// past count went out and one reply is missing, it is taken to be that newest probe's,
	// still on its way (the common case); a counted probe lost while the extra one replied
	// then reads as no loss, an error of one probe.
	sent, got := int(job.Send.uint()), int(job.Received.uint())
	if job.Status == "running" && sent > n && got < sent {
		sent--
	}
	stats := driver.PingStats{Transmitted: sent, Received: min(got, sent)}
	if sent > 0 {
		stats.LossPct = 100 * float64(sent-stats.Received) / float64(sent)
	}
	if stats.Received > 0 {
		stats.RTTMinMs, stats.RTTAvgMs, stats.RTTMaxMs = job.Min, job.Avg, job.Max
	}
	res = driver.PingStatsResult(req, host, stats, "", cmp.Or(job.LastError, "no replies"))
	if sent == 0 {
		res.Summary, res.Structured = fmt.Sprintf("ping %s failed", host), nil
	}
	return res, nil
}

// removeJob removes a ping job, retrying once, without the caller's cancellation. The job
// would otherwise keep pinging for a day, so a failure is reported, naming the job.
func removeJob(ctx context.Context, a *api, id string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var err error
	for range 2 {
		var out struct {
			Status string `json:"status"`
		}
		if err = a.do(ctx, http.MethodPost, "/api/diagnostics/ping/remove/"+id, map[string]any{}, &out); err == nil {
			if out.Status == "ok" {
				return nil
			}
			err = fmt.Errorf("status %q", out.Status)
		}
	}
	return fmt.Errorf("opnsense ping job %s may still be running (remove it under Diagnostics: Ping): %w", id, err)
}

// pingError explains a refused ping call: a 400 is a bad request, a 403 a missing privilege.
func pingError(step string, err error) error {
	switch driver.HTTPStatus(err) {
	case http.StatusBadRequest:
		return fmt.Errorf("%w: opnsense ping %s: %v", driver.ErrDiagnosticRequest, step, err)
	case http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("opnsense ping %s: %w (the API user needs the page-diagnostics-ping privilege)", step, err)
	}
	return fmt.Errorf("opnsense ping %s: %w", step, err)
}

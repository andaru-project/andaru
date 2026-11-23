// SPDX-License-Identifier: AGPL-3.0-or-later
package pfsense

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// RunDiagnostic runs ping through POST /api/v2/diagnostics/ping (REST API package v2.7.0 or
// later). The API key needs the api-v2-diagnostics-ping-post privilege. The REST API has no
// traceroute short of its command prompt endpoint, which netplane never uses.
func (d *Driver) RunDiagnostic(ctx context.Context, cfg driver.TargetConfig, req model.DiagnosticRequest) (*model.DiagnosticResult, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if req.Kind != model.DiagPing {
		return nil, fmt.Errorf("%w: pfsense runs ping, not %s", driver.ErrDiagnosticUnsupported, req.Kind)
	}
	host, err := driver.DiagnosticHost(req)
	if err != nil {
		return nil, err
	}
	n, err := driver.DiagnosticCount(req)
	if err != nil {
		return nil, err
	}
	// pfSense's ping waits up to 10 s for the last reply after one probe per second.
	client := driver.NewHTTPClient(cfg, time.Duration(n+15)*time.Second)
	defer client.CloseIdleConnections()
	var resp struct {
		Data struct {
			Output     string `json:"output"`
			ResultCode int    `json:"result_code"`
		} `json:"data"`
	}
	url := strings.TrimRight(cfg.Endpoint, "/") + "/api/v2/diagnostics/ping"
	if err := doJSON(ctx, client, http.MethodPost, url, cfg.ResolvedSecrets["api_key"], map[string]any{"host": host, "count": n}, &resp); err != nil {
		switch driver.HTTPStatus(err) {
		case http.StatusBadRequest:
			return nil, fmt.Errorf("%w: pfsense diagnostics/ping: %v", driver.ErrDiagnosticRequest, err)
		case http.StatusNotFound, http.StatusForbidden:
			return nil, fmt.Errorf("pfsense diagnostics/ping: %w (needs REST API v2.7.0 or later and the api-v2-diagnostics-ping-post privilege)", err)
		}
		return nil, fmt.Errorf("pfsense diagnostics/ping: %w", err)
	}
	res := driver.PingResult(req, host, strings.TrimSpace(resp.Data.Output))
	if !res.OK && res.Error == "" {
		res.Error = fmt.Sprintf("ping exited with status %d and no output", resp.Data.ResultCode)
	}
	return res, nil
}

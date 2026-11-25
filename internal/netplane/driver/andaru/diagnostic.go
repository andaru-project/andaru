// SPDX-License-Identifier: AGPL-3.0-or-later
package andaru

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// LocalEndpoint is the endpoint of the engine's built-in target, the box the engine runs on.
const LocalEndpoint = "local://engine"

// remoteDeviceID is the device every Andaru engine's fleet keeps for its own box.
const remoteDeviceID = "andaru:local"

// RunDiagnostic runs ping or traceroute on the Andaru box: in process for the engine's own box,
// otherwise through the remote engine's POST /api/v1/fleet/diagnostics on its own device, which
// needs a token with configuration.write.
func (d *Driver) RunDiagnostic(ctx context.Context, cfg driver.TargetConfig, req model.DiagnosticRequest) (*model.DiagnosticResult, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if req.Kind != model.DiagPing && req.Kind != model.DiagTraceroute {
		return nil, fmt.Errorf("%w: andaru runs ping and traceroute, not %s", driver.ErrDiagnosticUnsupported, req.Kind)
	}
	host, err := driver.DiagnosticHost(req)
	if err != nil {
		return nil, err
	}
	if _, err := driver.DiagnosticCount(req); err != nil {
		return nil, err
	}
	if cfg.Endpoint == LocalEndpoint {
		if d.LocalDiagnostics == nil {
			return nil, fmt.Errorf("%w: this engine runs no diagnostics on its own box", driver.ErrDiagnosticUnsupported)
		}
		return d.LocalDiagnostics(ctx, req)
	}

	client, baseURL := buildClient(cfg)
	// The remote engine bounds each run well inside this.
	client.Timeout = 90 * time.Second
	defer client.CloseIdleConnections()
	token := cfg.ResolvedSecrets["api_token"]
	remote := model.DiagnosticRequest{Kind: req.Kind, DeviceID: remoteDeviceID, Target: host, Params: req.Params}
	var res model.DiagnosticResult
	err = driver.DoJSON(ctx, client, http.MethodPost, baseURL+"/api/v1/fleet/diagnostics", func(r *http.Request) {
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	}, remote, &res)
	switch driver.HTTPStatus(err) {
	case 0:
	case http.StatusBadRequest:
		return nil, fmt.Errorf("%w: andaru fleet/diagnostics: %v", driver.ErrDiagnosticRequest, err)
	case http.StatusNotImplemented:
		return nil, fmt.Errorf("%w: andaru fleet/diagnostics: %v (the remote Andaru predates diagnostics on its own box)", driver.ErrDiagnosticUnsupported, err)
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("andaru fleet/diagnostics: %w (the token needs configuration.write)", err)
	}
	if err != nil {
		return nil, fmt.Errorf("andaru fleet/diagnostics: %w", err)
	}
	// The run is this fleet's: its own ID and device, and the remote's timing.
	res.ID, res.DeviceID = "", req.DeviceID
	return &res, nil
}

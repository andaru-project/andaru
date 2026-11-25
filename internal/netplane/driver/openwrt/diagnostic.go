// SPDX-License-Identifier: AGPL-3.0-or-later
package openwrt

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// tracerouteMaxHops bounds a traceroute: one probe per hop with a 1 s wait keeps a run under
// about 20 s, well inside uhttpd's default 60 s script timeout.
const tracerouteMaxHops = 20

// RunDiagnostic runs ping or traceroute on the device through rpcd's file.exec, which the
// account's ACL must grant for the tool (LuCI's luci-mod-network-diagnostics grant does).
// It logs in to a session of its own, so it never races a poll over the kept session, and
// destroys it after the run when the ACL grants session.destroy (LuCI's grants do not; rpcd
// then expires it after 300 s).
func (d *Driver) RunDiagnostic(ctx context.Context, cfg driver.TargetConfig, req model.DiagnosticRequest) (*model.DiagnosticResult, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if req.Kind != model.DiagPing && req.Kind != model.DiagTraceroute {
		return nil, fmt.Errorf("%w: openwrt runs ping and traceroute, not %s", driver.ErrDiagnosticUnsupported, req.Kind)
	}
	host, err := driver.DiagnosticHost(req)
	if err != nil {
		return nil, err
	}
	var paths, args []string
	switch req.Kind {
	case model.DiagPing:
		n, err := driver.DiagnosticCount(req)
		if err != nil {
			return nil, err
		}
		// -w bounds the whole run when replies never come.
		paths, args = []string{"/bin/ping", "/usr/bin/ping"}, []string{"-c", strconv.Itoa(n), "-w", strconv.Itoa(n + 2), host}
	case model.DiagTraceroute:
		paths = []string{"/usr/bin/traceroute", "/bin/traceroute"}
		args = []string{"-n", "-q", "1", "-w", "1", "-m", strconv.Itoa(tracerouteMaxHops), host}
	}

	client := driver.NewHTTPClient(cfg, 60*time.Second)
	defer client.CloseIdleConnections()
	ubusURL := ubusEndpoint(cfg)
	sid, err := ubusLogin(ctx, client, ubusURL, cfg)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Clean up even after the run timed out or the caller left, without holding the reply.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = ubusCall(dctx, client, ubusURL, sid, "session", "destroy", map[string]any{}, nil)
	}()
	var out struct {
		Code   int    `json:"code"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	// BusyBox installs the tools in /bin or /usr/bin depending on the applet and release, and
	// an ACL may grant only one of them: rpcd answers a missing file with NOT_FOUND and a path
	// the ACL refuses with PERMISSION_DENIED, so both move on to the next path.
	var path string
	for _, p := range paths {
		e := ubusCall(ctx, client, ubusURL, sid, "file", "exec", map[string]any{"command": p, "params": args}, &out)
		// A refusal outranks a later missing file: the refusal is what the user can fix.
		if !errors.Is(e, errNotFound) || !errors.Is(err, errPermissionDenied) {
			err, path = e, p
		}
		if !errors.Is(e, errNotFound) && !errors.Is(e, errPermissionDenied) {
			break
		}
	}
	if err != nil {
		hint := ""
		if errors.Is(err, errAccessDenied) || errors.Is(err, errPermissionDenied) {
			hint = " (grant the account luci-mod-network-diagnostics)"
		}
		return nil, fmt.Errorf("openwrt file.exec %s: %w%s", path, err, hint)
	}
	output := strings.TrimSpace(out.Stdout + "\n" + out.Stderr)
	if req.Kind == model.DiagPing {
		return driver.PingResult(req, host, output), nil
	}
	return driver.TracerouteResult(req, host, out.Code, output), nil
}

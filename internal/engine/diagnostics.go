// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	npdriver "github.com/andaru-project/andaru/internal/netplane/driver"
	npmodel "github.com/andaru-project/andaru/internal/netplane/model"
)

const (
	// diagnosticSlots bounds the runs at once; a run beyond them is refused, not queued.
	diagnosticSlots = 2
	// diagnosticMaxOutput bounds what a run keeps of its output; a bounded run prints far less.
	diagnosticMaxOutput = 64 << 10
	// tracerouteMaxHops with one probe of at most 1 s per hop keeps a traceroute under about 20 s.
	tracerouteMaxHops = 20
)

// errDiagnosticsBusy refuses a run while diagnosticSlots runs are going.
var errDiagnosticsBusy = errors.New("other diagnostics are running on this box; try again shortly")

// localDiagnostics runs ping and traceroute on this box for the fleet's built-in device. The
// engine has no IP sockets of its own (its unit allows AF_UNIX and AF_NETLINK only), so each run
// is a transient unit of PID 1's with a dynamic user, no capabilities, and IP sockets.
type localDiagnostics struct {
	slots chan struct{}
	// transient runs the tool through systemd-run; tests run it directly.
	transient bool
	exec      func(ctx context.Context, argv []string, out io.Writer) (exitCode int, err error)
}

func newLocalDiagnostics() *localDiagnostics {
	return &localDiagnostics{slots: make(chan struct{}, diagnosticSlots), transient: true, exec: execDiagnostic}
}

// Run runs req's ping or traceroute and parses its output.
func (d *localDiagnostics) Run(ctx context.Context, req npmodel.DiagnosticRequest) (*npmodel.DiagnosticResult, error) {
	host, err := npdriver.DiagnosticHost(req)
	if err != nil {
		return nil, err
	}
	argv, timeout, err := diagnosticCommand(req, host)
	if err != nil {
		return nil, err
	}
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	default:
		return nil, errDiagnosticsBusy
	}
	if d.transient {
		argv = append(transientDiagnosticUnit(timeout), argv...)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out cappedBuffer
	code, err := d.exec(ctx, argv, &out)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s did not finish within %s", req.Kind, timeout)
	}
	if err != nil {
		return nil, err
	}
	output := strings.TrimSpace(out.String())
	if req.Kind == npmodel.DiagPing {
		return npdriver.PingResult(req, host, output), nil
	}
	return npdriver.TracerouteResult(req, host, code, output), nil
}

// diagnosticCommand returns the fixed command line of req's tool for host, which
// npdriver.DiagnosticHost has checked, and how long the run may take.
func diagnosticCommand(req npmodel.DiagnosticRequest, host string) ([]string, time.Duration, error) {
	switch req.Kind {
	case npmodel.DiagPing:
		n, err := npdriver.DiagnosticCount(req)
		if err != nil {
			return nil, 0, err
		}
		// -w ends the run when replies never come; the rest is for name resolution.
		return []string{"/usr/bin/ping", "-n", "-c", strconv.Itoa(n), "-w", strconv.Itoa(n + 2), "--", host}, time.Duration(n+12) * time.Second, nil
	case npmodel.DiagTraceroute:
		return []string{"/usr/bin/traceroute", "-n", "-q", "1", "-w", "1", "-m", strconv.Itoa(tracerouteMaxHops), "--", host}, 40 * time.Second, nil
	}
	return nil, 0, fmt.Errorf("%w: andaru runs ping and traceroute, not %s", npdriver.ErrDiagnosticUnsupported, req.Kind)
}

// transientDiagnosticUnit is the systemd-run prefix of a run: PID 1 stops the unit at the
// timeout even when the engine's wait for it ends first.
func transientDiagnosticUnit(timeout time.Duration) []string {
	return []string{"/usr/bin/systemd-run", "--quiet", "--pipe", "--wait", "--collect", "--service-type=exec",
		"-p", "RuntimeMaxSec=" + strconv.Itoa(int(timeout.Seconds())),
		"-p", "DynamicUser=yes",
		"-p", "CapabilityBoundingSet=",
		"-p", "NoNewPrivileges=yes",
		// IP for the probes; AF_UNIX and AF_NETLINK for name resolution.
		"-p", "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK",
		"-p", "PrivateDevices=yes",
		"-p", "ProtectHome=yes",
		"-p", "ProtectKernelTunables=yes",
		"-p", "ProtectKernelModules=yes",
		"-p", "ProtectControlGroups=yes",
		"--"}
}

// execDiagnostic runs argv without a shell, its stdout and stderr both into out.
func execDiagnostic(ctx context.Context, argv []string, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// cappedBuffer keeps the first diagnosticMaxOutput bytes written to it and drops the rest.
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := diagnosticMaxOutput - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

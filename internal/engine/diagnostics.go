// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os/exec"
	"strconv"
	"strings"
	"time"

	npdriver "github.com/andaru-project/andaru/internal/netplane/driver"
	npmodel "github.com/andaru-project/andaru/internal/netplane/model"
)

const (
	// diagnosticUser runs the tools; it owns nothing and only they run as it.
	diagnosticUser = "andaru-diag"
	// diagnosticSlots bounds the runs at once; a run beyond them is refused, not queued.
	diagnosticSlots = 2
)

// errDiagnosticsBusy refuses a run while diagnosticSlots runs are going.
var errDiagnosticsBusy = fmt.Errorf("%w: other diagnostics are running on this box; try again shortly", npdriver.ErrDiagnosticBusy)

// localDiagnostics runs ping and traceroute on this box for the fleet's built-in device. The
// engine has no IP sockets of its own (its unit allows AF_UNIX and AF_NETLINK only), so each run
// is a transient unit of PID 1's, as diagnosticUser with no capabilities.
type localDiagnostics struct {
	slots chan struct{}
	// transient runs the tool through systemd-run as user, its output read back from the journal;
	// tests run it directly.
	transient bool
	user      string
	exec      func(ctx context.Context, argv []string, out io.Writer) (exitCode int, err error)
}

func newLocalDiagnostics() *localDiagnostics {
	return &localDiagnostics{slots: make(chan struct{}, diagnosticSlots), transient: true, user: diagnosticUser, exec: execDiagnostic}
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
	var unit string
	if d.transient {
		unit = "andaru-diagnostic-" + strconv.FormatUint(rand.Uint64(), 16) + ".service"
		argv = append(transientDiagnosticUnit(timeout, unit, d.user), argv...)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out cappedBuffer
	code, err := d.exec(runCtx, argv, &out)
	if unit != "" && err == nil && runCtx.Err() == nil {
		// The tool's output, which the unit logged, then what systemd-run itself said (a unit that did not start).
		said := out.String()
		out = cappedBuffer{}
		if err = d.readJournal(ctx, unit, &out); err != nil {
			return nil, err
		}
		_, _ = out.Write([]byte(said))
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if runCtx.Err() != nil {
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
		// -w ends the run when replies never come; the rest is for the unit's start and name
		// resolution through the image's one stub resolver (glibc's default is two tries of 5 s).
		return []string{"/usr/bin/ping", "-n", "-c", strconv.Itoa(n), "-w", strconv.Itoa(n + 2), "--", host}, time.Duration(n+20) * time.Second, nil
	case npmodel.DiagTraceroute:
		return []string{"/usr/bin/traceroute", "-n", "-q", "1", "-w", "1", "-m", strconv.Itoa(npdriver.TracerouteMaxHops), "--", host}, 40 * time.Second, nil
	}
	return nil, 0, fmt.Errorf("%w: andaru runs ping and traceroute, not %s", npdriver.ErrDiagnosticUnsupported, req.Kind)
}

// readJournal reads back what unit's tool printed, once the journal holds all of it. It has a
// budget of its own: a tool that used most of its time still has its output read.
func (d *localDiagnostics) readJournal(ctx context.Context, unit string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, argv := range [][]string{
		{"/usr/bin/journalctl", "--sync"},
		{"/usr/bin/journalctl", "--quiet", "--no-pager", "--output=cat", "_SYSTEMD_UNIT=" + unit, "_TRANSPORT=stdout"},
	} {
		var said cappedBuffer
		w := io.Writer(&said)
		if argv[1] != "--sync" {
			w = io.MultiWriter(out, &said)
		}
		code, err := d.exec(ctx, argv, w)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit status %d: %s", code, strings.TrimSpace(said.String()))
		}
		if err != nil {
			return fmt.Errorf("read the diagnostic's output from the journal: %w", err)
		}
	}
	return nil
}

// transientDiagnosticUnit is the systemd-run prefix of a run as unit, which logs the tool's output
// to the journal: PID 1 stops the unit at the timeout even when the engine's wait for it ends first.
//
// Fedora's SELinux policy confines ping and traceroute in their own domains (ping_t,
// traceroute_t), and refuses the transition into them under NoNewPrivileges, which DynamicUser
// implies: so a fixed user, and no NoNewPrivileges. The seccomp-based settings do not imply it
// here, PID 1 installing them while still privileged. The output goes to the journal, which those
// domains may write: --pipe fails from inside a service on systemd 262 ("Bad message"), and they
// may not write a file the engine could read.
func transientDiagnosticUnit(timeout time.Duration, unit, user string) []string {
	return []string{"/usr/bin/systemd-run", "--quiet", "--wait", "--collect", "--unit=" + unit, "--service-type=exec",
		"-p", "RuntimeMaxSec=" + strconv.Itoa(int(timeout.Seconds())),
		"-p", "User=" + user,
		"-p", "CapabilityBoundingSet=",
		"-p", "AmbientCapabilities=",
		// IP for the probes; AF_UNIX and AF_NETLINK for name resolution.
		"-p", "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK",
		"-p", "ProtectSystem=strict",
		"-p", "ProtectHome=yes",
		"-p", "PrivateTmp=yes",
		"-p", "PrivateDevices=yes",
		"-p", "ProtectKernelTunables=yes",
		"-p", "ProtectKernelModules=yes",
		"-p", "ProtectKernelLogs=yes",
		"-p", "ProtectControlGroups=yes",
		"-p", "ProtectClock=yes",
		"-p", "ProtectHostname=yes",
		"-p", "ProtectProc=invisible",
		"-p", "ProcSubset=pid",
		"-p", "RestrictNamespaces=yes",
		"-p", "RestrictRealtime=yes",
		"-p", "RestrictSUIDSGID=yes",
		"-p", "LockPersonality=yes",
		"-p", "MemoryDenyWriteExecute=yes",
		"-p", "SystemCallArchitectures=native",
		"-p", "SystemCallFilter=@system-service",
		"-p", "TasksMax=4",
		"-p", "MemoryMax=64M",
		"-p", "StandardInput=null",
		"-p", "StandardOutput=journal",
		"-p", "StandardError=journal",
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

// cappedBuffer keeps the first npdriver.DiagnosticMaxOutput bytes written to it and drops the
// rest. The buffer is a named field: an embedded one would lend it ReadFrom, which io.Copy (and
// so os/exec) would call instead of Write.
type cappedBuffer struct{ buf bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := npdriver.DiagnosticMaxOutput - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }

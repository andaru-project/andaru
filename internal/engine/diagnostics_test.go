// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	npdriver "github.com/andaru-project/andaru/internal/netplane/driver"
	npmodel "github.com/andaru-project/andaru/internal/netplane/model"
)

const iputilsPingOut = `PING 192.0.2.1 (192.0.2.1) 56(84) bytes of data.
64 bytes from 192.0.2.1: icmp_seq=1 ttl=64 time=0.412 ms
64 bytes from 192.0.2.1: icmp_seq=2 ttl=64 time=0.388 ms

--- 192.0.2.1 ping statistics ---
2 packets transmitted, 2 received, 0% packet loss, time 1001ms
rtt min/avg/max/mdev = 0.388/0.400/0.412/0.012 ms
`

const linuxTracerouteOut = `traceroute to example.com (93.184.215.14), 20 hops max, 60 byte packets
 1  192.168.1.1  0.412 ms
 2  *
 3  93.184.215.14  9.871 ms
`

func fakeDiagnostics(exec func(ctx context.Context, argv []string, out io.Writer) (int, error)) *localDiagnostics {
	d := newLocalDiagnostics()
	d.exec = exec
	return d
}

func TestDiagnosticCommand(t *testing.T) {
	argv, timeout, err := diagnosticCommand(npmodel.DiagnosticRequest{Kind: npmodel.DiagPing, Params: map[string]string{"count": "5"}}, "192.0.2.1")
	if err != nil || !slices.Equal(argv, []string{"/usr/bin/ping", "-n", "-c", "5", "-w", "7", "--", "192.0.2.1"}) || timeout != 25*time.Second {
		t.Errorf("ping: %v %v %v", argv, timeout, err)
	}
	argv, _, err = diagnosticCommand(npmodel.DiagnosticRequest{Kind: npmodel.DiagPing}, "a.example")
	if err != nil || argv[3] != "3" {
		t.Errorf("default ping count: %v %v", argv, err)
	}
	argv, timeout, err = diagnosticCommand(npmodel.DiagnosticRequest{Kind: npmodel.DiagTraceroute}, "2001:db8::1")
	if err != nil || !slices.Equal(argv, []string{"/usr/bin/traceroute", "-n", "-q", "1", "-w", "1", "-m", "20", "--", "2001:db8::1"}) || timeout != 40*time.Second {
		t.Errorf("traceroute: %v %v %v", argv, timeout, err)
	}
	if _, _, err := diagnosticCommand(npmodel.DiagnosticRequest{Kind: npmodel.DiagPing, Params: map[string]string{"count": "0"}}, "a.example"); !errors.Is(err, npdriver.ErrDiagnosticRequest) {
		t.Errorf("count 0: %v", err)
	}
	if _, _, err := diagnosticCommand(npmodel.DiagnosticRequest{Kind: npmodel.DiagCableTest}, "eth0"); !errors.Is(err, npdriver.ErrDiagnosticUnsupported) {
		t.Errorf("cable_test: %v", err)
	}
}

func TestLocalDiagnosticsRefusesBadRequests(t *testing.T) {
	d := fakeDiagnostics(func(context.Context, []string, io.Writer) (int, error) {
		t.Error("tool run for a bad request")
		return 0, nil
	})
	for _, target := range []string{"-f", "a;reboot"} {
		if _, err := d.Run(context.Background(), npmodel.DiagnosticRequest{Kind: npmodel.DiagPing, Target: target}); !errors.Is(err, npdriver.ErrDiagnosticRequest) {
			t.Errorf("target %q: %v", target, err)
		}
	}
	if _, err := d.Run(context.Background(), npmodel.DiagnosticRequest{Kind: npmodel.DiagFdbLookup, Target: "192.0.2.1"}); !errors.Is(err, npdriver.ErrDiagnosticUnsupported) {
		t.Errorf("fdb_lookup: %v", err)
	}
}

func TestLocalDiagnosticsRun(t *testing.T) {
	var got []string
	d := fakeDiagnostics(func(_ context.Context, argv []string, out io.Writer) (int, error) {
		got = argv
		if slices.Contains(argv, "/usr/bin/traceroute") {
			_, _ = io.WriteString(out, linuxTracerouteOut)
			return 0, nil
		}
		_, _ = io.WriteString(out, iputilsPingOut)
		return 0, nil
	})
	res, err := d.Run(context.Background(), npmodel.DiagnosticRequest{Kind: npmodel.DiagPing, DeviceID: fleetLocalDeviceID, Target: " 192.0.2.1 ", Params: map[string]string{"count": "2"}})
	if err != nil || !res.OK || res.Summary != "2/2 replies from 192.0.2.1 (0% loss), avg 0.4 ms" || res.DeviceID != fleetLocalDeviceID {
		t.Fatalf("ping: %+v %v", res, err)
	}
	// The tool runs in a sandboxed transient unit with no capabilities, after the unit's own options.
	want := []string{"/usr/bin/systemd-run", "--quiet", "--pipe", "--wait", "--collect", "--service-type=exec",
		"-p", "RuntimeMaxSec=22",
		"-p", "DynamicUser=yes",
		"-p", "CapabilityBoundingSet=",
		"-p", "NoNewPrivileges=yes",
		"-p", "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK",
		"-p", "PrivateDevices=yes",
		"-p", "ProtectHome=yes",
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
		"-p", "LockPersonality=yes",
		"-p", "MemoryDenyWriteExecute=yes",
		"-p", "SystemCallArchitectures=native",
		"-p", "SystemCallFilter=@system-service",
		"-p", "TasksMax=4",
		"-p", "MemoryMax=64M",
		"--", "/usr/bin/ping", "-n", "-c", "2", "-w", "4", "--", "192.0.2.1"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %q\nwant %q", got, want)
	}
	res, err = d.Run(context.Background(), npmodel.DiagnosticRequest{Kind: npmodel.DiagTraceroute, Target: "example.com"})
	if err != nil || !res.OK || res.Summary != "example.com (93.184.215.14) reached in 3 hops" {
		t.Fatalf("traceroute: %+v %v", res, err)
	}
}

func TestLocalDiagnosticsFailures(t *testing.T) {
	ping := npmodel.DiagnosticRequest{Kind: npmodel.DiagPing, Target: "192.0.2.1"}
	// A tool that fails before its summary is a failed run, with its last line as the error.
	d := fakeDiagnostics(func(_ context.Context, _ []string, out io.Writer) (int, error) {
		_, _ = io.WriteString(out, "ping: connect: Network is unreachable\n")
		return 2, nil
	})
	if res, err := d.Run(context.Background(), ping); err != nil || res.OK || res.Error != "ping: connect: Network is unreachable" {
		t.Errorf("unreachable: %+v %v", res, err)
	}
	waits := fakeDiagnostics(func(ctx context.Context, _ []string, _ io.Writer) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	// A caller that leaves gets its own error, not a timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waits.Run(ctx, ping); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "did not finish") {
		t.Errorf("caller left: %v", err)
	}
	// A run that outlives its own deadline is an error, whatever the tool printed.
	synctest.Test(t, func(t *testing.T) {
		if _, err := waits.Run(context.Background(), ping); err == nil || err.Error() != "ping did not finish within 23s" {
			t.Errorf("own deadline: %v", err)
		}
	})
}

func TestLocalDiagnosticsBusy(t *testing.T) {
	ping := npmodel.DiagnosticRequest{Kind: npmodel.DiagPing, Target: "192.0.2.1"}
	release := make(chan struct{})
	started := make(chan struct{}, diagnosticSlots)
	d := fakeDiagnostics(func(_ context.Context, _ []string, out io.Writer) (int, error) {
		started <- struct{}{}
		<-release
		_, _ = io.WriteString(out, iputilsPingOut)
		return 0, nil
	})
	done := make(chan error, diagnosticSlots+1)
	for range diagnosticSlots {
		go func() { _, err := d.Run(context.Background(), ping); done <- err }()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("run did not start")
		}
	}
	// A run beyond the slots is refused at once, not queued.
	go func() { _, err := d.Run(context.Background(), ping); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, npdriver.ErrDiagnosticBusy) {
			t.Errorf("beyond the slots: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("run beyond the slots waits for one")
	}
	close(release)
	for range diagnosticSlots {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestCappedBuffer(t *testing.T) {
	var b cappedBuffer
	for range 3 {
		if n, err := b.Write(make([]byte, npdriver.DiagnosticMaxOutput/2+1)); err != nil || n != npdriver.DiagnosticMaxOutput/2+1 {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if len(b.String()) != npdriver.DiagnosticMaxOutput {
		t.Fatalf("kept %d bytes, want %d", len(b.String()), npdriver.DiagnosticMaxOutput)
	}
}

// os/exec copies a command's output with io.Copy, which must still go through the cap.
func TestExecDiagnosticCapsOutput(t *testing.T) {
	var b cappedBuffer
	code, err := execDiagnostic(context.Background(), []string{"/usr/bin/head", "-c", "100000", "/dev/zero"}, &b)
	if err != nil || code != 0 || len(b.String()) != npdriver.DiagnosticMaxOutput {
		t.Fatalf("code %d, err %v, kept %d bytes, want %d", code, err, len(b.String()), npdriver.DiagnosticMaxOutput)
	}
}

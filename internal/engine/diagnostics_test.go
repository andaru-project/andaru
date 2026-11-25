// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
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
	if err != nil || !slices.Equal(argv, []string{"/usr/bin/ping", "-n", "-c", "5", "-w", "7", "--", "192.0.2.1"}) || timeout != 17*time.Second {
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

func TestLocalDiagnosticsRefusesBadTargets(t *testing.T) {
	d := fakeDiagnostics(func(context.Context, []string, io.Writer) (int, error) {
		t.Error("tool run for a bad request")
		return 0, nil
	})
	for _, target := range []string{"", "-f", "--help", "a b", "a;reboot", "$(id)", "fe80::1%eth0", "a\nb", strings.Repeat("a", 254)} {
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
	// The tool runs in a transient unit with no capabilities, after the unit's own options.
	unit := transientDiagnosticUnit(14 * time.Second)
	if !slices.Equal(got[:len(unit)], unit) || got[len(unit)] != "/usr/bin/ping" || !slices.Contains(unit, "RuntimeMaxSec=14") || !slices.Contains(unit, "CapabilityBoundingSet=") {
		t.Fatalf("argv = %q", got)
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
	// A run that outlives its deadline is an error, whatever the tool printed.
	d = fakeDiagnostics(func(ctx context.Context, _ []string, _ io.Writer) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := d.Run(ctx, ping); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("timeout: %v", err)
	}
	// A run beyond the slots is refused, not queued.
	release := make(chan struct{})
	started := make(chan struct{}, diagnosticSlots)
	d = fakeDiagnostics(func(_ context.Context, _ []string, out io.Writer) (int, error) {
		started <- struct{}{}
		<-release
		_, _ = io.WriteString(out, iputilsPingOut)
		return 0, nil
	})
	done := make(chan error, diagnosticSlots)
	for range diagnosticSlots {
		go func() { _, err := d.Run(context.Background(), ping); done <- err }()
		<-started
	}
	if _, err := d.Run(context.Background(), ping); !errors.Is(err, errDiagnosticsBusy) {
		t.Errorf("beyond the slots: %v", err)
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
		if n, err := b.Write(make([]byte, diagnosticMaxOutput/2+1)); err != nil || n != diagnosticMaxOutput/2+1 {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if b.Len() != diagnosticMaxOutput {
		t.Fatalf("kept %d bytes, want %d", b.Len(), diagnosticMaxOutput)
	}
}

//go:build integration

// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"context"
	"os"
	"os/user"
	"testing"

	npmodel "github.com/andaru-project/andaru/internal/netplane/model"
)

// The image's ping and traceroute print what the parsers read.
func TestLocalDiagnosticsRealTools(t *testing.T) {
	for _, tool := range []string{"/usr/bin/ping", "/usr/bin/traceroute"} {
		if _, err := os.Stat(tool); err != nil {
			if os.Getenv("ANDARU_TEST_REQUIRE_TOOLS") == "1" {
				t.Fatalf("%s is required", tool)
			}
			t.Skipf("%s not installed", tool)
		}
	}
	d := newLocalDiagnostics()
	d.transient = false
	for _, req := range []npmodel.DiagnosticRequest{
		{Kind: npmodel.DiagPing, Target: "127.0.0.1", Params: map[string]string{"count": "2"}},
		{Kind: npmodel.DiagTraceroute, Target: "127.0.0.1"},
	} {
		res, err := d.Run(context.Background(), req)
		if err != nil || !res.OK || res.Structured == nil {
			t.Errorf("%s %s: %+v %v", req.Kind, req.Target, res, err)
		}
	}
	res, err := d.Run(context.Background(), npmodel.DiagnosticRequest{Kind: npmodel.DiagTraceroute, Target: "name.invalid"})
	if err != nil || res.OK || res.Error == "" {
		t.Errorf("unresolvable name: %+v %v", res, err)
	}
}

// The transient unit runs ping and traceroute with no capabilities and a dynamic user; it needs systemd as PID 1.
func TestLocalDiagnosticsTransientUnit(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("systemd-run needs root")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("systemd is not PID 1")
	}
	if _, err := os.Stat("/usr/bin/ping"); err != nil {
		t.Skip("/usr/bin/ping not installed")
	}
	reqs := []npmodel.DiagnosticRequest{{Kind: npmodel.DiagPing, Target: "127.0.0.1", Params: map[string]string{"count": "1"}}}
	if _, err := os.Stat("/usr/bin/traceroute"); err == nil {
		reqs = append(reqs, npmodel.DiagnosticRequest{Kind: npmodel.DiagTraceroute, Target: "127.0.0.1"})
	}
	for _, req := range reqs {
		d := newLocalDiagnostics()
		// The image declares andaru-diag; a host without it runs the unit as nobody.
		if _, err := user.Lookup(diagnosticUser); err != nil {
			d.user = "nobody"
		}
		if res, err := d.Run(context.Background(), req); err != nil || !res.OK {
			t.Errorf("%s: %+v %v", req.Kind, res, err)
		}
	}
}

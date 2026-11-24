// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"errors"
	"testing"

	"github.com/andaru-project/andaru/internal/netplane/model"
)

func TestDiagnosticHost(t *testing.T) {
	for in, want := range map[string]string{
		" 192.0.2.1 ":        "192.0.2.1",
		"2001:0db8::1":       "2001:db8::1",
		"nas.lan":            "nas.lan",
		"_sip.example.com":   "_sip.example.com",
		"-c1":                "",
		"--help":             "",
		"a b":                "",
		"host;reboot":        "",
		"fe80::1%eth0":       "",
		"":                   "",
		"192.0.2.1\n-f":      "",
		"$(id)":              "",
		"example.com/../etc": "",
	} {
		got, err := DiagnosticHost(model.DiagnosticRequest{Target: in})
		if want == "" {
			if !errors.Is(err, ErrDiagnosticRequest) {
				t.Errorf("DiagnosticHost(%q) = %q, %v; want ErrDiagnosticRequest", in, got, err)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("DiagnosticHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestDiagnosticCount(t *testing.T) {
	for in, want := range map[string]int{"": 3, "1": 1, "10": 10, "0": 0, "11": 0, "x": 0, "-1": 0} {
		req := model.DiagnosticRequest{}
		if in != "" {
			req.Params = map[string]string{"count": in}
		}
		got, err := DiagnosticCount(req)
		if (want == 0) != errors.Is(err, ErrDiagnosticRequest) || got != want {
			t.Errorf("DiagnosticCount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

func TestPingResult(t *testing.T) {
	for name, tc := range map[string]struct {
		out     string
		ok      bool
		summary string
	}{
		"busybox": {"PING 192.0.2.1 (192.0.2.1): 56 data bytes\n64 bytes from 192.0.2.1: seq=0 ttl=64 time=0.512 ms\n\n--- 192.0.2.1 ping statistics ---\n3 packets transmitted, 3 packets received, 0% packet loss\nround-trip min/avg/max = 0.401/0.512/0.700 ms",
			true, "3/3 replies from 192.0.2.1 (0% loss), avg 0.5 ms"},
		"freebsd partial": {"--- 192.0.2.1 ping statistics ---\n4 packets transmitted, 3 packets received, 25.0% packet loss\nround-trip min/avg/max/stddev = 1.000/2.000/3.000/0.816 ms",
			true, "3/4 replies from 192.0.2.1 (25% loss), avg 2.0 ms"},
		"iputils": {"--- 192.0.2.1 ping statistics ---\n3 packets transmitted, 3 received, 0% packet loss, time 2003ms\nrtt min/avg/max/mdev = 0.1/0.2/0.3/0.1 ms",
			true, "3/3 replies from 192.0.2.1 (0% loss), avg 0.2 ms"},
		"no replies": {"--- 192.0.2.1 ping statistics ---\n3 packets transmitted, 0 packets received, 100% packet loss",
			false, "0/3 replies from 192.0.2.1 (100% loss)"},
		"bad address": {"ping: bad address 'nope.lan'\n", false, "ping 192.0.2.1 failed"},
	} {
		res := PingResult(model.DiagnosticRequest{Kind: model.DiagPing, DeviceID: "d1"}, "192.0.2.1", tc.out)
		if res.OK != tc.ok || res.Summary != tc.summary || res.DeviceID != "d1" || res.RawOutput != tc.out {
			t.Errorf("%s: got ok=%v summary=%q", name, res.OK, res.Summary)
		}
	}
	if res := PingResult(model.DiagnosticRequest{}, "x", "ping: bad address 'x'\n"); res.Error != "ping: bad address 'x'" || res.Structured != nil {
		t.Errorf("failed run: error=%q structured=%v", res.Error, res.Structured)
	}
	for out, want := range map[string]string{
		"ping: sendto: Network unreachable\n3 packets transmitted, 0 packets received, 100% packet loss": "ping: sendto: Network unreachable",
		"3 packets transmitted, 0 packets received, 100% packet loss":                                    "no replies",
		"3 packets transmitted, 1 packets received, 66% packet loss":                                     "",
	} {
		if res := PingResult(model.DiagnosticRequest{}, "x", out); res.Error != want {
			t.Errorf("error for %q = %q, want %q", out, res.Error, want)
		}
	}
}

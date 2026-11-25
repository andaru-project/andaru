// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/netplane/model"
)

var (
	// ErrDiagnosticUnsupported means the driver cannot run this diagnostic kind (HTTP 501).
	ErrDiagnosticUnsupported = errors.New("diagnostic not supported by this driver")
	// ErrDiagnosticRequest means the request itself is invalid (HTTP 400).
	ErrDiagnosticRequest = errors.New("invalid diagnostic request")
	// ErrDiagnosticBusy means the device runs as many diagnostics as it allows; try again (HTTP 503).
	ErrDiagnosticBusy = errors.New("diagnostics busy")
)

const (
	// DiagnosticMaxOutput bounds the output a run keeps; a bounded ping or traceroute prints far less.
	DiagnosticMaxOutput = 64 << 10
	// TracerouteMaxHops bounds a traceroute: one probe of at most 1 s per hop keeps a run under about 20 s.
	TracerouteMaxHops = 20
)

// hostnameRE is a DNS name. The first character is never '-', so the device's ping or
// traceroute cannot read the target as an option.
var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,252}$`)

// DiagnosticHost returns the request's target as a ping or traceroute host: an IP address
// or a DNS name, nothing a device tool could read as an option or a second argument.
func DiagnosticHost(req model.DiagnosticRequest) (string, error) {
	host := strings.TrimSpace(req.Target)
	if addr, err := netip.ParseAddr(host); err == nil && addr.Zone() == "" {
		return addr.String(), nil
	}
	if !hostnameRE.MatchString(host) {
		return "", fmt.Errorf("%w: target %q is not an IP address or hostname", ErrDiagnosticRequest, host)
	}
	return host, nil
}

// DiagnosticCount returns params["count"], the number of probes: 1 to 10, default 3.
func DiagnosticCount(req model.DiagnosticRequest) (int, error) {
	s, ok := req.Params["count"]
	if !ok {
		return 3, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 10 {
		return 0, fmt.Errorf("%w: count must be 1 to 10, got %q", ErrDiagnosticRequest, s)
	}
	return n, nil
}

// PingStats is the structured result of a ping diagnostic.
type PingStats struct {
	Transmitted int      `json:"transmitted"`
	Received    int      `json:"received"`
	LossPct     float64  `json:"loss_pct"`
	RTTMinMs    *float64 `json:"rtt_min_ms,omitempty"`
	RTTAvgMs    *float64 `json:"rtt_avg_ms,omitempty"`
	RTTMaxMs    *float64 `json:"rtt_max_ms,omitempty"`
}

var (
	// BusyBox and FreeBSD: "3 packets transmitted, 3 packets received"; iputils: "3 received".
	pingCountsRE = regexp.MustCompile(`(\d+) packets transmitted, (\d+) (?:packets )?received`)
	// "round-trip min/avg/max[/stddev] = 0.1/0.2/0.3[/0.1] ms" or iputils "rtt min/avg/max/mdev = ...".
	pingRTTRE = regexp.MustCompile(`min/avg/max\S* = ([\d.]+)/([\d.]+)/([\d.]+)`)
)

// parsePing reads the summary of BusyBox, iputils or FreeBSD ping output. ok is false when
// the output has no summary line (the tool failed before sending).
func parsePing(out string) (stats PingStats, ok bool) {
	m := pingCountsRE.FindStringSubmatch(out)
	if m == nil {
		return PingStats{}, false
	}
	stats.Transmitted, _ = strconv.Atoi(m[1])
	stats.Received, _ = strconv.Atoi(m[2])
	if stats.Transmitted > 0 {
		stats.LossPct = 100 * float64(stats.Transmitted-stats.Received) / float64(stats.Transmitted)
	}
	if r := pingRTTRE.FindStringSubmatch(out); r != nil {
		f := func(s string) *float64 { v, _ := strconv.ParseFloat(s, 64); return &v }
		stats.RTTMinMs, stats.RTTAvgMs, stats.RTTMaxMs = f(r[1]), f(r[2]), f(r[3])
	}
	return stats, true
}

// PingResult builds the result of a ping run from the tool's output.
func PingResult(req model.DiagnosticRequest, host, output string) *model.DiagnosticResult {
	stats, ok := parsePing(output)
	if !ok {
		res := &model.DiagnosticResult{Kind: req.Kind, DeviceID: req.DeviceID, Target: host, RawOutput: output}
		res.Summary = fmt.Sprintf("ping %s failed", host)
		res.Error = LastLine(output)
		return res
	}
	return PingStatsResult(req, host, stats, output, toolError(output, "no replies"))
}

// PingStatsResult builds the result of a ping run from its statistics; noReplies is the
// error reported when no reply came back.
func PingStatsResult(req model.DiagnosticRequest, host string, stats PingStats, output, noReplies string) *model.DiagnosticResult {
	res := &model.DiagnosticResult{Kind: req.Kind, DeviceID: req.DeviceID, Target: host, RawOutput: output, Structured: stats}
	res.OK = stats.Received > 0
	if !res.OK {
		res.Error = noReplies
	}
	res.Summary = fmt.Sprintf("%d/%d replies from %s (%.0f%% loss)", stats.Received, stats.Transmitted, host, stats.LossPct)
	if stats.RTTAvgMs != nil {
		res.Summary += fmt.Sprintf(", avg %.1f ms", *stats.RTTAvgMs)
	}
	return res
}

// toolError returns the last line ping printed about itself ("ping: sendto: Network
// unreachable"), or fallback when it printed none.
func toolError(output, fallback string) string {
	msg := fallback
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "ping: ") {
			msg = strings.TrimSpace(line)
		}
	}
	return msg
}

// LastLine returns the last non-empty line of a tool's output, usually its error message.
func LastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// TracerouteHop is one line of traceroute output; Address is empty when the probe timed out.
type TracerouteHop struct {
	TTL     int      `json:"ttl"`
	Address string   `json:"address,omitempty"`
	RTTMs   *float64 `json:"rtt_ms,omitempty"`
}

var (
	traceDestRE = regexp.MustCompile(`^traceroute6? to \S+ \(([^)]+)\)`)
	traceHopRE  = regexp.MustCompile(`^\s*(\d+)\s+(\S+)(?:\s+([\d.]+) ms)?`)
)

// TracerouteResult parses traceroute -n -q 1 output, BusyBox's or Linux traceroute 2.x's. The
// run succeeds when the last hop is the destination address.
func TracerouteResult(req model.DiagnosticRequest, host string, code int, output string) *model.DiagnosticResult {
	res := &model.DiagnosticResult{Kind: req.Kind, DeviceID: req.DeviceID, Target: host, RawOutput: output}
	dest := ""
	hops := []TracerouteHop{}
	for line := range strings.SplitSeq(output, "\n") {
		if m := traceDestRE.FindStringSubmatch(line); m != nil {
			dest = m[1]
			continue
		}
		m := traceHopRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		hop := TracerouteHop{}
		hop.TTL, _ = strconv.Atoi(m[1])
		if m[2] != "*" {
			hop.Address = m[2]
		}
		if m[3] != "" {
			v, _ := strconv.ParseFloat(m[3], 64)
			hop.RTTMs = &v
		}
		hops = append(hops, hop)
	}
	if dest == "" || code != 0 {
		res.Summary = fmt.Sprintf("traceroute %s failed", host)
		res.Error = LastLine(output)
		return res
	}
	res.Structured = map[string]any{"destination": dest, "hops": hops}
	res.OK = len(hops) > 0 && hops[len(hops)-1].Address == dest
	if res.OK {
		res.Summary = fmt.Sprintf("%s (%s) reached in %d hops", host, dest, len(hops))
	} else {
		res.Summary = fmt.Sprintf("%s (%s) not reached within %d hops", host, dest, len(hops))
	}
	return res
}

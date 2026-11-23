// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import "time"

// DiagnosticKind identifies an on-demand diagnostic operation.
type DiagnosticKind string

const (
	DiagPing       DiagnosticKind = "ping"
	DiagTraceroute DiagnosticKind = "traceroute"
	DiagCableTest  DiagnosticKind = "cable_test"
	DiagFdbLookup  DiagnosticKind = "fdb_lookup"
)

// Valid reports whether k is one of the kinds above.
func (k DiagnosticKind) Valid() bool {
	switch k {
	case DiagPing, DiagTraceroute, DiagCableTest, DiagFdbLookup:
		return true
	}
	return false
}

// DiagnosticRequest describes a single diagnostic action to execute on a device.
type DiagnosticRequest struct {
	Kind     DiagnosticKind    `json:"kind"`
	DeviceID string            `json:"device_id"`
	Target   string            `json:"target"`
	Params   map[string]string `json:"params,omitempty"`
}

// DiagnosticResult captures the structured and raw output of a diagnostic run.
type DiagnosticResult struct {
	ID         string         `json:"id"`
	Kind       DiagnosticKind `json:"kind"`
	DeviceID   string         `json:"device_id"`
	Target     string         `json:"target"`
	OK         bool           `json:"ok"`
	StartedAt  time.Time      `json:"started_at"`
	DurationMs int64          `json:"duration_ms"`
	Summary    string         `json:"summary"`
	Structured any            `json:"structured,omitempty"`
	RawOutput  string         `json:"raw_output,omitempty"`
	Error      string         `json:"error,omitempty"`
}

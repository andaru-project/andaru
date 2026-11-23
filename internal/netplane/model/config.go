// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import "time"

// StatePlane distinguishes observed hardware facts, currently active config, and saved desired config.
type StatePlane string

const (
	PlaneObserved StatePlane = "observed"
	PlaneLive     StatePlane = "live"
	PlaneSaved    StatePlane = "saved"
)

// ChangeRequest is the Phase 2 contract for version-checked desired configuration edits.
type ChangeRequest struct {
	DeviceID        string         `json:"device_id"`
	Area            string         `json:"area"` // e.g., "link", "vlan", "wireless", "network"
	ExpectedVersion string         `json:"expected_version,omitempty"`
	RollbackSec     int            `json:"rollback_sec,omitempty"`
	Patch           map[string]any `json:"patch"`
}

// ChangeDiffEntry describes one old -> new setting transition in a preview.
type ChangeDiffEntry struct {
	Path     string `json:"path"`
	OldValue any    `json:"old_value,omitempty"`
	NewValue any    `json:"new_value,omitempty"`
	Risk     string `json:"risk,omitempty"` // none, link_flap, lockout
}

// ChangePreview is an authoritative dry-run preview produced by a ConfigDriver before applying.
type ChangePreview struct {
	DeviceID        string            `json:"device_id"`
	Area            string            `json:"area"`
	SavedVersion    string            `json:"saved_version,omitempty"`
	RequiresConfirm bool              `json:"requires_confirm"`
	Diffs           []ChangeDiffEntry `json:"diffs"`
	Warnings        []string          `json:"warnings"`
}

// ApplySession tracks an in-flight apply protected by a timed rollback window.
type ApplySession struct {
	SessionID        string    `json:"session_id"`
	DeviceID         string    `json:"device_id"`
	Area             string    `json:"area"`
	State            string    `json:"state"` // pending_confirm, confirmed, rolled_back, failed
	StartedAt        time.Time `json:"started_at"`
	RollbackDeadline time.Time `json:"rollback_deadline"`
	RequiresConfirm  bool      `json:"requires_confirm"`
	AppliedVersion   string    `json:"applied_version,omitempty"`
}

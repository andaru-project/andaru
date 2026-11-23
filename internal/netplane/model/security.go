// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import "time"

// VpnTunnel captures a WireGuard, OpenVPN, IPsec, or Tailscale tunnel/peer.
type VpnTunnel struct {
	DeviceID      string     `json:"device_id"`
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"` // wireguard, openvpn, ipsec, tailscale, other
	Peer          string     `json:"peer,omitempty"`
	Endpoint      string     `json:"endpoint,omitempty"`
	Up            bool       `json:"up"`
	LastHandshake *time.Time `json:"last_handshake,omitempty"`
	RxBytes       uint64     `json:"rx_bytes"`
	TxBytes       uint64     `json:"tx_bytes"`
}

// ServiceStatus captures runtime status of a router/firewall daemon (Unbound, Kea, pfBlockerNG, etc.).
type ServiceStatus struct {
	Name    string `json:"name"`
	State   string `json:"state"` // running, stopped, degraded, unknown
	Summary string `json:"summary,omitempty"`
}

// FirewallState captures observed packet-filter state and control-plane version info.
type FirewallState struct {
	DeviceID            string          `json:"device_id"`
	Engine              string          `json:"engine"` // nftables, pf, iptables, other
	StatesUsed          *int64          `json:"states_used,omitempty"`
	StatesMax           *int64          `json:"states_max,omitempty"`
	PolicyRuleCount     *int            `json:"policy_rule_count,omitempty"`
	NatRuleCount        *int            `json:"nat_rule_count,omitempty"`
	PendingSavedChanges *bool           `json:"pending_saved_changes,omitempty"`
	SavedVersion        string          `json:"saved_version,omitempty"`
	Services            []ServiceStatus `json:"services"`
}

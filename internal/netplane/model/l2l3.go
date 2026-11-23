// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import "time"

// FdbEntry is a single L2 MAC forwarding table entry on a switch/bridge link.
type FdbEntry struct {
	DeviceID string `json:"device_id"`
	LinkName string `json:"link_name"`
	MAC      string `json:"mac"`
	VLAN     int    `json:"vlan"`
	IsLocal  bool   `json:"is_local,omitempty"`
	AgeSec   *int   `json:"age_sec,omitempty"`
}

// LldpNeighbor is an observed LLDP/CDP neighbor seen on a local link.
type LldpNeighbor struct {
	DeviceID      string `json:"device_id"`
	LocalLink     string `json:"local_link"`
	ChassisID     string `json:"chassis_id,omitempty"`
	RemotePort    string `json:"remote_port,omitempty"`
	RemoteSysName string `json:"remote_sys_name,omitempty"`
	RemoteMgmtIP  string `json:"remote_mgmt_ip,omitempty"`
}

// NeighborEntry represents a dual-stack L3 neighbor (IPv4 ARP or IPv6 NDP).
type NeighborEntry struct {
	DeviceID    string   `json:"device_id"`
	Family      IPFamily `json:"family"`
	IP          string   `json:"ip"`
	MAC         string   `json:"mac"`
	SegmentName string   `json:"segment_name,omitempty"`
	State       string   `json:"state,omitempty"` // reachable, stale, incomplete, permanent, unknown
}

// DhcpLease is a DHCPv4 or DHCPv6 lease reported by a gateway/router.
type DhcpLease struct {
	DeviceID  string     `json:"device_id"`
	Family    IPFamily   `json:"family"`
	IP        string     `json:"ip"`
	MAC       string     `json:"mac"`
	Hostname  string     `json:"hostname,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Pool      string     `json:"pool,omitempty"`
	Static    bool       `json:"static,omitempty"`
}

// GatewayStatus reports WAN/upstream gateway reachability and latency.
type GatewayStatus struct {
	DeviceID    string   `json:"device_id"`
	Name        string   `json:"name"`
	SegmentName string   `json:"segment_name,omitempty"`
	Family      IPFamily `json:"family"`
	GatewayIP   string   `json:"gateway_ip"`
	MonitorIP   string   `json:"monitor_ip,omitempty"`
	Online      bool     `json:"online"`
	RTTMs       *float64 `json:"rtt_ms,omitempty"`
	LossPct     *float64 `json:"loss_pct,omitempty"`
	IsDefault   bool     `json:"is_default,omitempty"`
}

// Route represents a static, connected, or dynamic (BGP/OSPF) routing entry.
type Route struct {
	DeviceID    string   `json:"device_id"`
	Family      IPFamily `json:"family"`
	Destination string   `json:"destination"` // CIDR
	Gateway     string   `json:"gateway,omitempty"`
	SegmentName string   `json:"segment_name,omitempty"`
	Protocol    string   `json:"protocol"` // static, connected, bgp, ospf, kernel, other
	Metric      int      `json:"metric,omitempty"`
}

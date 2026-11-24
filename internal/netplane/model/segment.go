// SPDX-License-Identifier: AGPL-3.0-or-later
package model

// IPFamily distinguishes IPv4 and IPv6 addresses.
type IPFamily string

const (
	IPv4 IPFamily = "ipv4"
	IPv6 IPFamily = "ipv6"
)

// IPAddress is one dual-stack address assigned to a Segment.
type IPAddress struct {
	Family       IPFamily `json:"family"`
	Address      string   `json:"address"`
	PrefixLength int      `json:"prefix_length"`
	Scope        string   `json:"scope,omitempty"`  // global, link, host
	Source       string   `json:"source,omitempty"` // static, dhcp, dhcpv6, slaac, pppoe, carp
}

// HAState captures CARP or VRRP high-availability state on a Segment.
type HAState struct {
	Protocol string `json:"protocol"` // carp, vrrp
	VHID     int    `json:"vhid"`
	State    string `json:"state"` // master, backup, init
	VIP      string `json:"vip"`
}

// Segment represents a VLAN or logical L3 interface (Andaru segment, pfSense/OPNsense
// interface, OpenWrt network interface, or UniFi network).
type Segment struct {
	DeviceID     string      `json:"device_id"`
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Kind         string      `json:"kind"` // ethernet, vlan, bridge, lag, pppoe, wireguard, loopback, other
	ParentLink   string      `json:"parent_link,omitempty"`
	VLANID       *int        `json:"vlan_id,omitempty"`
	ZoneID       string      `json:"zone_id,omitempty"`
	MTU          *int        `json:"mtu,omitempty"`
	AdminUp      bool        `json:"admin_up"`
	OperState    OperState   `json:"oper_state"`
	Addresses    []IPAddress `json:"addresses"`
	HAState      *HAState    `json:"ha_state,omitempty"`
	IsManagement bool        `json:"is_management,omitempty"`
}

// Zone represents a firewall/routing trust domain grouping one or more Segments.
type Zone struct {
	DeviceID   string   `json:"device_id"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SegmentIDs []string `json:"segment_ids"`
}

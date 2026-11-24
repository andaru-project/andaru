// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import "time"

// Ssid represents a configured/active wireless SSID on a radio.
type Ssid struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	VLANID      *int   `json:"vlan_id,omitempty"`
	Security    string `json:"security,omitempty"`
	ClientCount int    `json:"client_count"`
}

// WirelessRadio represents a physical Wi-Fi radio (2.4GHz, 5GHz, 6GHz) on an AP.
type WirelessRadio struct {
	DeviceID       string   `json:"device_id"`
	Name           string   `json:"name"`
	Band           string   `json:"band"` // 2.4GHz, 5GHz, 6GHz
	Channel        int      `json:"channel"`
	WidthMHz       int      `json:"width_mhz"`
	TxPowerDbm     int      `json:"tx_power_dbm"`
	NoiseFloorDbm  *int     `json:"noise_floor_dbm,omitempty"`
	UtilizationPct *float64 `json:"utilization_pct,omitempty"`
	SSIDs          []Ssid   `json:"ssids"`
}

// WirelessUplink captures a wireless mesh uplink between two APs.
type WirelessUplink struct {
	DeviceID     string `json:"device_id"`
	PeerDeviceID string `json:"peer_device_id,omitempty"`
	PeerMAC      string `json:"peer_mac"`
	Band         string `json:"band,omitempty"`
	SignalDbm    int    `json:"signal_dbm"`
	TxRateMbps   int    `json:"tx_rate_mbps,omitempty"`
	RxRateMbps   int    `json:"rx_rate_mbps,omitempty"`
}

// Client represents an observed wired or wireless endpoint on the network.
type Client struct {
	MAC              string    `json:"mac"`
	IPs              []string  `json:"ips"`
	Hostname         string    `json:"hostname,omitempty"`
	VendorOUI        string    `json:"vendor_oui,omitempty"`
	Online           bool      `json:"online"`
	ConnectionType   string    `json:"connection_type"` // wired, wireless
	AttachedDeviceID string    `json:"attached_device_id,omitempty"`
	AttachedLink     string    `json:"attached_link,omitempty"`
	SSID             string    `json:"ssid,omitempty"`
	VLAN             *int      `json:"vlan,omitempty"`
	SignalDbm        *int      `json:"signal_dbm,omitempty"`
	RxRateMbps       *int      `json:"rx_rate_mbps,omitempty"`
	TxRateMbps       *int      `json:"tx_rate_mbps,omitempty"`
	LastSeen         time.Time `json:"last_seen"`
}

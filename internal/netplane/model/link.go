// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import "slices"

// MediaType classifies physical link media.
type MediaType string

const (
	MediaFE     MediaType = "FE"
	MediaGE     MediaType = "GE"
	Media25GE   MediaType = "2.5GE" // 2.5 Gb/s; Media25G is 25 Gb/s
	Media5GE    MediaType = "5GE"
	Media10GE   MediaType = "10GE"
	Media25G    MediaType = "25GE"
	Media40G    MediaType = "40GE"
	Media100G   MediaType = "100GE"
	MediaSFP    MediaType = "SFP"
	MediaSFPPl  MediaType = "SFP+"
	MediaSFP28  MediaType = "SFP28"
	MediaQSFPPl MediaType = "QSFP+"
	MediaQSFP28 MediaType = "QSFP28"
	MediaOther  MediaType = "other"
)

// MediaForSpeed classifies a port from its link speed. Above 10 Gb/s every port is a cage
// (25GBASE-T and 40GBASE-T hardware is all but absent): SFP28 from 25 Gb/s, QSFP+ from 40 Gb/s and
// QSFP28 from 50 Gb/s, and a pluggable port below 25 Gb/s is SFP+. There a port not known to be
// pluggable at a non-standard speed is the sum of an aggregate (a 3x10G bond reports 30000), so
// other; drivers keep aggregates they recognise out of this function. Up to 10 Gb/s a pluggable cage is SFP+
// above 1 Gb/s (2.5G and 5G modules sit in SFP+ cages) and SFP otherwise, and any other port is
// the Ethernet class of its speed (FE for 10/100, as UniFi reports it), or other with no speed.
// shortcut: 200G and 400G cages (QSFP56, QSFP-DD) and single-lane 50G (SFP56) read as QSFP28, and
// a breakout lane reads as its own speed's cage; add types or lane hints when a driver sees one.
func MediaForSpeed(speedMbps int, pluggable bool) MediaType {
	if speedMbps > 10000 {
		if !pluggable && !slices.Contains([]int{25000, 40000, 50000, 100000, 200000, 400000}, speedMbps) {
			return MediaOther
		}
		switch {
		case speedMbps >= 50000:
			return MediaQSFP28
		case speedMbps >= 40000:
			return MediaQSFPPl
		case speedMbps >= 25000:
			return MediaSFP28
		}
		return MediaSFPPl
	}
	switch {
	case pluggable && speedMbps > 1000:
		return MediaSFPPl
	case pluggable:
		return MediaSFP
	case speedMbps >= 10000:
		return Media10GE
	case speedMbps >= 5000:
		return Media5GE
	case speedMbps >= 2500:
		return Media25GE
	case speedMbps >= 1000:
		return MediaGE
	case speedMbps > 0:
		return MediaFE
	}
	return MediaOther
}

// OperState reflects observed kernel/switch link operational state.
type OperState string

const (
	OperUp        OperState = "up"
	OperDown      OperState = "down"
	OperNoCarrier OperState = "no_carrier"
	OperDisabled  OperState = "disabled"
	OperMissing   OperState = "missing"
	OperUnknown   OperState = "unknown"
)

// OperFromAdminLink returns the operational state of a port from its admin and link state.
func OperFromAdminLink(adminUp, linkUp bool) OperState {
	switch {
	case !adminUp:
		return OperDisabled
	case linkUp:
		return OperUp
	default:
		return OperNoCarrier
	}
}

// StpState reflects Spanning Tree port state.
type StpState string

const (
	StpForwarding StpState = "forwarding"
	StpBlocking   StpState = "blocking"
	StpDiscarding StpState = "discarding"
	StpLearning   StpState = "learning"
	StpListening  StpState = "listening"
	StpDisabled   StpState = "disabled"
)

// FlowControlState captures IEEE 802.3x Pause settings.
type FlowControlState struct {
	Rx      bool  `json:"rx"`
	Tx      bool  `json:"tx"`
	Autoneg *bool `json:"autoneg,omitempty"`
}

// PortPoE captures per-port PoE capability and live delivery.
type PortPoE struct {
	Supported bool    `json:"supported"`
	Enabled   bool    `json:"enabled"`
	State     string  `json:"state,omitempty"`
	Watts     float64 `json:"watts,omitempty"`
	Class     string  `json:"class,omitempty"`
}

// SfpDom captures optical transceiver DDMI/DOM telemetry.
type SfpDom struct {
	Present      bool       `json:"present"`
	Vendor       string     `json:"vendor,omitempty"`
	PartNumber   string     `json:"part_number,omitempty"`
	Serial       string     `json:"serial,omitempty"`
	WavelengthNm *float64   `json:"wavelength_nm,omitempty"`
	TempC        *float64   `json:"temp_c,omitempty"`
	VoltageV     *float64   `json:"voltage_v,omitempty"`
	BiasMa       *float64   `json:"bias_ma,omitempty"`
	TxPowerDbm   *float64   `json:"tx_power_dbm,omitempty"`
	RxPowerDbm   *float64   `json:"rx_power_dbm,omitempty"`
	AlertState   AlertState `json:"alert_state"`
}

// PortCounters captures cumulative observed hardware/interface counters.
type PortCounters struct {
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	RxPackets uint64 `json:"rx_packets"`
	TxPackets uint64 `json:"tx_packets"`
	RxErrors  uint64 `json:"rx_errors"`
	TxErrors  uint64 `json:"tx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxDropped uint64 `json:"tx_dropped"`
	RxPause   uint64 `json:"rx_pause,omitempty"`
	TxPause   uint64 `json:"tx_pause,omitempty"`
	CrcErrors uint64 `json:"crc_errors,omitempty"`
	// Counter32 marks RxBytes/TxBytes as 32-bit counters (e.g. SNMP ifInOctets without
	// ifXTable) so bitrate deltas treat a decrease as a wrap, not a reset.
	Counter32 bool `json:"-"`
}

// PortRates captures derived positive bitrates in bits per second.
type PortRates struct {
	RxBps float64 `json:"rx_bps"`
	TxBps float64 `json:"tx_bps"`
}

// Link represents a physical L1/L2 switch or NIC port on a Device.
type Link struct {
	DeviceID    string            `json:"device_id"`
	Index       int               `json:"index"`
	IfName      string            `json:"if_name"`
	Label       string            `json:"label,omitempty"`
	MAC         string            `json:"mac,omitempty"`
	AdminUp     bool              `json:"admin_up"`
	LinkUp      bool              `json:"link_up"`
	OperState   OperState         `json:"oper_state"`
	SpeedMbps   int               `json:"speed_mbps"`
	Duplex      string            `json:"duplex,omitempty"`
	Autoneg     *bool             `json:"autoneg,omitempty"`
	MTU         *int              `json:"mtu,omitempty"`
	Media       MediaType         `json:"media"`
	IsFiber     bool              `json:"is_fiber"`
	Isolated    *bool             `json:"isolated,omitempty"`
	LagGroupID  string            `json:"lag_group_id,omitempty"`
	StpState    StpState          `json:"stp_state,omitempty"`
	StpRole     string            `json:"stp_role,omitempty"`
	EEE         *bool             `json:"eee,omitempty"`
	FlowControl *FlowControlState `json:"flow_control,omitempty"`
	PVID        *int              `json:"pvid,omitempty"`
	TaggedVLANs []int             `json:"tagged_vlans"`
	PoE         *PortPoE          `json:"poe,omitempty"`
	SFP         *SfpDom           `json:"sfp,omitempty"`
	Counters    *PortCounters     `json:"counters,omitempty"`
	Rates       *PortRates        `json:"rates,omitempty"`
}

// LagGroup represents a bonded/aggregated L2 trunk (LACP or static).
type LagGroup struct {
	DeviceID    string   `json:"device_id"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Mode        string   `json:"mode"` // "lacp" or "static"
	MemberLinks []string `json:"member_links"`
	LinkUp      bool     `json:"link_up"`
	SpeedMbps   int      `json:"speed_mbps"`
}

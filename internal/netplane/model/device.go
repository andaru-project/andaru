// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import "time"

// Capability identifies an explicit hardware or protocol feature supported by a Device.
type Capability string

const (
	// L1 / L2 Switching
	CapLinks       Capability = "cap:links"
	CapSfpDom      Capability = "cap:sfp_dom"
	CapEEE         Capability = "cap:eee"
	CapFlowControl Capability = "cap:flow_control"
	CapPoE         Capability = "cap:poe"
	CapLAG         Capability = "cap:lag"
	CapSTP         Capability = "cap:stp"
	CapVLANs       Capability = "cap:vlans"
	CapFDB         Capability = "cap:fdb"
	CapLLDP        Capability = "cap:lldp"

	// L3 / Routing / Firewall
	CapSegments  Capability = "cap:segments"
	CapZones     Capability = "cap:zones"
	CapNeighbors Capability = "cap:neighbors"
	CapDHCP      Capability = "cap:dhcp"
	CapGateway   Capability = "cap:gateway"
	CapRoutes    Capability = "cap:routes"
	CapVPN       Capability = "cap:vpn"
	CapFirewall  Capability = "cap:firewall"
	CapHA        Capability = "cap:ha"
	CapDNS       Capability = "cap:dns"

	// Wireless
	CapWireless     Capability = "cap:wireless"
	CapWirelessMesh Capability = "cap:wireless_mesh"

	// Hardware / System
	CapSensors      Capability = "cap:sensors"
	CapFirmwareInfo Capability = "cap:firmware_info"

	// Diagnostics
	CapDiagPing       Capability = "cap:diag_ping"
	CapDiagTraceroute Capability = "cap:diag_traceroute"
	CapDiagCableTest  Capability = "cap:diag_cable_test"
	CapDiagFdbLookup  Capability = "cap:diag_fdb_lookup"

	// Phase 2 Control
	CapConfigPreview       Capability = "cap:config_preview"
	CapConfigApply         Capability = "cap:config_apply"
	CapConfigRollbackTimer Capability = "cap:config_rollback_timer"
	CapConfigBackup        Capability = "cap:config_backup"
)

// DeviceRole categorizes a network device's primary function.
type DeviceRole string

const (
	RoleSwitch   DeviceRole = "switch"
	RoleRouter   DeviceRole = "router"
	RoleGateway  DeviceRole = "gateway"
	RoleFirewall DeviceRole = "firewall"
	RoleAP       DeviceRole = "ap"
	RoleCombo    DeviceRole = "combo"
	RoleUnknown  DeviceRole = "unknown"
)

// SensorKind identifies a hardware sensor measurement type.
type SensorKind string

const (
	SensorTempC    SensorKind = "temp_c"
	SensorFanRPM   SensorKind = "fan_rpm"
	SensorVoltageV SensorKind = "voltage_v"
	SensorPowerW   SensorKind = "power_w"
	SensorPSUOK    SensorKind = "psu_ok"
)

// AlertState represents health/threshold severity for sensors and transceivers.
type AlertState string

const (
	AlertOK      AlertState = "ok"
	AlertWarn    AlertState = "warn"
	AlertCrit    AlertState = "crit"
	AlertUnknown AlertState = "unknown"
)

// FirmwareInfo captures read-only firmware and hardware identification.
type FirmwareInfo struct {
	Current     string `json:"current,omitempty"`
	Available   string `json:"available,omitempty"`
	HardwareRev string `json:"hardware_rev,omitempty"`
	Serial      string `json:"serial,omitempty"`
}

// SystemStats holds observed CPU/memory/load statistics.
type SystemStats struct {
	CPUPct        float64 `json:"cpu_pct,omitempty"`
	MemUsedBytes  uint64  `json:"mem_used_bytes,omitempty"`
	MemTotalBytes uint64  `json:"mem_total_bytes,omitempty"`
	Load1         float64 `json:"load1,omitempty"`
}

// SensorReading is one chassis health measurement (temperature, fan, voltage, PSU).
type SensorReading struct {
	Name   string     `json:"name"`
	Kind   SensorKind `json:"kind"`
	Value  float64    `json:"value"`
	Unit   string     `json:"unit"`
	Status AlertState `json:"status"`
}

// PoEBudget captures chassis-wide PoE capacity and current draw.
type PoEBudget struct {
	TotalWatts float64 `json:"total_watts"`
	UsedWatts  float64 `json:"used_watts"`
}

// SecretRef references a named secret without embedding its plaintext value.
type SecretRef struct {
	Secret string `json:"secret"`
}

// SecretMetadata is the redacted read-back view of a stored secret.
type SecretMetadata struct {
	Name      string     `json:"name"`
	IsSet     bool       `json:"is_set"`
	Version   int64      `json:"version"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// Device is the canonical representation of a managed switch, router, firewall, or AP.
type Device struct {
	ID             string          `json:"id"`
	Driver         string          `json:"driver"`
	TargetID       string          `json:"target_id"`
	Name           string          `json:"name"`
	Hostname       string          `json:"hostname,omitempty"`
	Vendor         string          `json:"vendor,omitempty"`
	Model          string          `json:"model,omitempty"`
	Role           DeviceRole      `json:"role"`
	PrimaryMAC     string          `json:"primary_mac,omitempty"`
	MACs           []string        `json:"macs"`
	MgmtIP         string          `json:"mgmt_ip,omitempty"`
	Firmware       FirmwareInfo    `json:"firmware"`
	UptimeSec      uint64          `json:"uptime_sec"`
	Online         bool            `json:"online"`
	LastSeen       time.Time       `json:"last_seen"`
	Capabilities   []Capability    `json:"capabilities"`
	SystemStats    SystemStats     `json:"system_stats"`
	Sensors        []SensorReading `json:"sensors"`
	PoEBudget      *PoEBudget      `json:"poe_budget,omitempty"`
	PendingChanges *bool           `json:"pending_changes,omitempty"`
	Extensions     map[string]any  `json:"extensions,omitempty"`
}

// HasCapability returns true if the device advertises cap.
func (d Device) HasCapability(cap Capability) bool {
	for _, c := range d.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

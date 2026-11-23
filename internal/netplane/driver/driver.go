// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/model"
)

// TargetConfig describes one configured device or controller target to poll.
// ResolvedSecrets holds runtime-resolved secret values and is never serialized to JSON or logged.
type TargetConfig struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Driver          string                     `json:"driver"`
	Endpoint        string                     `json:"endpoint"`
	Enabled         bool                       `json:"enabled"`
	PollInterval    time.Duration              `json:"poll_interval"`
	Settings        map[string]any             `json:"settings,omitempty"`
	SecretRefs      map[string]model.SecretRef `json:"secret_refs,omitempty"`
	ResolvedSecrets map[string]string          `json:"-"`
}

// LogValue implements slog.LogValuer, redacting all resolved secret values.
func (c TargetConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", c.ID),
		slog.String("name", c.Name),
		slog.String("driver", c.Driver),
		slog.String("endpoint", c.Endpoint),
		slog.Bool("enabled", c.Enabled),
		slog.Duration("poll_interval", c.PollInterval),
		slog.Int("secret_count", len(c.ResolvedSecrets)),
	)
}

// Snapshot is an atomic point-in-time observed/live read returned by a Driver.
// One TargetID (for example, a UniFi controller) may yield multiple devices.
type Snapshot struct {
	TargetID    string    `json:"target_id"`
	CollectedAt time.Time `json:"collected_at"`
	Inventory

	// Unavailable lists sections the driver could not refresh this poll (a best-effort call
	// failed). The store carries them over from the previous snapshot instead of treating
	// their absence as everything having disappeared.
	Unavailable []Section `json:"unavailable,omitempty"`
	// Stale maps each carried-over section to when its data was last fresh.
	Stale map[Section]time.Time `json:"stale,omitempty"`
	// MarkedOffline is set when the store rewrote this snapshot for an unreachable target;
	// its sections are not fresh and are never carried over.
	MarkedOffline bool `json:"marked_offline,omitempty"`
	// PollInterval is the target's poll interval, set by the poller so the store can size the
	// one-time carry-over window; zero means unknown.
	PollInterval time.Duration `json:"-"`
}

// Inventory is the observed network state a snapshot carries and the store aggregates.
type Inventory struct {
	Devices         []model.Device         `json:"devices"`
	Links           []model.Link           `json:"links"`
	LagGroups       []model.LagGroup       `json:"lag_groups"`
	Segments        []model.Segment        `json:"segments"`
	Zones           []model.Zone           `json:"zones"`
	FDB             []model.FdbEntry       `json:"fdb"`
	LLDP            []model.LldpNeighbor   `json:"lldp"`
	Neighbors       []model.NeighborEntry  `json:"neighbors"`
	DHCP            []model.DhcpLease      `json:"dhcp"`
	Gateways        []model.GatewayStatus  `json:"gateways"`
	Routes          []model.Route          `json:"routes"`
	VPNs            []model.VpnTunnel      `json:"vpns"`
	Firewalls       []model.FirewallState  `json:"firewalls"`
	Radios          []model.WirelessRadio  `json:"radios"`
	WirelessUplinks []model.WirelessUplink `json:"wireless_uplinks"`
	Clients         []model.Client         `json:"clients"`
}

// Append adds every entry of o to inv.
func (inv *Inventory) Append(o Inventory) {
	inv.Devices = append(inv.Devices, o.Devices...)
	inv.Links = append(inv.Links, o.Links...)
	inv.LagGroups = append(inv.LagGroups, o.LagGroups...)
	inv.Segments = append(inv.Segments, o.Segments...)
	inv.Zones = append(inv.Zones, o.Zones...)
	inv.FDB = append(inv.FDB, o.FDB...)
	inv.LLDP = append(inv.LLDP, o.LLDP...)
	inv.Neighbors = append(inv.Neighbors, o.Neighbors...)
	inv.DHCP = append(inv.DHCP, o.DHCP...)
	inv.Gateways = append(inv.Gateways, o.Gateways...)
	inv.Routes = append(inv.Routes, o.Routes...)
	inv.VPNs = append(inv.VPNs, o.VPNs...)
	inv.Firewalls = append(inv.Firewalls, o.Firewalls...)
	inv.Radios = append(inv.Radios, o.Radios...)
	inv.WirelessUplinks = append(inv.WirelessUplinks, o.WirelessUplinks...)
	inv.Clients = append(inv.Clients, o.Clients...)
}

// FillNil replaces nil slices with empty ones, so they encode as [] rather than null.
func (inv *Inventory) FillNil() {
	inv.Devices = nonNil(inv.Devices)
	inv.Links = nonNil(inv.Links)
	inv.LagGroups = nonNil(inv.LagGroups)
	inv.Segments = nonNil(inv.Segments)
	inv.Zones = nonNil(inv.Zones)
	inv.FDB = nonNil(inv.FDB)
	inv.LLDP = nonNil(inv.LLDP)
	inv.Neighbors = nonNil(inv.Neighbors)
	inv.DHCP = nonNil(inv.DHCP)
	inv.Gateways = nonNil(inv.Gateways)
	inv.Routes = nonNil(inv.Routes)
	inv.VPNs = nonNil(inv.VPNs)
	inv.Firewalls = nonNil(inv.Firewalls)
	inv.Radios = nonNil(inv.Radios)
	inv.WirelessUplinks = nonNil(inv.WirelessUplinks)
	inv.Clients = nonNil(inv.Clients)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Section names a snapshot slice by its JSON field name.
type Section string

// Sections a driver can mark unavailable.
const (
	SectionSegments  Section = "segments"
	SectionZones     Section = "zones"
	SectionFDB       Section = "fdb"
	SectionLLDP      Section = "lldp"
	SectionNeighbors Section = "neighbors"
	SectionDHCP      Section = "dhcp"
	SectionGateways  Section = "gateways"
	SectionVPNs      Section = "vpns"
	SectionFirewalls Section = "firewalls"
	SectionRadios    Section = "radios"
	SectionClients   Section = "clients"
)

// MarkUnavailable records that sec could not be refreshed this poll.
func (s *Snapshot) MarkUnavailable(sec Section) {
	if !slices.Contains(s.Unavailable, sec) {
		s.Unavailable = append(s.Unavailable, sec)
	}
}

// CarryOver fills each unavailable section from prev. Data fresh in prev is carried once, if
// prev is no older than three poll intervals or maxAge, whichever is longer, so a single failed call never
// looks like everything leaving however slow the interval, but a snapshot left from before a
// long stop is not revived. Data prev itself carried keeps its original freshness and expires
// after maxAge, so a permanently failing call cannot freeze it.
func (s *Snapshot) CarryOver(prev *Snapshot, maxAge time.Duration) {
	if prev == nil || prev.MarkedOffline {
		return
	}
	for _, sec := range s.Unavailable {
		freshAt, carried := prev.Stale[sec]
		limit := maxAge
		if !carried {
			if slices.Contains(prev.Unavailable, sec) {
				continue // prev had nothing fresh to give either
			}
			freshAt, limit = prev.CollectedAt, max(maxAge, 3*s.PollInterval)
		}
		// A negative age means the clock stepped back: treat the data as expired.
		if age := s.CollectedAt.Sub(freshAt); age < 0 || age > limit {
			continue
		}
		switch sec {
		case SectionSegments:
			s.Segments = prev.Segments
		case SectionZones:
			s.Zones = prev.Zones
		case SectionFDB:
			s.FDB = prev.FDB
		case SectionLLDP:
			s.LLDP = prev.LLDP
		case SectionNeighbors:
			s.Neighbors = prev.Neighbors
		case SectionDHCP:
			s.DHCP = prev.DHCP
		case SectionGateways:
			s.Gateways = prev.Gateways
		case SectionVPNs:
			s.VPNs = prev.VPNs
		case SectionFirewalls:
			s.Firewalls = prev.Firewalls
		case SectionRadios:
			s.Radios = prev.Radios
		case SectionClients:
			s.Clients = prev.Clients
		default:
			continue
		}
		if s.Stale == nil {
			s.Stale = map[Section]time.Time{}
		}
		s.Stale[sec] = freshAt
	}
}

// Normalize canonicalizes MAC addresses, SFP alert defaults, and non-nil slices across the snapshot.
// A malformed MAC (e.g. a 4-byte tunnel address) never fails the poll: optional MAC fields are
// cleared, and FDB/client entries, which are keyed by MAC, are dropped.
func (s *Snapshot) Normalize() {
	th := model.DefaultSfpThresholds()
	for i := range s.Devices {
		d := &s.Devices[i]
		normMAC(&d.PrimaryMAC)
		for j := range d.MACs {
			normMAC(&d.MACs[j])
		}
		d.MACs = slices.DeleteFunc(d.MACs, func(m string) bool { return m == "" })
		d.NormalizeSlices()
	}
	for i := range s.Links {
		l := &s.Links[i]
		normMAC(&l.MAC)
		if l.SFP != nil {
			l.SFP.AlertState = model.EvaluateSfpAlert(l.SFP, th)
		}
		l.NormalizeSlices()
	}
	for i := range s.LagGroups {
		s.LagGroups[i].NormalizeSlices()
	}
	for i := range s.Segments {
		s.Segments[i].NormalizeSlices()
	}
	for i := range s.Zones {
		s.Zones[i].NormalizeSlices()
	}
	for i := range s.FDB {
		normMAC(&s.FDB[i].MAC)
	}
	s.FDB = slices.DeleteFunc(s.FDB, func(f model.FdbEntry) bool { return f.MAC == "" })
	for i := range s.LLDP {
		// Chassis IDs may legitimately be non-MAC strings; keep them as reported.
		if mac, err := model.NormalizeMAC(s.LLDP[i].ChassisID); err == nil {
			s.LLDP[i].ChassisID = mac
		}
	}
	for i := range s.Neighbors {
		normMAC(&s.Neighbors[i].MAC)
	}
	for i := range s.DHCP {
		normMAC(&s.DHCP[i].MAC)
	}
	for i := range s.Firewalls {
		s.Firewalls[i].NormalizeSlices()
	}
	for i := range s.Radios {
		s.Radios[i].NormalizeSlices()
	}
	for i := range s.WirelessUplinks {
		normMAC(&s.WirelessUplinks[i].PeerMAC)
	}
	for i := range s.Clients {
		normMAC(&s.Clients[i].MAC)
		s.Clients[i].NormalizeSlices()
	}
	s.Clients = slices.DeleteFunc(s.Clients, func(c model.Client) bool { return c.MAC == "" })
}

// normMAC canonicalizes *p in place and clears it when it is not a valid MAC.
func normMAC(p *string) {
	if *p == "" {
		return
	}
	mac, err := model.NormalizeMAC(*p)
	if err != nil {
		mac = ""
	}
	*p = mac
}

// MinIntervalDriver is implemented by drivers whose devices must not be polled more often
// than a floor; the poller raises a target's configured interval to it.
type MinIntervalDriver interface {
	MinPollInterval() time.Duration
}

// Driver is implemented by every platform integration (OpenWrt, RTLPlayground, Andaru, pfSense, OPNsense, UniFi, SNMP).
type Driver interface {
	Kind() string
	ValidateConfig(cfg TargetConfig) error
	Poll(ctx context.Context, cfg TargetConfig) (*Snapshot, error)
}

// DiagnosticDriver is an optional interface for drivers supporting on-demand device diagnostics.
type DiagnosticDriver interface {
	RunDiagnostic(ctx context.Context, cfg TargetConfig, req model.DiagnosticRequest) (*model.DiagnosticResult, error)
}

// ConfigDriver is the Phase 2 contract for safe save -> preview -> apply -> confirm/rollback flows.
type ConfigDriver interface {
	PreviewChange(ctx context.Context, cfg TargetConfig, change model.ChangeRequest) (*model.ChangePreview, error)
	ApplyChange(ctx context.Context, cfg TargetConfig, change model.ChangeRequest) (*model.ApplySession, error)
	ConfirmApply(ctx context.Context, cfg TargetConfig, sessionID string) error
	RollbackApply(ctx context.Context, cfg TargetConfig, sessionID string) error
}

// CollectMACs normalizes a device's interface MACs in order, dropping invalid, all-zero and
// repeated ones. primary is the first one kept, or "" when none is.
func CollectMACs(raw []string) (macs []string, primary string) {
	macs = []string{}
	for _, r := range raw {
		m, err := model.NormalizeMAC(r)
		if err != nil || m == "00:00:00:00:00:00" || slices.Contains(macs, m) {
			continue
		}
		macs = append(macs, m)
	}
	if len(macs) > 0 {
		primary = macs[0]
	}
	return macs, primary
}

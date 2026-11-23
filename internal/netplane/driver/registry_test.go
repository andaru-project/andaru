// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/model"
)

type stubDriver struct{ kind string }

func (s stubDriver) Kind() string                          { return s.kind }
func (s stubDriver) ValidateConfig(cfg TargetConfig) error { return nil }
func (s stubDriver) Poll(context.Context, TargetConfig) (*Snapshot, error) {
	return &Snapshot{}, nil
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("expected error for nil driver")
	}
	if err := r.Register(stubDriver{kind: ""}); err == nil {
		t.Fatal("expected error for empty kind")
	}
	if err := r.Register(stubDriver{kind: "unifi"}); err != nil {
		t.Fatalf("register unifi: %v", err)
	}
	if err := r.Register(stubDriver{kind: "openwrt"}); err != nil {
		t.Fatalf("register openwrt: %v", err)
	}
	if err := r.Register(stubDriver{kind: "unifi"}); err == nil {
		t.Fatal("expected duplicate kind error")
	}

	d, ok := r.Get("openwrt")
	if !ok || d.Kind() != "openwrt" {
		t.Fatalf("Get(openwrt) = (%v, %v)", d, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("expected missing driver to return false")
	}
	kinds := r.Kinds()
	if len(kinds) != 2 || kinds[0] != "openwrt" || kinds[1] != "unifi" {
		t.Fatalf("Kinds() = %v, want [openwrt unifi]", kinds)
	}
}

func TestTargetConfigRedactsSecrets(t *testing.T) {
	const secretVal = "super-secret-api-token-9999"
	cfg := TargetConfig{
		ID:              "t1",
		Name:            "Lab Firewall",
		Driver:          "pfsense",
		Endpoint:        "https://192.0.2.1",
		Enabled:         true,
		PollInterval:    15 * time.Second,
		SecretRefs:      map[string]model.SecretRef{"api_key": {Secret: "PFSENSE_API_KEY"}},
		ResolvedSecrets: map[string]string{"api_key": secretVal},
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secretVal) {
		t.Fatalf("secret leaked in JSON: %s", raw)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("polling target", "target", cfg)
	if strings.Contains(buf.String(), secretVal) {
		t.Fatalf("secret leaked in slog: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"secret_count":1`) {
		t.Fatalf("expected secret_count in log, got %s", buf.String())
	}
}

func TestSnapshotNormalize(t *testing.T) {
	rx := -22.5
	snap := &Snapshot{
		TargetID:    "t1",
		CollectedAt: time.Now().UTC(),
		Devices: []model.Device{{
			ID:         "openwrt:02:51:0a:00:00:01",
			PrimaryMAC: "02510A000001",
			MACs:       []string{"02-51-0A-00-00-01"},
		}},
		Links: []model.Link{{
			DeviceID: "openwrt:02:51:0a:00:00:01",
			IfName:   "sfp1",
			MAC:      "0251.0a00.0002",
			Media:    model.MediaSFPPl,
			SFP:      &model.SfpDom{Present: true, RxPowerDbm: &rx},
		}},
		LagGroups:       []model.LagGroup{{ID: "lag1"}},
		Segments:        []model.Segment{{ID: "seg1"}},
		Zones:           []model.Zone{{ID: "z1"}},
		FDB:             []model.FdbEntry{{DeviceID: "d1", LinkName: "lan1", MAC: "AA-BB-CC-DD-EE-FF", VLAN: 1}},
		LLDP:            []model.LldpNeighbor{{DeviceID: "d1", LocalLink: "sfp1", ChassisID: "AABBCCDDEEFF"}},
		Neighbors:       []model.NeighborEntry{{DeviceID: "d1", Family: model.IPv4, IP: "192.0.2.5", MAC: "AA:BB:CC:DD:EE:FF"}},
		DHCP:            []model.DhcpLease{{DeviceID: "d1", Family: model.IPv4, IP: "192.0.2.5", MAC: "aabb.ccdd.eeff"}},
		Firewalls:       []model.FirewallState{{DeviceID: "d1"}},
		Radios:          []model.WirelessRadio{{DeviceID: "d1"}},
		WirelessUplinks: []model.WirelessUplink{{DeviceID: "ap2", PeerMAC: "11-22-33-44-55-66"}},
		Clients:         []model.Client{{MAC: "112233445566"}},
	}

	snap.Normalize()
	if snap.Devices[0].PrimaryMAC != "02:51:0a:00:00:01" {
		t.Fatalf("device mac = %q", snap.Devices[0].PrimaryMAC)
	}
	if !snap.Links[0].IsFiber || snap.Links[0].SFP.AlertState != model.AlertWarn {
		t.Fatalf("link fiber/sfp alert = %v / %v", snap.Links[0].IsFiber, snap.Links[0].SFP.AlertState)
	}
	if snap.LLDP[0].ChassisID != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("lldp chassis = %q", snap.LLDP[0].ChassisID)
	}
	if snap.Clients[0].MAC != "11:22:33:44:55:66" {
		t.Fatalf("client mac = %q", snap.Clients[0].MAC)
	}

	// Malformed MACs (e.g. a 4-byte sit0 address) must not fail the snapshot.
	bad := &Snapshot{
		Devices: []model.Device{{ID: "d1", PrimaryMAC: "00:00:00:00", MACs: []string{"00:00:00:00", "02:51:0a:00:00:01"}}},
		Links:   []model.Link{{DeviceID: "d1", IfName: "sit0", MAC: "00:00:00:00"}},
		FDB:     []model.FdbEntry{{DeviceID: "d1", LinkName: "lan1", MAC: "invalid"}, {DeviceID: "d1", LinkName: "lan1", MAC: "02:51:0a:00:00:02"}},
		Clients: []model.Client{{MAC: "bogus"}},
	}
	bad.Normalize()
	if d := bad.Devices[0]; d.PrimaryMAC != "" || len(d.MACs) != 1 || d.MACs[0] != "02:51:0a:00:00:01" {
		t.Fatalf("device macs = %q / %v", d.PrimaryMAC, d.MACs)
	}
	if bad.Links[0].MAC != "" || len(bad.FDB) != 1 || len(bad.Clients) != 0 {
		t.Fatalf("invalid MACs kept: link=%q fdb=%v clients=%v", bad.Links[0].MAC, bad.FDB, bad.Clients)
	}
}

func TestCarryOver(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	prev := &Snapshot{CollectedAt: t0, Clients: []model.Client{{MAC: "aa:aa:aa:aa:aa:01", Online: true}}, LLDP: []model.LldpNeighbor{{LocalLink: "lan1"}}}

	cur := &Snapshot{CollectedAt: t0.Add(time.Minute)}
	cur.MarkUnavailable(SectionClients)
	cur.MarkUnavailable(SectionClients) // idempotent
	cur.CarryOver(prev, 15*time.Minute)
	if len(cur.Unavailable) != 1 || len(cur.Clients) != 1 || len(cur.LLDP) != 0 || !cur.Stale[SectionClients].Equal(t0) {
		t.Fatalf("carry: unavailable=%v clients=%v lldp=%v stale=%v", cur.Unavailable, cur.Clients, cur.LLDP, cur.Stale)
	}

	// A carried section keeps its original freshness, and expires after maxAge.
	next := &Snapshot{CollectedAt: t0.Add(20 * time.Minute), Unavailable: []Section{SectionClients}}
	next.CarryOver(cur, 15*time.Minute)
	if len(next.Clients) != 0 || next.Stale != nil {
		t.Fatalf("stale data carried past maxAge: %v", next.Clients)
	}

	// Nothing fresh in prev either: nothing to carry.
	after := &Snapshot{CollectedAt: t0.Add(21 * time.Minute), Unavailable: []Section{SectionClients}}
	after.CarryOver(next, time.Hour)
	if len(after.Clients) != 0 {
		t.Fatal("carried data from a snapshot that had none")
	}
	(&Snapshot{Unavailable: []Section{SectionClients}}).CarryOver(nil, time.Hour) // first poll: no panic
}

func TestCarryOverFreshnessRules(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	clients := []model.Client{{MAC: "aa:aa:aa:aa:aa:01"}}
	carry := func(prev *Snapshot, at time.Duration) *Snapshot {
		s := &Snapshot{CollectedAt: t0.Add(at), Unavailable: []Section{SectionClients}}
		s.CarryOver(prev, 15*time.Minute)
		return s
	}
	fresh := &Snapshot{CollectedAt: t0, Clients: clients}

	// A chain keeps the original freshness: +10m carried, +20m expired (not 10m after +10m).
	s1 := carry(fresh, 10*time.Minute)
	if len(s1.Clients) != 1 || !s1.Stale[SectionClients].Equal(t0) {
		t.Fatalf("+10m: clients=%v stale=%v", s1.Clients, s1.Stale)
	}
	if s2 := carry(s1, 20*time.Minute); len(s2.Clients) != 0 {
		t.Fatal("+20m: chained carry outlived 15 minutes")
	}

	// Data fresh in the previous poll is carried once within three poll intervals: a slow
	// interval keeps it, a restart after a long stop does not revive it.
	slow := &Snapshot{CollectedAt: t0.Add(40 * time.Minute), Unavailable: []Section{SectionClients}, PollInterval: 20 * time.Minute}
	if slow.CarryOver(fresh, 15*time.Minute); len(slow.Clients) != 1 {
		t.Fatal("slow interval: one failed poll dropped fresh data")
	}
	restart := &Snapshot{CollectedAt: t0.Add(50 * time.Minute), Unavailable: []Section{SectionClients}, PollInterval: time.Minute}
	if restart.CarryOver(fresh, 15*time.Minute); len(restart.Clients) != 0 {
		t.Fatal("carried 50-minute-old data on a 1-minute interval")
	}

	// prev marked the section unavailable and carried nothing: its leftover data is not fresh.
	dropped := &Snapshot{CollectedAt: t0, Clients: clients, Unavailable: []Section{SectionClients}}
	if s := carry(dropped, time.Minute); len(s.Clients) != 0 {
		t.Fatal("carried data prev itself could not refresh")
	}

	// A snapshot rewritten for an unreachable target is never a source.
	offline := &Snapshot{CollectedAt: t0, Clients: clients, MarkedOffline: true}
	if s := carry(offline, time.Minute); len(s.Clients) != 0 {
		t.Fatal("carried from an offline-marked snapshot")
	}

	// A clock that stepped back expires the data.
	if s := carry(fresh, -time.Hour); len(s.Clients) != 0 {
		t.Fatal("carried data across a backwards clock step")
	}
}

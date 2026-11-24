// SPDX-License-Identifier: AGPL-3.0-or-later
package events

import (
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

func TestBusPublishAndSlowSubscriberDrop(t *testing.T) {
	bus := NewBus()
	fastCh, unsubFast := bus.Subscribe(4)
	defer unsubFast()
	slowCh, unsubSlow := bus.Subscribe(1)
	defer unsubSlow()

	// Publish 3 events without reading slowCh -> slowCh buffers 1, drops 2, never blocks.
	for i := 0; i < 3; i++ {
		bus.Publish(Event{Type: TopologyUpdated, Severity: SeverityInfo, Summary: "updated"})
	}

	if bus.DroppedCount() != 2 {
		t.Fatalf("DroppedCount = %d, want 2", bus.DroppedCount())
	}
	if len(fastCh) != 3 {
		t.Fatalf("fastCh len = %d, want 3", len(fastCh))
	}
	ev := <-fastCh
	if ev.ID == "" || ev.Timestamp.IsZero() {
		t.Fatalf("expected auto-populated ID and Timestamp, got %+v", ev)
	}
	<-slowCh
	unsubSlow()
	// Calling unsub twice must be safe.
	unsubSlow()
}

func TestDiffSnapshots(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 40, 0, 0, time.UTC)

	// First poll emits active SFP warning only.
	first := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: now,
		Devices:     []model.Device{{ID: "sw1", Name: "LG510S", Online: true}},
		Links: []model.Link{{
			DeviceID: "sw1", IfName: "sfp9", LinkUp: true, StpState: model.StpForwarding,
			SFP: &model.SfpDom{Present: true, AlertState: model.AlertWarn},
		}, {
			// An empty cage's stale alert state raises nothing.
			DeviceID: "sw1", IfName: "sfp10", SFP: &model.SfpDom{Present: false, AlertState: model.AlertCrit},
		}},
		VPNs:     []model.VpnTunnel{{DeviceID: "fw1", ID: "wg0", Name: "wg-home", Up: true}},
		Gateways: []model.GatewayStatus{{DeviceID: "fw1", Name: "WAN_DHCP", Online: true}},
	}
	evs0 := DiffSnapshots(nil, first)
	if len(evs0) != 1 || evs0[0].Type != SfpAlert || evs0[0].Severity != SeverityWarn {
		t.Fatalf("unexpected initial diff events: %+v", evs0)
	}

	// Second poll triggers offline, link down, STP blocking, SFP crit, VPN down, gateway down.
	second := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: now.Add(15 * time.Second),
		Devices: []model.Device{
			{ID: "sw1", Name: "LG510S", Online: false},
			{ID: "ap1", Name: "AP-New", Online: true},
		},
		Links: []model.Link{{
			DeviceID: "sw1", IfName: "sfp9", LinkUp: false, StpState: model.StpBlocking,
			SFP: &model.SfpDom{Present: true, AlertState: model.AlertCrit},
		}},
		VPNs:     []model.VpnTunnel{{DeviceID: "fw1", ID: "wg0", Name: "wg-home", Up: false}},
		Gateways: []model.GatewayStatus{{DeviceID: "fw1", Name: "WAN_DHCP", Online: false}},
	}
	evs := DiffSnapshots(first, second)
	seen := make(map[Type]bool)
	for _, e := range evs {
		seen[e.Type] = true
		if e.Type == LinkStpChanged && (e.Details["old"] != model.StpForwarding || e.Details["new"] != model.StpBlocking) {
			t.Errorf("STP change details = %v, want forwarding -> blocking", e.Details)
		}
	}
	for _, want := range []Type{DeviceOffline, DeviceOnline, LinkDown, LinkStpChanged, SfpAlert, VpnDown, GatewayDown} {
		if !seen[want] {
			t.Errorf("missing expected event %q in %+v", want, evs)
		}
	}

	// Recovery poll triggers online, link up, VPN up, gateway up.
	third := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: now.Add(30 * time.Second),
		Devices:     []model.Device{{ID: "sw1", Name: "LG510S", Online: true}},
		Links:       []model.Link{{DeviceID: "sw1", IfName: "sfp9", LinkUp: true, StpState: model.StpForwarding}},
		VPNs:        []model.VpnTunnel{{DeviceID: "fw1", ID: "wg0", Name: "wg-home", Up: true}},
		Gateways:    []model.GatewayStatus{{DeviceID: "fw1", Name: "WAN_DHCP", Online: true}},
	}
	evs3 := DiffSnapshots(second, third)
	seen3 := make(map[Type]bool)
	for _, e := range evs3 {
		seen3[e.Type] = true
	}
	for _, want := range []Type{DeviceOnline, LinkUp, LinkStpChanged, VpnUp, GatewayUp} {
		if !seen3[want] {
			t.Errorf("missing recovery event %q in %+v", want, evs3)
		}
	}
}

func TestDiffSnapshotsVanishedDevicesAndClients(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: ts,
		Devices:     []model.Device{{ID: "d1", Name: "sw1", Online: true}},
		Clients:     []model.Client{{MAC: "aa:aa:aa:aa:aa:01", Online: true}, {MAC: "aa:aa:aa:aa:aa:02", Online: true}},
	}
	curr := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: ts.Add(time.Minute),
		Clients:     []model.Client{{MAC: "aa:aa:aa:aa:aa:02", Online: true}, {MAC: "aa:aa:aa:aa:aa:03", Hostname: "phone", Online: true}},
	}
	got := map[Type]int{}
	for _, ev := range DiffSnapshots(prev, curr) {
		got[ev.Type]++
	}
	if got[DeviceOffline] != 1 || got[ClientJoined] != 1 || got[ClientLeft] != 1 || len(got) != 3 {
		t.Fatalf("events = %v", got)
	}
}

func TestDiffSnapshotsNewLinkSfpAlert(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := &driver.Snapshot{TargetID: "t1", CollectedAt: ts}
	curr := &driver.Snapshot{
		TargetID:    "t1",
		CollectedAt: ts.Add(time.Minute),
		Links: []model.Link{
			{DeviceID: "sw1", IfName: "sfp9", LinkUp: true, StpState: model.StpBlocking,
				SFP: &model.SfpDom{Present: true, AlertState: model.AlertCrit}},
			{DeviceID: "sw1", IfName: "p1", LinkUp: true, StpState: model.StpForwarding},
		},
	}
	evs := DiffSnapshots(prev, curr)
	if len(evs) != 1 || evs[0].Type != SfpAlert || evs[0].Severity != SeverityCrit || evs[0].LinkName != "sfp9" {
		t.Fatalf("events = %+v, want one crit SFP alert for sfp9", evs)
	}
}

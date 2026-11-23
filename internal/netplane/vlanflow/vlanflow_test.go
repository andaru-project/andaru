// SPDX-License-Identifier: AGPL-3.0-or-later
package vlanflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/andaru-project/andaru/internal/netplane/model"
	"github.com/andaru-project/andaru/internal/netplane/store"
	"github.com/andaru-project/andaru/internal/netplane/topology"
)

func ip(v int) *int { return &v }

// fw ==bond0/lag1== core; core:lan1 -> office:lan1 (VLAN 40 missing on core);
// core:lan2 -> poe:port1 (PVID 10 vs 20); office ~~wireless~~ ap.
func fixture() (*store.NetworkState, topology.Graph) {
	state := &store.NetworkState{}
	state.Devices = []model.Device{
		{ID: "fw", Name: "fw-core", Role: model.RoleFirewall}, {ID: "core", Name: "sw-core"}, {ID: "office", Name: "sw-office"},
		{ID: "poe", Name: "sw-poe"}, {ID: "ap", Name: "ap-mesh"},
	}
	state.LagGroups = []model.LagGroup{
		{DeviceID: "fw", ID: "bond0", Name: "bond0", MemberLinks: []string{"sfp0", "sfp1"}},
		{DeviceID: "core", ID: "lag1", Name: "lag1", MemberLinks: []string{"sfp9", "sfp10"}},
	}
	state.Segments = []model.Segment{
		{DeviceID: "fw", AdminUp: true, ID: "lan", Name: "LAN", ParentLink: "bond0", VLANID: ip(10), ZoneID: "z-lan", Addresses: []model.IPAddress{{Address: "10.10.10.1", PrefixLength: 24}}},
		{DeviceID: "fw", AdminUp: true, ID: "iot", Name: "IoT", ParentLink: "bond0", VLANID: ip(20), Addresses: []model.IPAddress{{Address: "10.10.20.1", PrefixLength: 24}}},
		{DeviceID: "fw", AdminUp: true, ID: "guest", Name: "Guest", ParentLink: "bond0", VLANID: ip(30), Addresses: []model.IPAddress{{Address: "10.10.30.1", PrefixLength: 24}}},
		{DeviceID: "fw", AdminUp: true, ID: "lab", Name: "Lab", ParentLink: "bond0", VLANID: ip(40), Addresses: []model.IPAddress{{Address: "10.10.40.1", PrefixLength: 24}}},
	}
	state.Zones = []model.Zone{{DeviceID: "fw", ID: "z-lan", Name: "lan"}}
	state.Links = []model.Link{
		{DeviceID: "fw", IfName: "sfp0"},
		{DeviceID: "core", IfName: "sfp9", PVID: ip(1), TaggedVLANs: []int{10, 20, 30, 40}},
		{DeviceID: "core", IfName: "lan1", PVID: ip(1), TaggedVLANs: []int{10, 20, 30}},
		{DeviceID: "core", IfName: "lan2", PVID: ip(10), TaggedVLANs: []int{20}},
		{DeviceID: "core", IfName: "lan5", PVID: ip(10), OperState: model.OperUp},
		{DeviceID: "office", IfName: "lan1", PVID: ip(1), TaggedVLANs: []int{10, 20, 40}},
		{DeviceID: "office", IfName: "lan4", PVID: ip(40), Label: "bench", OperState: model.OperUp},
		{DeviceID: "poe", IfName: "port1", PVID: ip(20), TaggedVLANs: []int{10}},
	}
	state.Radios = []model.WirelessRadio{{DeviceID: "ap", Name: "wifi1", SSIDs: []model.Ssid{{Name: "Home", VLANID: ip(10), ClientCount: 3}}}}
	state.Clients = []model.Client{{MAC: "aa", Online: true, VLAN: ip(40)}}

	g := topology.Graph{}
	for _, d := range state.Devices {
		g.Nodes = append(g.Nodes, topology.Node{ID: d.ID, Kind: topology.NodeDevice})
	}
	g.Nodes = append(g.Nodes, topology.Node{ID: "client", Kind: topology.NodeClient})
	g.Edges = []topology.Edge{
		{ID: "e1", FromID: "fw", FromLink: "bond0", ToID: "core", ToLink: "lag1", Kind: topology.EdgeLAG},
		{ID: "e2", FromID: "core", FromLink: "lan1", ToID: "office", ToLink: "lan1", Kind: topology.EdgeWired},
		{ID: "e3", FromID: "core", FromLink: "lan2", ToID: "poe", ToLink: "port1", Kind: topology.EdgeWired},
		{ID: "e4", FromID: "office", ToID: "ap", Kind: topology.EdgeWirelessUplink},
		{ID: "e5", FromID: "office", FromLink: "lan4", ToID: "client", Kind: topology.EdgeWired},
	}
	return state, g
}

func vlan(t *testing.T, f Flow, id int) VLAN {
	t.Helper()
	for _, v := range f.VLANs {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("VLAN %d missing", id)
	return VLAN{}
}

func hasHop(v VLAN, edge string) bool {
	return slices.ContainsFunc(v.Hops, func(h Hop) bool { return h.EdgeID == edge })
}

func hop(t *testing.T, v VLAN, edge string) Hop {
	t.Helper()
	for _, h := range v.Hops {
		if h.EdgeID == edge {
			return h
		}
	}
	t.Fatalf("VLAN %d has no hop on %s", v.ID, edge)
	return Hop{}
}

func TestAnalyze(t *testing.T) {
	f := Analyze(fixture())

	var ids []int
	for _, v := range f.VLANs {
		ids = append(ids, v.ID)
	}
	if !slices.Equal(ids, []int{1, 10, 20, 30, 40}) {
		t.Fatalf("VLANs = %v", ids)
	}

	v10 := vlan(t, f, 10)
	if v10.Name != "LAN" || len(v10.Gateways) != 1 || v10.Gateways[0].ZoneName != "lan" {
		t.Errorf("VLAN 10 name/gateways = %q %+v", v10.Name, v10.Gateways)
	}
	// The LAG's VLANs come from the firewall's segments and the switch's first member port.
	if h := hop(t, v10, "e1"); h.FromMode != ModeTagged || h.ToMode != ModeTagged || h.State != HopCarried {
		t.Errorf("VLAN 10 on e1 = %+v", h)
	}
	if h := hop(t, v10, "e3"); h.FromMode != ModeUntagged || h.ToMode != ModeTagged || h.State != HopLeak {
		t.Errorf("VLAN 10 on e3 = %+v", h)
	}
	if h := hop(t, v10, "e4"); h.State != HopUnverified {
		t.Errorf("VLAN 10 on the wireless uplink = %+v", h)
	}
	if !slices.Equal(v10.Reached, []string{"ap", "core", "fw", "office"}) {
		t.Errorf("VLAN 10 reached = %v", v10.Reached)
	}
	if len(v10.SSIDs) != 1 || !v10.SSIDs[0].Reachable {
		t.Errorf("VLAN 10 SSIDs = %+v", v10.SSIDs)
	}
	if len(v10.AccessPorts) != 1 || v10.AccessPorts[0].Link != "lan5" {
		t.Errorf("VLAN 10 access ports = %+v (infra ports must be excluded)", v10.AccessPorts)
	}

	v40 := vlan(t, f, 40)
	if h := hop(t, v40, "e2"); h.FromMode != ModeNone || h.ToMode != ModeTagged || h.State != HopDeadEnd {
		t.Errorf("VLAN 40 on e2 = %+v", h)
	}
	if !slices.Equal(v40.Reached, []string{"core", "fw"}) || v40.Clients != 1 {
		t.Errorf("VLAN 40 reached = %v clients = %d", v40.Reached, v40.Clients)
	}
	if len(v40.AccessPorts) != 1 || v40.AccessPorts[0].Reachable {
		t.Errorf("VLAN 40 access ports = %+v", v40.AccessPorts)
	}

	// Trunks carrying a VLAN toward a device that never uses it are unpruned, not broken: no finding.
	if v1 := vlan(t, f, 1); len(v1.Gateways) != 0 || len(v1.Reached) != 0 || hasHop(v1, "e1") {
		t.Errorf("VLAN 1 = %+v", v1)
	}
	if hasHop(vlan(t, f, 30), "e2") {
		t.Errorf("VLAN 30 on e2: an unpruned trunk must be left out")
	}

	type key struct {
		kind  FindingKind
		vlans string
		edge  string
	}
	var got []key
	for _, x := range f.Findings {
		got = append(got, key{x.Kind, fmt.Sprint(x.VLANs), x.EdgeID})
	}
	want := []key{
		{FindingLeak, "[10 20]", "e3"},
		{FindingDeadEnd, "[40]", "e2"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
	de := f.Findings[1]
	if de.DeviceID != "office" || de.PeerID != "core" || !slices.Equal(de.Stranded, []string{"office"}) ||
		de.Message != "VLAN 40 is tagged on sw-office:lan1 but missing on sw-core:lan1" {
		t.Errorf("dead-end finding = %+v", de)
	}
}

func TestHopState(t *testing.T) {
	for _, c := range []struct {
		from, to Mode
		want     HopState
	}{
		{ModeNone, ModeNone, ""},
		{ModeTagged, ModeTagged, HopCarried},
		{ModeUntagged, ModeUntagged, HopCarried},
		{ModeTagged, ModeNone, HopDeadEnd},
		{ModeNone, ModeUntagged, HopDeadEnd},
		{ModeTagged, ModeUntagged, HopMismatch},
		{ModeUnknown, ModeNone, HopDeadEnd},
		{ModeUnknown, ModeUnknown, HopUnverified},
		{ModeTagged, ModeUnknown, HopUnverified},
	} {
		if got := hopState(c.from, c.to); got != c.want {
			t.Errorf("hopState(%s, %s) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestAnalyzeNil(t *testing.T) {
	f := Analyze(nil, topology.Graph{})
	if f.VLANs == nil || f.Findings == nil {
		t.Fatal("nil state must yield empty, non-nil slices")
	}
}

// chain builds fw -> a -> b -> c over wired edges e1..e3 with the given ports, for edge cases.
func chain(links []model.Link, segs []model.Segment) (*store.NetworkState, topology.Graph) {
	state := &store.NetworkState{Links: links, Segments: segs}
	g := topology.Graph{}
	for _, d := range []string{"fw", "a", "b", "c"} {
		dev := model.Device{ID: d, Name: d}
		if d == "fw" {
			dev.Role = model.RoleFirewall
		}
		state.Devices = append(state.Devices, dev)
		g.Nodes = append(g.Nodes, topology.Node{ID: d, Kind: topology.NodeDevice})
	}
	g.Edges = []topology.Edge{
		{ID: "e1", FromID: "fw", FromLink: "p0", ToID: "a", ToLink: "up", Kind: topology.EdgeWired},
		{ID: "e2", FromID: "a", FromLink: "down", ToID: "b", ToLink: "up", Kind: topology.EdgeWired},
		{ID: "e3", FromID: "b", FromLink: "down", ToID: "c", ToLink: "up", Kind: topology.EdgeWired},
	}
	return state, g
}

func TestAnalyzeEdgeCases(t *testing.T) {
	seg := []model.Segment{{DeviceID: "fw", AdminUp: true, ID: "s", Name: "S", ParentLink: "p0", VLANID: ip(20)}} // no address: still a gateway

	t.Run("unknown end facing a known none blocks reach", func(t *testing.T) {
		// a reports no VLAN data on any port (unknown); b's uplink excludes VLAN 20.
		f := Analyze(chain([]model.Link{
			{DeviceID: "b", IfName: "up", PVID: ip(1), TaggedVLANs: []int{10}},
			{DeviceID: "b", IfName: "down", PVID: ip(1), TaggedVLANs: []int{20}},
			{DeviceID: "c", IfName: "up", PVID: ip(1), TaggedVLANs: []int{20}},
			{DeviceID: "c", IfName: "p", PVID: ip(20)},
		}, seg))
		v := vlan(t, f, 20)
		if h := hop(t, v, "e2"); h.FromMode != ModeUnknown || h.ToMode != ModeNone || h.State != HopDeadEnd {
			t.Errorf("e2 = %+v", h)
		}
		if !slices.Equal(v.Reached, []string{"a", "fw"}) {
			t.Errorf("reached = %v: a known none end must block the VLAN", v.Reached)
		}
		if len(f.Findings) != 1 || !slices.Equal(f.Findings[0].Stranded, []string{"c"}) ||
			f.Findings[0].Message != "VLAN 20 is missing on b:up, and a:down reports no VLAN membership" {
			t.Errorf("findings = %+v", f.Findings)
		}
	})

	t.Run("stranded lists only the side cut off from the gateway", func(t *testing.T) {
		// VLAN 20 is carried fw -> a, missing on a:down, and b, c use it.
		f := Analyze(chain([]model.Link{
			{DeviceID: "a", IfName: "up", PVID: ip(1), TaggedVLANs: []int{20}},
			{DeviceID: "a", IfName: "down", PVID: ip(1), TaggedVLANs: []int{10}},
			{DeviceID: "b", IfName: "up", PVID: ip(1), TaggedVLANs: []int{20}},
			{DeviceID: "b", IfName: "down", PVID: ip(1), TaggedVLANs: []int{20}},
			{DeviceID: "c", IfName: "up", PVID: ip(1), TaggedVLANs: []int{20}},
			{DeviceID: "c", IfName: "p", PVID: ip(20)},
		}, seg))
		if v := vlan(t, f, 20); len(v.Gateways) != 1 || !slices.Equal(v.Reached, []string{"a", "fw"}) {
			t.Fatalf("VLAN 20 gateways = %+v reached = %v", v.Gateways, v.Reached)
		}
		// b only carries the VLAN on trunks, so c, the device with an endpoint, is the one stranded.
		if len(f.Findings) != 1 || f.Findings[0].EdgeID != "e2" || !slices.Equal(f.Findings[0].Stranded, []string{"c"}) {
			t.Errorf("findings = %+v", f.Findings)
		}
	})

	t.Run("a transit switch missing the VLAN is a dead end, not unused", func(t *testing.T) {
		// a carries only VLAN 10 but sits between the gateway and b, which uses VLAN 20.
		f := Analyze(chain([]model.Link{
			{DeviceID: "a", IfName: "up", PVID: ip(1), TaggedVLANs: []int{10}},
			{DeviceID: "a", IfName: "down", PVID: ip(1), TaggedVLANs: []int{10}},
			{DeviceID: "b", IfName: "up", PVID: ip(1), TaggedVLANs: []int{10, 20}},
			{DeviceID: "b", IfName: "p5", PVID: ip(20)},
		}, []model.Segment{{DeviceID: "fw", AdminUp: true, ID: "s", ParentLink: "p0", VLANID: ip(20)}}))
		v := vlan(t, f, 20)
		if h := hop(t, v, "e1"); h.State != HopDeadEnd {
			t.Errorf("e1 = %+v", h)
		}
		if h := hop(t, v, "e2"); h.State != HopDeadEnd {
			t.Errorf("e2 = %+v", h)
		}
		var stranded []string
		for _, x := range f.Findings {
			if x.Kind == FindingDeadEnd && x.EdgeID == "e2" {
				stranded = x.Stranded
			}
		}
		if !slices.Equal(stranded, []string{"b"}) || len(v.AccessPorts) != 1 || v.AccessPorts[0].Reachable {
			t.Errorf("stranded = %v, access ports = %+v", stranded, v.AccessPorts)
		}
	})

	t.Run("an untagged routed segment is the gateway of its peer's PVID", func(t *testing.T) {
		f := Analyze(chain([]model.Link{
			{DeviceID: "a", IfName: "up", PVID: ip(1), TaggedVLANs: []int{20}},
		}, []model.Segment{
			{DeviceID: "fw", AdminUp: true, ID: "lan", Name: "LAN", ParentLink: "p0"},
			{DeviceID: "fw", AdminUp: true, ID: "iot", Name: "IoT", ParentLink: "p0", VLANID: ip(20)},
		}))
		v1 := vlan(t, f, 1)
		if v1.Name != "LAN" || len(v1.Gateways) != 1 || !slices.Equal(v1.Reached, []string{"a", "fw"}) {
			t.Errorf("VLAN 1 = %+v", v1)
		}
		if h := hop(t, v1, "e1"); h.FromMode != ModeUntagged || h.ToMode != ModeUntagged || h.State != HopCarried {
			t.Errorf("VLAN 1 on e1 = %+v", h)
		}
		if h := hop(t, vlan(t, f, 20), "e1"); h.FromMode != ModeTagged || h.State != HopCarried || !slices.Equal(h.FromTagged, []int{20}) {
			t.Errorf("VLAN 20 on e1 = %+v", h)
		}
	})

	t.Run("a wireless uplink stays unverified when the parent AP only bridges the VLAN", func(t *testing.T) {
		state, g := chain([]model.Link{
			{DeviceID: "a", IfName: "up", PVID: ip(1), TaggedVLANs: []int{50}},
		}, []model.Segment{{DeviceID: "fw", AdminUp: true, ID: "s", ParentLink: "p0", VLANID: ip(50)}})
		state.Radios = []model.WirelessRadio{{DeviceID: "c", Name: "r", SSIDs: []model.Ssid{{Name: "Mesh", VLANID: ip(50)}}}}
		g.Edges = []topology.Edge{g.Edges[0], {ID: "w", FromID: "a", ToID: "c", Kind: topology.EdgeWirelessUplink}}
		v := vlan(t, Analyze(state, g), 50)
		if h := hop(t, v, "w"); h.State != HopUnverified {
			t.Errorf("wireless hop = %+v", h)
		}
		if len(v.SSIDs) != 1 || !v.SSIDs[0].Reachable {
			t.Errorf("SSIDs = %+v", v.SSIDs)
		}
	})

	t.Run("tag mismatch, and LAG members that disagree", func(t *testing.T) {
		state, g := chain([]model.Link{
			{DeviceID: "a", IfName: "m1", PVID: ip(1), TaggedVLANs: []int{20, 30}},
			{DeviceID: "a", IfName: "m2", PVID: ip(1), TaggedVLANs: []int{20}},
			{DeviceID: "a", IfName: "down", PVID: ip(20), TaggedVLANs: []int{20, 30}},
			{DeviceID: "b", IfName: "up", PVID: ip(20), TaggedVLANs: []int{30}},
			{DeviceID: "b", IfName: "p", PVID: ip(30)},
		}, []model.Segment{
			{DeviceID: "fw", AdminUp: true, ID: "s20", ParentLink: "p0", VLANID: ip(20)},
			{DeviceID: "fw", AdminUp: true, ID: "s30", ParentLink: "p0", VLANID: ip(30)},
		})
		state.LagGroups = []model.LagGroup{{DeviceID: "a", ID: "bond", Name: "bond", MemberLinks: []string{"m1", "m2"}}}
		g.Edges[0].ToLink = "bond"
		f := Analyze(state, g)
		// The LAG carries only what every member carries: VLAN 30 is missing on m2.
		if h := hop(t, vlan(t, f, 20), "e1"); h.ToMode != ModeTagged || h.State != HopCarried || *h.ToPVID != 1 || !slices.Equal(h.ToTagged, []int{20}) {
			t.Errorf("VLAN 20 on the LAG = %+v", h)
		}
		if h := hop(t, vlan(t, f, 30), "e1"); h.ToMode != ModeNone || h.State != HopDeadEnd {
			t.Errorf("VLAN 30 on the LAG = %+v", h)
		}
		var got []string
		for _, x := range f.Findings {
			got = append(got, fmt.Sprint(x.Kind, x.VLANs, x.EdgeID))
		}
		// a:down tags VLAN 20 (its PVID too) while b:up carries it untagged: same PVID, so no leak.
		if want := []string{"tag_mismatch[20]e2", "dead_end[30]e1"}; !slices.Equal(got, want) {
			t.Errorf("findings = %v, want %v", got, want)
		}

		// Members with different PVIDs split untagged frames across VLANs.
		state.Links[1].PVID = ip(20)
		f = Analyze(state, g)
		if f.Findings[0].Kind != FindingLeak || f.Findings[0].Link != "bond" || !slices.Equal(f.Findings[0].VLANs, []int{1, 20}) {
			t.Errorf("findings = %+v", f.Findings)
		}
		if h := hop(t, vlan(t, f, 1), "e1"); h.State != HopLeak {
			t.Errorf("VLAN 1 hop on split-PVID LAG = %+v, want leak", h)
		}
		if h := hop(t, vlan(t, f, 20), "e1"); h.State != HopLeak {
			t.Errorf("VLAN 20 hop on split-PVID LAG = %+v, want leak", h)
		}

		// Peer with a third PVID (30) facing the split-PVID LAG [1, 20] emits cross-end leak findings too.
		state.Links = append(state.Links, model.Link{DeviceID: "fw", IfName: "p0", PVID: ip(30), TaggedVLANs: []int{20}})
		f = Analyze(state, g)
		if h := hop(t, vlan(t, f, 30), "e1"); h.State != HopLeak {
			t.Errorf("VLAN 30 hop facing split-PVID LAG = %+v, want leak", h)
		}
		var leaks []string
		for _, x := range f.Findings {
			if x.Kind == FindingLeak && x.EdgeID == "e1" {
				leaks = append(leaks, fmt.Sprint(x.VLANs))
			}
		}
		if want := []string{"[1 20]", "[30 1]", "[30 20]"}; !slices.Equal(leaks, want) {
			t.Errorf("split-PVID LAG leaks = %v, want %v", leaks, want)
		}
	})

	t.Run("a ring does not turn an unpruned trunk into a dead end", func(t *testing.T) {
		state, g := chain([]model.Link{
			{DeviceID: "a", IfName: "up", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "a", IfName: "down", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "b", IfName: "up", PVID: ip(1)},
		}, []model.Segment{{DeviceID: "fw", AdminUp: true, ID: "s", ParentLink: "p0", VLANID: ip(30)}})
		g.Edges = append(g.Edges, topology.Edge{ID: "e4", FromID: "c", FromLink: "x", ToID: "a", ToLink: "x", Kind: topology.EdgeWired})
		if f := Analyze(state, g); len(f.Findings) != 0 || hasHop(vlan(t, f, 30), "e2") {
			t.Errorf("findings = %+v", f.Findings)
		}
	})

	t.Run("a link missing a VLAN every user reaches another way is redundant", func(t *testing.T) {
		state, g := chain([]model.Link{
			{DeviceID: "a", IfName: "up", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "a", IfName: "down", PVID: ip(1)},
			{DeviceID: "a", IfName: "x", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "c", IfName: "x", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "c", IfName: "up", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "b", IfName: "down", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "b", IfName: "up", PVID: ip(1), TaggedVLANs: []int{30}},
		}, []model.Segment{{DeviceID: "fw", AdminUp: true, ID: "s", ParentLink: "p0", VLANID: ip(30)}})
		g.Edges = append(g.Edges, topology.Edge{ID: "e4", FromID: "a", FromLink: "x", ToID: "c", ToLink: "x", Kind: topology.EdgeWired})
		f := Analyze(state, g)
		if v := vlan(t, f, 30); len(f.Findings) != 0 || hasHop(v, "e2") || !slices.Equal(v.Reached, []string{"a", "b", "c", "fw"}) {
			t.Errorf("VLAN 30 = %+v, findings = %+v", v, f.Findings)
		}
	})

	t.Run("an L2-only VLAN split by a link is a dead end with nothing stranded", func(t *testing.T) {
		f := Analyze(chain([]model.Link{
			{DeviceID: "a", IfName: "down", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "a", IfName: "q", PVID: ip(60)},
			{DeviceID: "b", IfName: "up", PVID: ip(1)},
			{DeviceID: "b", IfName: "p", PVID: ip(60)},
			{DeviceID: "b", IfName: "p", PVID: ip(60)}, // a duplicate row lists the port once
		}, nil))
		v := vlan(t, f, 60)
		if len(f.Findings) != 1 || f.Findings[0].EdgeID != "e2" || len(f.Findings[0].Stranded) != 0 || len(v.AccessPorts) != 2 {
			t.Errorf("findings = %+v, access ports = %+v", f.Findings, v.AccessPorts)
		}
	})

	t.Run("an end without VLAN data facing a VLAN its side never uses is no dead end", func(t *testing.T) {
		// a reports nothing; VLAN 60 lives only on b and c, so the a-b link does not need it.
		f := Analyze(chain([]model.Link{
			{DeviceID: "b", IfName: "up", PVID: ip(1)},
			{DeviceID: "b", IfName: "down", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "b", IfName: "p", PVID: ip(60)},
			{DeviceID: "c", IfName: "up", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "c", IfName: "p", PVID: ip(60)},
		}, nil))
		if v := vlan(t, f, 60); len(f.Findings) != 0 || hasHop(v, "e2") {
			t.Errorf("findings = %+v", f.Findings)
		}
	})

	t.Run("a ring lists each stranded device once, and an L2-only ring has no dead end", func(t *testing.T) {
		// Ring fw-a-b-c-fw: VLAN 30 is missing on b's side of a-b and on fw's side of c-fw.
		links := []model.Link{
			{DeviceID: "a", IfName: "up", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "a", IfName: "down", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "a", IfName: "p", PVID: ip(30)},
			{DeviceID: "b", IfName: "up", PVID: ip(1)},
			{DeviceID: "b", IfName: "down", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "b", IfName: "p", PVID: ip(30)},
			{DeviceID: "c", IfName: "up", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "c", IfName: "x", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "c", IfName: "p", PVID: ip(30)},
			{DeviceID: "fw", IfName: "x", PVID: ip(1)},
		}
		state, g := chain(links, []model.Segment{{DeviceID: "fw", AdminUp: true, ID: "s", ParentLink: "p0", VLANID: ip(30)}})
		g.Edges = append(g.Edges, topology.Edge{ID: "e4", FromID: "c", FromLink: "x", ToID: "fw", ToLink: "x", Kind: topology.EdgeWired})
		f := Analyze(state, g)
		for _, x := range f.Findings {
			if !slices.Equal(x.Stranded, []string{"b", "c"}) {
				t.Errorf("finding %s stranded = %v, want [b c]", x.EdgeID, x.Stranded)
			}
		}
		if len(f.Findings) != 2 {
			t.Errorf("findings = %+v", f.Findings)
		}

		// Without a gateway, b still reaches a through c: the missing a-b link splits nothing.
		state.Segments = nil
		links[9].TaggedVLANs = []int{30}
		if f := Analyze(state, g); len(f.Findings) != 0 {
			t.Errorf("L2-only findings = %+v", f.Findings)
		}
	})

	t.Run("a VLAN trunked toward a device without VLAN data counts as used", func(t *testing.T) {
		// c reports nothing; b trunks VLAN 30 to it, but a drops VLAN 30 on the way.
		f := Analyze(chain([]model.Link{
			{DeviceID: "a", IfName: "up", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "a", IfName: "down", PVID: ip(1)},
			{DeviceID: "b", IfName: "up", PVID: ip(1), TaggedVLANs: []int{30}},
			{DeviceID: "b", IfName: "down", PVID: ip(1), TaggedVLANs: []int{30}},
		}, []model.Segment{{DeviceID: "fw", AdminUp: true, ID: "s", ParentLink: "p0", VLANID: ip(30)}}))
		if len(f.Findings) != 1 || f.Findings[0].EdgeID != "e2" || !slices.Equal(f.Findings[0].Stranded, []string{"b"}) {
			t.Errorf("findings = %+v", f.Findings)
		}
	})

	t.Run("an L2-only VLAN whose users connect around a ring is not split", func(t *testing.T) {
		// Ring a-b-c-fw-a (fw only switches here): a carries VLAN 60 nowhere, but b and fw still tag
		// it toward a; b and fw have users and connect through c.
		state, g := chain([]model.Link{
			{DeviceID: "fw", IfName: "p0", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "fw", IfName: "p9", PVID: ip(60)},
			{DeviceID: "fw", IfName: "x", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "a", IfName: "up", PVID: ip(1)},
			{DeviceID: "a", IfName: "down", PVID: ip(1)},
			{DeviceID: "b", IfName: "up", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "b", IfName: "down", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "b", IfName: "p9", PVID: ip(60)},
			{DeviceID: "c", IfName: "up", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "c", IfName: "x", PVID: ip(1), TaggedVLANs: []int{60}},
		}, nil)
		g.Edges = append(g.Edges, topology.Edge{ID: "e4", FromID: "c", FromLink: "x", ToID: "fw", ToLink: "x", Kind: topology.EdgeWired})
		if f := Analyze(state, g); len(f.Findings) != 0 {
			t.Errorf("findings = %+v", f.Findings)
		}
	})

	t.Run("a LAG with three member PVIDs raises one leak per pair with the lowest", func(t *testing.T) {
		state, g := chain([]model.Link{
			{DeviceID: "a", IfName: "m1", PVID: ip(10)},
			{DeviceID: "a", IfName: "m2", PVID: ip(20)},
			{DeviceID: "a", IfName: "m3", PVID: ip(30)},
		}, nil)
		state.LagGroups = []model.LagGroup{{DeviceID: "a", ID: "bond", Name: "bond", MemberLinks: []string{"m1", "m2", "m3"}}}
		g.Edges[0].ToLink = "bond"
		var got []string
		for _, x := range Analyze(state, g).Findings {
			got = append(got, fmt.Sprint(x.Kind, x.VLANs, x.DeviceID, x.PeerID))
		}
		if want := []string{"leak[10 20]afw", "leak[10 30]afw"}; !slices.Equal(got, want) {
			t.Errorf("findings = %v, want %v", got, want)
		}
	})

	t.Run("a triangle isolating one switch reports both of its links", func(t *testing.T) {
		// a tags VLAN 60 toward b and c, which both drop it; b-c carries it. Every device has a user.
		state, g := chain([]model.Link{
			{DeviceID: "a", IfName: "down", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "a", IfName: "x", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "a", IfName: "p", PVID: ip(60)},
			{DeviceID: "b", IfName: "up", PVID: ip(1)},
			{DeviceID: "b", IfName: "down", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "b", IfName: "p", PVID: ip(60)},
			{DeviceID: "c", IfName: "up", PVID: ip(1), TaggedVLANs: []int{60}},
			{DeviceID: "c", IfName: "x", PVID: ip(1)},
			{DeviceID: "c", IfName: "p", PVID: ip(60)},
		}, nil)
		g.Edges = append(g.Edges[1:], topology.Edge{ID: "e4", FromID: "a", FromLink: "x", ToID: "c", ToLink: "x", Kind: topology.EdgeWired})
		var edges []string
		for _, x := range Analyze(state, g).Findings {
			edges = append(edges, x.EdgeID)
		}
		if !slices.Equal(edges, []string{"e2", "e4"}) {
			t.Errorf("dead ends on %v, want [e2 e4]", edges)
		}
	})

	t.Run("an untagged segment on a port that reports its PVID is that VLAN's gateway", func(t *testing.T) {
		f := Analyze(chain([]model.Link{
			{DeviceID: "fw", IfName: "p0", PVID: ip(10)},
			{DeviceID: "a", IfName: "up", PVID: ip(10)},
		}, []model.Segment{{DeviceID: "fw", AdminUp: true, ID: "lan", Name: "LAN", ParentLink: "p0"}}))
		if v := vlan(t, f, 10); len(v.Gateways) != 1 || !slices.Equal(v.Reached, []string{"a", "fw"}) {
			t.Errorf("VLAN 10 = %+v", v)
		}
	})

	t.Run("empty lists encode as arrays, not null", func(t *testing.T) {
		b, err := json.Marshal(Analyze(chain([]model.Link{{DeviceID: "a", IfName: "down", PVID: ip(70)}, {DeviceID: "b", IfName: "up", PVID: ip(70)}}, nil)))
		if err != nil || bytes.Contains(b, []byte("null")) {
			t.Errorf("JSON = %s, err = %v", b, err)
		}
	})

	t.Run("access ports, SSIDs, and SSID fallback name are sorted deterministically", func(t *testing.T) {
		state, g := chain([]model.Link{
			{DeviceID: "b", IfName: "p9", PVID: ip(55)},
			{DeviceID: "a", IfName: "p2", PVID: ip(55)},
			{DeviceID: "a", IfName: "p1", PVID: ip(55)},
		}, nil)
		state.Radios = []model.WirelessRadio{
			{DeviceID: "b", Name: "radio1", SSIDs: []model.Ssid{{Name: "Zulu", VLANID: ip(55)}}},
			{DeviceID: "a", Name: "radio0", SSIDs: []model.Ssid{{Name: "Alpha", VLANID: ip(55)}}},
		}
		v := vlan(t, Analyze(state, g), 55)
		if v.Name != "Alpha" {
			t.Errorf("fallback VLAN name = %q, want Alpha", v.Name)
		}
		var apKeys []string
		for _, ap := range v.AccessPorts {
			apKeys = append(apKeys, ap.DeviceID+":"+ap.Link)
		}
		if want := []string{"a:p1", "a:p2", "b:p9"}; !slices.Equal(apKeys, want) {
			t.Errorf("access ports = %v, want %v", apKeys, want)
		}
		var ssidKeys []string
		for _, s := range v.SSIDs {
			ssidKeys = append(ssidKeys, s.DeviceID+":"+s.Radio+":"+s.Name)
		}
		if want := []string{"a:radio0:Alpha", "b:radio1:Zulu"}; !slices.Equal(ssidKeys, want) {
			t.Errorf("SSIDs = %v, want %v", ssidKeys, want)
		}
	})

	t.Run("reserved VLAN IDs are ignored", func(t *testing.T) {
		f := Analyze(chain([]model.Link{
			{DeviceID: "a", IfName: "down", PVID: ip(4095), TaggedVLANs: []int{0}},
			{DeviceID: "b", IfName: "up", PVID: ip(1)},
		}, nil))
		for _, v := range f.VLANs {
			if v.ID == 0 || v.ID == 4095 {
				t.Errorf("reserved VLAN %d listed", v.ID)
			}
		}
		if len(f.Findings) != 0 {
			t.Errorf("findings = %+v: a reserved PVID is no leak", f.Findings)
		}
	})
}

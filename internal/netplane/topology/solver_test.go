// SPDX-License-Identifier: AGPL-3.0-or-later
package topology

import (
	"reflect"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/model"
	"github.com/andaru-project/andaru/internal/netplane/store"
)

func TestSolveMultiVendorTopology(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 30, 0, 0, time.UTC)
	txRate := 1200

	// 4-vendor chain:
	// andaru (firewall, root) ==[LAG bond0: sfp0+sfp1, LLDP]==> rtlplayground switch (LG510S)
	//   LG510S --[lan2, FDB-only]--> unifi AP1 --[wireless mesh]--> unifi AP2 --[wifi]--> phone
	//   LG510S --[lan4, FDB leaf]--> wired workstation
	state := &store.NetworkState{
		Devices: []model.Device{
			{
				ID: "andaru:00:11:22:33:44:01", Driver: "andaru", Name: "core-fw", Hostname: "core-fw",
				Role: model.RoleFirewall, PrimaryMAC: "00:11:22:33:44:01", MACs: []string{"00:11:22:33:44:01"},
				MgmtIP: "192.0.2.1", Online: true, Capabilities: []model.Capability{model.CapGateway, model.CapLAG},
			},
			{
				ID: "rtlplayground:02:51:0a:00:00:01", Driver: "rtlplayground", Name: "LG510S", Hostname: "lg510s",
				Role: model.RoleSwitch, PrimaryMAC: "02:51:0a:00:00:01", MACs: []string{"02:51:0a:00:00:01"},
				MgmtIP: "192.0.2.10", Online: true, Capabilities: []model.Capability{model.CapLinks, model.CapFDB, model.CapLLDP, model.CapLAG},
			},
			{
				ID: "unifi:84:78:48:00:00:01", Driver: "unifi", Name: "AP-Office", Hostname: "ap-office",
				Role: model.RoleAP, PrimaryMAC: "84:78:48:00:00:01", MACs: []string{"84:78:48:00:00:01"},
				MgmtIP: "192.0.2.20", Online: true, Capabilities: []model.Capability{model.CapWireless},
			},
			{
				ID: "unifi:84:78:48:00:00:02", Driver: "unifi", Name: "AP-Mesh", Hostname: "ap-mesh",
				Role: model.RoleAP, PrimaryMAC: "84:78:48:00:00:02", MACs: []string{"84:78:48:00:00:02"},
				MgmtIP: "192.0.2.21", Online: true, Capabilities: []model.Capability{model.CapWireless, model.CapWirelessMesh},
			},
		},
		LagGroups: []model.LagGroup{
			{DeviceID: "andaru:00:11:22:33:44:01", ID: "bond0", Name: "bond0", Mode: "lacp", MemberLinks: []string{"sfp0", "sfp1"}, LinkUp: true, SpeedMbps: 20000},
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", ID: "lag1", Name: "lag1", Mode: "lacp", MemberLinks: []string{"sfp9", "sfp10"}, LinkUp: true, SpeedMbps: 20000},
		},
		Links: []model.Link{
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", IfName: "lan2", SpeedMbps: 2500, LinkUp: true},
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", IfName: "lan4", SpeedMbps: 2500, LinkUp: true},
		},
		LLDP: []model.LldpNeighbor{
			// Both LAG members report LLDP to the firewall -> must collapse into ONE LAG edge.
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", LocalLink: "sfp9", ChassisID: "00:11:22:33:44:01", RemotePort: "sfp0"},
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", LocalLink: "sfp10", ChassisID: "00:11:22:33:44:01", RemotePort: "sfp1"},
			{DeviceID: "andaru:00:11:22:33:44:01", LocalLink: "sfp0", ChassisID: "02:51:0a:00:00:01", RemotePort: "sfp9"},
		},
		FDB: []model.FdbEntry{
			// LG510S sees AP-Office and AP-Mesh on lan2, workstation on lan4, and firewall on sfp9.
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", LinkName: "sfp9", MAC: "00:11:22:33:44:01", VLAN: 1},
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", LinkName: "lan2", MAC: "84:78:48:00:00:01", VLAN: 1},
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", LinkName: "lan2", MAC: "84:78:48:00:00:02", VLAN: 1},
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", LinkName: "lan2", MAC: "de:ad:be:ef:00:02", VLAN: 1}, // wireless phone behind AP
			{DeviceID: "rtlplayground:02:51:0a:00:00:01", LinkName: "lan4", MAC: "de:ad:be:ef:00:01", VLAN: 1}, // wired workstation
		},
		WirelessUplinks: []model.WirelessUplink{
			{DeviceID: "unifi:84:78:48:00:00:02", PeerMAC: "84:78:48:00:00:01", Band: "5GHz", SignalDbm: -61, TxRateMbps: 866},
		},
		DHCP: []model.DhcpLease{
			{DeviceID: "andaru:00:11:22:33:44:01", Family: model.IPv4, IP: "192.0.2.101", MAC: "de:ad:be:ef:00:01", Hostname: "workstation"},
		},
		Clients: []model.Client{
			{
				MAC: "de:ad:be:ef:00:02", IPs: []string{"192.0.2.102"}, Hostname: "pixel",
				Online: true, ConnectionType: "wireless", AttachedDeviceID: "unifi:84:78:48:00:00:02",
				SSID: "HomeWiFi", TxRateMbps: &txRate,
			},
		},
	}

	g := Solve(state, now)

	if len(g.RootIDs) != 1 || g.RootIDs[0] != "andaru:00:11:22:33:44:01" {
		t.Fatalf("RootIDs = %v, want [andaru:00:11:22:33:44:01]", g.RootIDs)
	}
	if len(g.Nodes) != 6 {
		t.Fatalf("Nodes = %d, want 6 (4 devices + 2 clients)", len(g.Nodes))
	}

	edgeMap := make(map[string]Edge)
	for _, e := range g.Edges {
		edgeMap[e.FromID+"->"+e.ToID] = e
	}

	// 1. Firewall -> LG510S collapsed into a single LAG edge oriented away from root.
	lagEdge, ok := edgeMap["andaru:00:11:22:33:44:01->rtlplayground:02:51:0a:00:00:01"]
	if !ok {
		t.Fatalf("missing firewall->LG510S edge; got %+v", g.Edges)
	}
	if lagEdge.Kind != EdgeLAG || lagEdge.FromLink != "bond0" || lagEdge.ToLink != "lag1" || lagEdge.SpeedMbps != 20000 {
		t.Fatalf("unexpected LAG edge: %+v", lagEdge)
	}

	// 2. LG510S -> AP-Office via FDB (no duplicate shortcut to AP-Mesh because AP-Mesh is already connected to AP-Office).
	apEdge, ok := edgeMap["rtlplayground:02:51:0a:00:00:01->unifi:84:78:48:00:00:01"]
	if !ok || apEdge.FromLink != "lan2" || apEdge.Evidence != EvidenceFDBInferred {
		t.Fatalf("unexpected LG510S->AP-Office edge: %+v (ok=%v)", apEdge, ok)
	}
	if _, bad := edgeMap["rtlplayground:02:51:0a:00:00:01->unifi:84:78:48:00:00:02"]; bad {
		t.Fatalf("unexpected transitive shortcut edge LG510S->AP-Mesh")
	}

	// 3. AP-Office -> AP-Mesh wireless mesh uplink.
	meshEdge, ok := edgeMap["unifi:84:78:48:00:00:01->unifi:84:78:48:00:00:02"]
	if !ok || meshEdge.Kind != EdgeWirelessUplink {
		t.Fatalf("unexpected mesh edge: %+v (ok=%v)", meshEdge, ok)
	}

	// 4. Wired workstation attached to leaf access port lan4 with fdb_unique evidence.
	wsEdge, ok := edgeMap["rtlplayground:02:51:0a:00:00:01->client:de:ad:be:ef:00:01"]
	if !ok || wsEdge.FromLink != "lan4" || wsEdge.Evidence != EvidenceFDBUnique {
		t.Fatalf("unexpected workstation edge: %+v (ok=%v)", wsEdge, ok)
	}

	// 5. Wireless phone attached to AP-Mesh on SSID HomeWiFi (not duplicated on lan2!).
	phEdge, ok := edgeMap["unifi:84:78:48:00:00:02->client:de:ad:be:ef:00:02"]
	if !ok || phEdge.Kind != EdgeWirelessClient || phEdge.FromLink != "HomeWiFi" {
		t.Fatalf("unexpected wireless client edge: %+v (ok=%v)", phEdge, ok)
	}

	if len(g.Edges) != 5 {
		t.Fatalf("expected exactly 5 edges, got %d: %+v", len(g.Edges), g.Edges)
	}

	// Determinism check: running Solve twice yields identical output.
	if g2 := Solve(state, now); !reflect.DeepEqual(g, g2) {
		t.Fatalf("non-deterministic graph:\n%+v\nvs\n%+v", g, g2)
	}
}

func TestSolveLoopAndNilState(t *testing.T) {
	now := time.Now().UTC()
	if g := Solve(nil, now); len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("expected empty graph for nil state, got %+v", g)
	}

	// Triangle of 3 switches with redundant LLDP loop must not panic or duplicate edges.
	state := &store.NetworkState{
		Devices: []model.Device{
			{ID: "sw1", Name: "sw1", Role: model.RoleSwitch, PrimaryMAC: "00:00:00:00:00:01"},
			{ID: "sw2", Name: "sw2", Role: model.RoleSwitch, PrimaryMAC: "00:00:00:00:00:02"},
			{ID: "sw3", Name: "sw3", Role: model.RoleSwitch, PrimaryMAC: "00:00:00:00:00:03"},
		},
		LLDP: []model.LldpNeighbor{
			{DeviceID: "sw1", LocalLink: "p1", ChassisID: "00:00:00:00:00:02", RemotePort: "p1"},
			{DeviceID: "sw2", LocalLink: "p1", ChassisID: "00:00:00:00:00:01", RemotePort: "p1"},
			{DeviceID: "sw2", LocalLink: "p2", ChassisID: "00:00:00:00:00:03", RemotePort: "p2"},
			{DeviceID: "sw3", LocalLink: "p3", ChassisID: "00:00:00:00:00:01", RemotePort: "p3"},
		},
	}
	g := Solve(state, now)
	if len(g.Edges) != 3 {
		t.Fatalf("expected 3 deduplicated triangle edges, got %d: %+v", len(g.Edges), g.Edges)
	}
}

func TestSolveLLDPByIPAndHostnameAndDualStackClients(t *testing.T) {
	now := time.Now().UTC()
	state := &store.NetworkState{
		Devices: []model.Device{
			{ID: "gw1", Name: "pfsense", Hostname: "pfsense.lan", Role: model.RoleGateway, MgmtIP: "192.0.2.1", PrimaryMAC: "aa:00:00:00:00:01", Online: true},
			{ID: "sw1", Name: "switch-a", Hostname: "switch-a", Role: model.RoleSwitch, MgmtIP: "192.0.2.2", PrimaryMAC: "aa:00:00:00:00:02", Online: true},
			{ID: "sw2", Name: "switch-b", Hostname: "switch-b", Role: model.RoleSwitch, MgmtIP: "192.0.2.3", PrimaryMAC: "aa:00:00:00:00:03", Online: true},
		},
		Links: []model.Link{
			{DeviceID: "sw1", IfName: "eth1", MAC: "aa:00:00:00:00:12", SpeedMbps: 1000},
			{DeviceID: "sw2", IfName: "eth2", SpeedMbps: 1000},
		},
		LLDP: []model.LldpNeighbor{
			// Resolve peer by RemoteMgmtIP when ChassisID is non-MAC.
			{DeviceID: "sw1", LocalLink: "eth1", ChassisID: "not-a-mac", RemoteMgmtIP: "192.0.2.1", RemotePort: "igc0"},
			// Resolve peer by RemoteSysName.
			{DeviceID: "sw2", LocalLink: "eth1", RemoteSysName: "switch-a", RemotePort: "eth2"},
		},
		Neighbors: []model.NeighborEntry{
			{DeviceID: "gw1", Family: model.IPv6, IP: "2001:db8::50", MAC: "bb:00:00:00:00:50", State: "reachable"},
		},
		FDB: []model.FdbEntry{
			// Wired IPv6 client seen on sw2 leaf port eth2 AND on sw1 transit uplink eth9 (which also sees sw2's MAC).
			{DeviceID: "sw1", LinkName: "eth9", MAC: "aa:00:00:00:00:03", VLAN: 1},
			{DeviceID: "sw1", LinkName: "eth9", MAC: "bb:00:00:00:00:50", VLAN: 1},
			{DeviceID: "sw2", LinkName: "eth2", MAC: "bb:00:00:00:00:50", VLAN: 1},
		},
		Clients: []model.Client{
			// Wired client reported directly with AttachedDeviceID but absent from FDB.
			{MAC: "bb:00:00:00:00:51", IPs: []string{"192.0.2.51"}, ConnectionType: "wired", AttachedDeviceID: "gw1", AttachedLink: "igc1", Online: true},
		},
	}

	g := Solve(state, now)
	edges := make(map[string]Edge)
	for _, e := range g.Edges {
		edges[e.FromID+"->"+e.ToID] = e
	}
	if _, ok := edges["gw1->sw1"]; !ok {
		t.Fatalf("expected gw1->sw1 resolved by RemoteMgmtIP, got %+v", g.Edges)
	}
	if _, ok := edges["sw1->sw2"]; !ok {
		t.Fatalf("expected sw1->sw2 resolved by RemoteSysName, got %+v", g.Edges)
	}
	if _, ok := edges["sw2->client:bb:00:00:00:00:50"]; !ok {
		t.Fatalf("expected IPv6 neighbor client attached to sw2/eth2, got %+v", g.Edges)
	}
	if _, ok := edges["gw1->client:bb:00:00:00:00:51"]; !ok {
		t.Fatalf("expected direct wired client attached to gw1/igc1, got %+v", g.Edges)
	}
}

func TestSolveParallelLLDPLinks(t *testing.T) {
	state := &store.NetworkState{
		Devices: []model.Device{
			{ID: "a", Name: "a", PrimaryMAC: "00:00:00:00:00:0a", Online: true},
			{ID: "b", Name: "b", PrimaryMAC: "00:00:00:00:00:0b", Online: true},
		},
		LLDP: []model.LldpNeighbor{
			{DeviceID: "a", LocalLink: "p1", ChassisID: "00:00:00:00:00:0b", RemotePort: "q1"},
			{DeviceID: "a", LocalLink: "p2", ChassisID: "00:00:00:00:00:0b", RemotePort: "q2"},
			// Reverse records: one matching, one with port IDs spelled differently.
			{DeviceID: "b", LocalLink: "q2", ChassisID: "00:00:00:00:00:0a", RemotePort: "p2"},
			{DeviceID: "b", LocalLink: "q1", ChassisID: "00:00:00:00:00:0a", RemotePort: "Port 1"},
		},
	}
	var ids []string
	for _, e := range Solve(state, time.Unix(0, 0)).Edges {
		ids = append(ids, e.ID)
	}
	if want := []string{"a:p1->b:q1", "a:p2->b:q2"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("edges = %v, want %v", ids, want)
	}

	// A single link whose reverse record spells both port IDs differently stays one edge.
	state.LLDP = []model.LldpNeighbor{
		{DeviceID: "a", LocalLink: "p1", ChassisID: "00:00:00:00:00:0b", RemotePort: "Gi0/1"},
		{DeviceID: "b", LocalLink: "q1", ChassisID: "00:00:00:00:00:0a", RemotePort: "Port 1"},
	}
	if g := Solve(state, time.Unix(0, 0)); len(g.Edges) != 1 || g.Edges[0].ID != "a:p1->b:q1" {
		t.Fatalf("edges = %+v, want one a:p1->b:q1", g.Edges)
	}

	// Parallel links whose remote port IDs do not match local names must not be mispaired by order;
	// both directions stay as separate unconfirmed edges rather than guessing a cross-pairing.
	state.LLDP = []model.LldpNeighbor{
		{DeviceID: "a", LocalLink: "p1", ChassisID: "00:00:00:00:00:0b", RemotePort: "Gi0/1"},
		{DeviceID: "a", LocalLink: "p2", ChassisID: "00:00:00:00:00:0b", RemotePort: "Gi0/2"},
		{DeviceID: "b", LocalLink: "q2", ChassisID: "00:00:00:00:00:0a", RemotePort: "Port 2"},
		{DeviceID: "b", LocalLink: "q1", ChassisID: "00:00:00:00:00:0a", RemotePort: "Port 1"},
	}
	unpaired := Solve(state, time.Unix(0, 0)).Edges
	if len(unpaired) != 4 {
		t.Fatalf("ambiguous parallel LLDP edges = %d (%+v), want 4 unpaired edges", len(unpaired), unpaired)
	}
	for _, e := range unpaired {
		if (e.FromLink == "p1" && e.ToLink == "q2") || (e.FromLink == "p2" && e.ToLink == "q1") {
			t.Fatalf("parallel LLDP links mispaired by record order: %+v", e)
		}
	}

	// Parallel links from the same device reporting the same remote port ID (e.g. peer chassis MAC)
	// must stay two distinct edges rather than collapsing p2 into p1.
	state.LLDP = []model.LldpNeighbor{
		{DeviceID: "a", LocalLink: "p1", ChassisID: "00:00:00:00:00:0b", RemotePort: "00:00:00:00:00:0b"},
		{DeviceID: "a", LocalLink: "p2", ChassisID: "00:00:00:00:00:0b", RemotePort: "00:00:00:00:00:0b"},
	}
	sameRemote := Solve(state, time.Unix(0, 0)).Edges
	if len(sameRemote) != 2 {
		t.Fatalf("parallel links with identical remote port ID = %d (%+v), want 2", len(sameRemote), sameRemote)
	}
}

func TestSolveIgnoresDanglingAndOfflineEvidence(t *testing.T) {
	state := &store.NetworkState{
		Devices: []model.Device{
			{ID: "sw1", Name: "sw1", PrimaryMAC: "00:00:00:00:00:01", Online: false},
			{ID: "ap1", Name: "ap1", PrimaryMAC: "00:00:00:00:00:02", Online: true},
		},
		// Peer and attachment devices missing from the state must not produce dangling edges.
		WirelessUplinks: []model.WirelessUplink{{DeviceID: "ap1", PeerDeviceID: "gone"}},
		Clients: []model.Client{
			{MAC: "bb:00:00:00:00:01", ConnectionType: "wireless", AttachedDeviceID: "gone", SSID: "x"},
			{MAC: "bb:00:00:00:00:02", ConnectionType: "wired", AttachedDeviceID: "gone"},
		},
		// Retained records of the offline switch place clients but do not make them online.
		FDB: []model.FdbEntry{
			{DeviceID: "sw1", LinkName: "p1", MAC: "bb:00:00:00:00:01"},
			{DeviceID: "sw1", LinkName: "p3", MAC: "bb:00:00:00:00:03"},
		},
		Neighbors: []model.NeighborEntry{{DeviceID: "sw1", MAC: "bb:00:00:00:00:03", State: "reachable"}},
	}
	g := Solve(state, time.Unix(0, 0))
	nodes := map[string]Node{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	var ids []string
	for _, e := range g.Edges {
		if _, ok := nodes[e.FromID]; !ok {
			t.Errorf("edge %s starts at a missing node", e.ID)
		}
		if _, ok := nodes[e.ToID]; !ok {
			t.Errorf("edge %s ends at a missing node", e.ID)
		}
		ids = append(ids, e.ID)
	}
	if want := []string{"sw1:p1->client:bb:00:00:00:00:01", "sw1:p3->client:bb:00:00:00:00:03"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("edges = %v, want %v", ids, want)
	}
	for _, id := range []string{"client:bb:00:00:00:00:01", "client:bb:00:00:00:00:03"} {
		if nodes[id].Online {
			t.Errorf("%s online from an offline device's records", id)
		}
	}
}

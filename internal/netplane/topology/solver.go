// SPDX-License-Identifier: AGPL-3.0-or-later
package topology

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/model"
	"github.com/andaru-project/andaru/internal/netplane/store"
)

// NodeKind distinguishes infrastructure devices from end-user clients.
type NodeKind string

const (
	NodeDevice NodeKind = "device"
	NodeClient NodeKind = "client"
)

// EdgeKind classifies a topology link.
type EdgeKind string

const (
	EdgeWired          EdgeKind = "wired"
	EdgeLAG            EdgeKind = "lag"
	EdgeWirelessUplink EdgeKind = "wireless_uplink"
	EdgeWirelessClient EdgeKind = "wireless_client"
)

// EdgeEvidence explains how an edge was discovered.
type EdgeEvidence string

const (
	EvidenceLLDP           EdgeEvidence = "lldp"
	EvidenceFDBUnique      EdgeEvidence = "fdb_unique"
	EvidenceFDBInferred    EdgeEvidence = "fdb_inferred"
	EvidenceWirelessUplink EdgeEvidence = "wireless_uplink"
	EvidenceWirelessClient EdgeEvidence = "wireless_client"
)

// Node represents one vertex in the topology graph.
type Node struct {
	ID     string   `json:"id"`
	Kind   NodeKind `json:"kind"`
	Role   string   `json:"role,omitempty"`
	Driver string   `json:"driver,omitempty"`
	Label  string   `json:"label"`
	MAC    string   `json:"mac,omitempty"`
	IP     string   `json:"ip,omitempty"`
	Online bool     `json:"online"`
}

// Edge represents a resolved connection between two nodes.
type Edge struct {
	ID          string       `json:"id"`
	FromID      string       `json:"from_id"`
	FromLink    string       `json:"from_link,omitempty"`
	ToID        string       `json:"to_id"`
	ToLink      string       `json:"to_link,omitempty"`
	Kind        EdgeKind     `json:"kind"`
	Evidence    EdgeEvidence `json:"evidence"`
	Confidence  float64      `json:"confidence"`
	SpeedMbps   int          `json:"speed_mbps,omitempty"`
	MemberLinks []string     `json:"member_links"`
}

// Graph is the complete deterministic cross-vendor L2/L3/Wi-Fi topology snapshot.
type Graph struct {
	GeneratedAt time.Time `json:"generated_at"`
	RootIDs     []string  `json:"root_ids"`
	Nodes       []Node    `json:"nodes"`
	Edges       []Edge    `json:"edges"`
}

type pairKey struct{ a, b string }

func makePair(x, y string) pairKey {
	if x < y {
		return pairKey{x, y}
	}
	return pairKey{y, x}
}

type devPortKey struct{ devID, link string }

// solver holds the indexes the topology phases share.
type solver struct {
	state *store.NetworkState
	g     Graph

	devByID   map[string]model.Device
	devByMAC  map[string]string
	devByIP   map[string]string
	devByName map[string]string

	linkSpeed map[devPortKey]int
	linkToLAG map[devPortKey]model.LagGroup
	lagByName map[devPortKey]model.LagGroup

	// Several edges may join one device pair (parallel links); each is one physical link or LAG.
	infraEdges map[pairKey][]*Edge
	infraPorts map[devPortKey]bool
	// confirmed records which endpoint names came from the device's own LLDP record (its local
	// port), as opposed to the peer's possibly differently spelled remote port ID.
	confirmed map[*Edge]map[string]bool
	// portMACs holds the remote MACs each (normalized) port learned in the FDB.
	portMACs map[devPortKey]map[string]bool
}

// Solve deterministically constructs a cross-vendor topology graph from merged network state.
func Solve(state *store.NetworkState, now time.Time) Graph {
	g := Graph{
		GeneratedAt: now.UTC(),
		RootIDs:     []string{},
		Nodes:       []Node{},
		Edges:       []Edge{},
	}
	if state == nil {
		return g
	}
	s := &solver{
		state:      state,
		g:          g,
		devByID:    make(map[string]model.Device, len(state.Devices)),
		devByMAC:   make(map[string]string),
		devByIP:    make(map[string]string),
		devByName:  make(map[string]string),
		linkSpeed:  make(map[devPortKey]int),
		linkToLAG:  make(map[devPortKey]model.LagGroup),
		lagByName:  make(map[devPortKey]model.LagGroup),
		infraEdges: make(map[pairKey][]*Edge),
		infraPorts: make(map[devPortKey]bool),
		confirmed:  make(map[*Edge]map[string]bool),
		portMACs:   make(map[devPortKey]map[string]bool),
	}
	s.indexDevices()
	s.indexLinks()
	s.lldpEdges()
	s.wirelessUplinkEdges()
	s.fdbEdges()
	s.orientEdges()
	s.clients()
	sort.Slice(s.g.Nodes, func(i, j int) bool { return s.g.Nodes[i].ID < s.g.Nodes[j].ID })
	sort.Slice(s.g.Edges, func(i, j int) bool { return s.g.Edges[i].ID < s.g.Edges[j].ID })
	return s.g
}

// indexDevices indexes devices by ID, MAC, management IP and name, and adds their nodes.
func (s *solver) indexDevices() {
	for _, d := range s.state.Devices {
		s.devByID[d.ID] = d
		if d.PrimaryMAC != "" {
			s.devByMAC[d.PrimaryMAC] = d.ID
		}
		for _, m := range d.MACs {
			s.devByMAC[m] = d.ID
		}
		if d.MgmtIP != "" {
			s.devByIP[d.MgmtIP] = d.ID
		}
		if d.Hostname != "" {
			s.devByName[strings.ToLower(d.Hostname)] = d.ID
		}
		if d.Name != "" {
			s.devByName[strings.ToLower(d.Name)] = d.ID
		}
		label := cmp.Or(d.Name, d.Hostname, d.ID)
		s.g.Nodes = append(s.g.Nodes, Node{
			ID:     d.ID,
			Kind:   NodeDevice,
			Role:   string(d.Role),
			Driver: d.Driver,
			Label:  label,
			MAC:    d.PrimaryMAC,
			IP:     d.MgmtIP,
			Online: d.Online,
		})
	}
}

// indexLinks indexes physical links and LAG memberships.
func (s *solver) indexLinks() {
	for _, lag := range s.state.LagGroups {
		s.lagByName[devPortKey{lag.DeviceID, lag.Name}] = lag
		for _, m := range lag.MemberLinks {
			s.linkToLAG[devPortKey{lag.DeviceID, m}] = lag
		}
	}
	for _, l := range s.state.Links {
		if l.MAC != "" {
			if _, exists := s.devByMAC[l.MAC]; !exists {
				s.devByMAC[l.MAC] = l.DeviceID
			}
		}
		s.linkSpeed[devPortKey{l.DeviceID, l.IfName}] = l.SpeedMbps
	}
}

// normalizePort maps a port to its LAG when it is a member or the LAG itself.
func (s *solver) normalizePort(devID, portName string) (normName string, isLag bool, members []string, speed int) {
	k := devPortKey{devID, portName}
	lag, ok := s.linkToLAG[k]
	if !ok {
		lag, ok = s.lagByName[k]
	}
	if ok {
		return lag.Name, true, slices.Sorted(slices.Values(lag.MemberLinks)), lag.SpeedMbps
	}
	return portName, false, []string{}, s.linkSpeed[k]
}

// resolvePeer finds the device an LLDP neighbor names, by chassis MAC, management IP or name.
func (s *solver) resolvePeer(chassisID, mgmtIP, sysName string) string {
	if chassisID != "" {
		if mac, err := model.NormalizeMAC(chassisID); err == nil {
			if id, ok := s.devByMAC[mac]; ok {
				return id
			}
		}
		if id, ok := s.devByMAC[strings.ToLower(chassisID)]; ok {
			return id
		}
	}
	if mgmtIP != "" {
		if id, ok := s.devByIP[mgmtIP]; ok {
			return id
		}
	}
	if sysName != "" {
		if id, ok := s.devByName[strings.ToLower(sysName)]; ok {
			return id
		}
	}
	return ""
}

// adjacency returns the device adjacency of the infrastructure edges found so far.
func (s *solver) adjacency() map[string][]string {
	adj := make(map[string][]string)
	for pk := range s.infraEdges {
		addPair(adj, pk)
	}
	return adj
}

func addPair(adj map[string][]string, pk pairKey) {
	adj[pk.a] = append(adj[pk.a], pk.b)
	adj[pk.b] = append(adj[pk.b], pk.a)
}

// side returns the link name field of e that belongs to devID.
func side(e *Edge, devID string) *string {
	if e.FromID == devID {
		return &e.FromLink
	}
	return &e.ToLink
}

// lldpEdges adds direct LLDP edges (confidence 1.0).
func (s *solver) lldpEdges() {
	type pendingLLDP struct {
		pk        pairKey
		devID     string
		peerID    string
		localName string
		remote    string
		kind      EdgeKind
		spd       int
		members   []string
	}
	var pending []pendingLLDP

	mergeEdge := func(existing *Edge, devID, peerID, localName, remoteName string, kind EdgeKind, spd int, members []string) {
		if !s.confirmed[existing][devID] {
			*side(existing, devID) = localName
		}
		if *side(existing, peerID) == "" {
			*side(existing, peerID) = remoteName
		}
		s.confirmed[existing][devID] = true
		if kind == EdgeLAG {
			existing.Kind = EdgeLAG
		}
		if existing.SpeedMbps == 0 && spd > 0 {
			existing.SpeedMbps = spd
		}
		if len(existing.MemberLinks) == 0 && len(members) > 0 {
			existing.MemberLinks = members
		}
	}
	addEdge := func(pk pairKey, devID, peerID, localName, remoteName string, kind EdgeKind, spd int, members []string) {
		e := &Edge{
			FromID:      devID,
			FromLink:    localName,
			ToID:        peerID,
			ToLink:      remoteName,
			Kind:        kind,
			Evidence:    EvidenceLLDP,
			Confidence:  1.0,
			SpeedMbps:   spd,
			MemberLinks: members,
		}
		s.infraEdges[pk] = append(s.infraEdges[pk], e)
		s.confirmed[e] = map[string]bool{devID: true}
	}
	findExact := func(pk pairKey, devID, peerID, localName, remoteName string) *Edge {
		for _, e := range s.infraEdges[pk] {
			if *side(e, devID) == localName || (remoteName != "" && *side(e, peerID) == remoteName) {
				return e
			}
		}
		return nil
	}

	// Pass 1: resolve exact port matches and record initial one-sided edges; defer unmatched
	// reverse records so parallel links are never mispaired by record order.
	for _, n := range s.state.LLDP {
		if _, ok := s.devByID[n.DeviceID]; !ok {
			continue
		}
		peerID := s.resolvePeer(n.ChassisID, n.RemoteMgmtIP, n.RemoteSysName)
		if peerID == "" || peerID == n.DeviceID {
			continue
		}
		localName, localLag, members, spd := s.normalizePort(n.DeviceID, n.LocalLink)
		remoteName, remoteLag, rMembers, rSpd := s.normalizePort(peerID, n.RemotePort)

		pk := makePair(n.DeviceID, peerID)
		kind := EdgeWired
		if localLag || remoteLag {
			kind = EdgeLAG
		}
		if spd == 0 {
			spd = rSpd
		}
		if len(members) == 0 && len(rMembers) > 0 {
			members = rMembers
		}

		s.infraPorts[devPortKey{n.DeviceID, localName}] = true
		if remoteName != "" {
			s.infraPorts[devPortKey{peerID, remoteName}] = true
		}

		if existing := findExact(pk, n.DeviceID, peerID, localName, remoteName); existing != nil {
			mergeEdge(existing, n.DeviceID, peerID, localName, remoteName, kind, spd, members)
			continue
		}
		hasUnconfirmed := false
		for _, e := range s.infraEdges[pk] {
			if !s.confirmed[e][n.DeviceID] {
				hasUnconfirmed = true
				break
			}
		}
		if hasUnconfirmed {
			pending = append(pending, pendingLLDP{pk, n.DeviceID, peerID, localName, remoteName, kind, spd, members})
		} else {
			addEdge(pk, n.DeviceID, peerID, localName, remoteName, kind, spd, members)
		}
	}

	// Pass 2: retry exact matches, then apply the single-unmatched-pair fallback only when
	// unambiguous (one unmatched record and one unconfirmed edge for that pair/device).
	type pairDev struct {
		pk    pairKey
		devID string
	}
	remaining := make(map[pairDev][]pendingLLDP)
	var order []pairDev
	for _, p := range pending {
		if existing := findExact(p.pk, p.devID, p.peerID, p.localName, p.remote); existing != nil {
			mergeEdge(existing, p.devID, p.peerID, p.localName, p.remote, p.kind, p.spd, p.members)
			continue
		}
		k := pairDev{p.pk, p.devID}
		if _, seen := remaining[k]; !seen {
			order = append(order, k)
		}
		remaining[k] = append(remaining[k], p)
	}
	for _, k := range order {
		recs := remaining[k]
		var unconfirmed []*Edge
		for _, e := range s.infraEdges[k.pk] {
			if !s.confirmed[e][k.devID] {
				unconfirmed = append(unconfirmed, e)
			}
		}
		if len(recs) == 1 && len(unconfirmed) == 1 {
			p := recs[0]
			mergeEdge(unconfirmed[0], p.devID, p.peerID, p.localName, p.remote, p.kind, p.spd, p.members)
			continue
		}
		for _, p := range recs {
			addEdge(p.pk, p.devID, p.peerID, p.localName, p.remote, p.kind, p.spd, p.members)
		}
	}
}

// wirelessUplinkEdges adds wireless mesh uplinks (confidence 0.95).
func (s *solver) wirelessUplinkEdges() {
	for _, u := range s.state.WirelessUplinks {
		if _, ok := s.devByID[u.DeviceID]; !ok {
			continue
		}
		peerID := u.PeerDeviceID
		if peerID == "" && u.PeerMAC != "" {
			peerID = s.devByMAC[u.PeerMAC]
		}
		if _, ok := s.devByID[peerID]; !ok || peerID == u.DeviceID {
			continue
		}
		pk := makePair(u.DeviceID, peerID)
		if len(s.infraEdges[pk]) > 0 {
			continue
		}
		spd := u.TxRateMbps
		if u.RxRateMbps > spd {
			spd = u.RxRateMbps
		}
		s.infraEdges[pk] = []*Edge{{
			FromID:      peerID,
			ToID:        u.DeviceID,
			Kind:        EdgeWirelessUplink,
			Evidence:    EvidenceWirelessUplink,
			Confidence:  0.95,
			SpeedMbps:   spd,
			MemberLinks: []string{},
		}}
	}
}

// fdbEdges adds FDB infrastructure edges (confidence 0.85), avoiding transitive shortcuts.
func (s *solver) fdbEdges() {
	for _, f := range s.state.FDB {
		if f.IsLocal || f.MAC == "" {
			continue
		}
		if _, ok := s.devByID[f.DeviceID]; !ok {
			continue
		}
		normName, _, _, _ := s.normalizePort(f.DeviceID, f.LinkName)
		key := devPortKey{f.DeviceID, normName}
		if s.portMACs[key] == nil {
			s.portMACs[key] = make(map[string]bool)
		}
		s.portMACs[key][f.MAC] = true
	}

	// Adjacency of known edges, so FDB edges never shortcut multi-hop LLDP chains.
	infraAdj := s.adjacency()
	connected := func(src, dst string) bool {
		if src == dst {
			return true
		}
		visited := map[string]bool{src: true}
		q := []string{src}
		for len(q) > 0 {
			cur := q[0]
			q = q[1:]
			for _, nxt := range infraAdj[cur] {
				if nxt == dst {
					return true
				}
				if !visited[nxt] {
					visited[nxt] = true
					q = append(q, nxt)
				}
			}
		}
		return false
	}

	type fdbCandidate struct {
		srcID   string
		srcPort string
		dstID   string
		card    int
	}
	var cands []fdbCandidate
	for pk, macSet := range s.portMACs {
		for mac := range macSet {
			dstID, isDev := s.devByMAC[mac]
			if !isDev || dstID == pk.devID {
				continue
			}
			cands = append(cands, fdbCandidate{
				srcID:   pk.devID,
				srcPort: pk.link,
				dstID:   dstID,
				card:    len(macSet),
			})
		}
	}
	// Smallest port MAC cardinality first (closest hop first), deterministic tie-break.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].card != cands[j].card {
			return cands[i].card < cands[j].card
		}
		if cands[i].srcID != cands[j].srcID {
			return cands[i].srcID < cands[j].srcID
		}
		if cands[i].dstID != cands[j].dstID {
			return cands[i].dstID < cands[j].dstID
		}
		return cands[i].srcPort < cands[j].srcPort
	})

	for _, c := range cands {
		normName, isLag, members, spd := s.normalizePort(c.srcID, c.srcPort)
		// Any port seeing another infrastructure device's MAC is a transit/uplink port,
		// never a leaf client access port — mark it even if an LLDP/earlier path already exists.
		alreadyInfra := s.infraPorts[devPortKey{c.srcID, normName}]
		s.infraPorts[devPortKey{c.srcID, normName}] = true
		if alreadyInfra || connected(c.srcID, c.dstID) {
			continue
		}
		kind := EdgeWired
		if isLag {
			kind = EdgeLAG
		}
		pk := makePair(c.srcID, c.dstID)
		addPair(infraAdj, pk)
		s.infraEdges[pk] = []*Edge{{
			FromID:      c.srcID,
			FromLink:    normName,
			ToID:        c.dstID,
			Kind:        kind,
			Evidence:    EvidenceFDBInferred,
			Confidence:  0.85,
			SpeedMbps:   spd,
			MemberLinks: members,
		}}
	}
}

// orientEdges identifies root devices and orients edges away from them via BFS.
func (s *solver) orientEdges() {
	for _, d := range s.state.Devices {
		if d.Role == model.RoleGateway || d.Role == model.RoleFirewall || d.HasCapability(model.CapGateway) {
			s.g.RootIDs = append(s.g.RootIDs, d.ID)
		}
	}
	sort.Strings(s.g.RootIDs)

	depth := make(map[string]int, len(s.state.Devices))
	if len(s.g.RootIDs) > 0 {
		adj := s.adjacency()
		for k := range adj {
			sort.Strings(adj[k])
		}
		q := make([]string, 0, len(s.g.RootIDs))
		for _, r := range s.g.RootIDs {
			depth[r] = 0
			q = append(q, r)
		}
		for len(q) > 0 {
			cur := q[0]
			q = q[1:]
			for _, nxt := range adj[cur] {
				if _, seen := depth[nxt]; !seen {
					depth[nxt] = depth[cur] + 1
					q = append(q, nxt)
				}
			}
		}
	}

	for _, e := range slices.Concat(slices.Collect(maps.Values(s.infraEdges))...) {
		dFrom, okFrom := depth[e.FromID]
		dTo, okTo := depth[e.ToID]
		swap := false
		if okFrom && okTo {
			if dTo < dFrom || (dTo == dFrom && e.ToID < e.FromID) {
				swap = true
			}
		} else if !okFrom && okTo {
			swap = true
		} else if !okFrom && !okTo && e.ToID < e.FromID {
			swap = true
		}
		if swap {
			e.FromID, e.ToID = e.ToID, e.FromID
			e.FromLink, e.ToLink = e.ToLink, e.FromLink
		}
		e.ID = fmt.Sprintf("%s:%s->%s:%s", e.FromID, e.FromLink, e.ToID, e.ToLink)
		if e.MemberLinks == nil {
			e.MemberLinks = []string{}
		}
		s.g.Edges = append(s.g.Edges, *e)
	}
}

// clients correlates client metadata (IPs, hostnames) from Clients, DHCP and Neighbors, and
// attaches each client to its wireless AP or wired access port.
func (s *solver) clients() {
	type clientInfo struct {
		mac      string
		ip       string
		hostname string
		online   bool
		wireless bool
		devID    string
		link     string
		ssid     string
		speed    int
	}
	clientsByMAC := make(map[string]*clientInfo)
	ensureClient := func(mac string) *clientInfo {
		if c, ok := clientsByMAC[mac]; ok {
			return c
		}
		c := &clientInfo{mac: mac}
		clientsByMAC[mac] = c
		return c
	}

	for _, c := range s.state.Clients {
		if _, isDev := s.devByMAC[c.MAC]; isDev {
			continue
		}
		ci := ensureClient(c.MAC)
		if c.Hostname != "" {
			ci.hostname = c.Hostname
		}
		if len(c.IPs) > 0 && ci.ip == "" {
			ci.ip = c.IPs[0]
		}
		ci.online = ci.online || c.Online
		// An attachment to a device missing from the graph would dangle; FDB may still place it.
		_, attached := s.devByID[c.AttachedDeviceID]
		if c.ConnectionType == "wireless" && attached {
			ci.wireless = true
			ci.devID = c.AttachedDeviceID
			ci.ssid = c.SSID
			if c.TxRateMbps != nil {
				ci.speed = *c.TxRateMbps
			}
		} else if attached && ci.devID == "" {
			ci.devID = c.AttachedDeviceID
			ci.link = c.AttachedLink
		}
	}
	for _, l := range s.state.DHCP {
		if l.MAC == "" {
			continue
		}
		if _, isDev := s.devByMAC[l.MAC]; isDev {
			continue
		}
		ci := ensureClient(l.MAC)
		if ci.hostname == "" && l.Hostname != "" {
			ci.hostname = l.Hostname
		}
		if ci.ip == "" && l.IP != "" {
			ci.ip = l.IP
		}
	}
	for _, n := range s.state.Neighbors {
		if n.MAC == "" {
			continue
		}
		if _, isDev := s.devByMAC[n.MAC]; isDev {
			continue
		}
		ci := ensureClient(n.MAC)
		if ci.ip == "" && n.IP != "" {
			ci.ip = n.IP
		}
		// A retained record of an offline device is no proof the client is online.
		if (n.State == "reachable" || n.State == "permanent") && s.devByID[n.DeviceID].Online {
			ci.online = true
		}
	}

	// Build leaf access-port candidates for wired clients from FDB.
	type leafMatch struct {
		devID string
		port  string
		card  int
	}
	clientLeaf := make(map[string][]leafMatch)
	for pk, macSet := range s.portMACs {
		if s.infraPorts[pk] {
			continue
		}
		for mac := range macSet {
			if _, isDev := s.devByMAC[mac]; isDev {
				continue
			}
			if s.devByID[pk.devID].Online {
				ensureClient(mac).online = true
			} else {
				ensureClient(mac)
			}
			clientLeaf[mac] = append(clientLeaf[mac], leafMatch{
				devID: pk.devID,
				port:  pk.link,
				card:  len(macSet),
			})
		}
	}

	for mac, ci := range clientsByMAC {
		nodeID := "client:" + mac
		label := cmp.Or(ci.hostname, ci.ip, mac)
		s.g.Nodes = append(s.g.Nodes, Node{
			ID:     nodeID,
			Kind:   NodeClient,
			Label:  label,
			MAC:    mac,
			IP:     ci.ip,
			Online: ci.online,
		})

		if ci.wireless && ci.devID != "" {
			s.g.Edges = append(s.g.Edges, Edge{
				ID:          fmt.Sprintf("%s:%s->%s", ci.devID, ci.ssid, nodeID),
				FromID:      ci.devID,
				FromLink:    ci.ssid,
				ToID:        nodeID,
				Kind:        EdgeWirelessClient,
				Evidence:    EvidenceWirelessClient,
				Confidence:  0.95,
				SpeedMbps:   ci.speed,
				MemberLinks: []string{},
			})
			continue
		}

		if leaves := clientLeaf[mac]; len(leaves) > 0 {
			sort.Slice(leaves, func(i, j int) bool {
				if leaves[i].card != leaves[j].card {
					return leaves[i].card < leaves[j].card
				}
				if leaves[i].devID != leaves[j].devID {
					return leaves[i].devID < leaves[j].devID
				}
				return leaves[i].port < leaves[j].port
			})
			best := leaves[0]
			ev := EvidenceFDBInferred
			conf := 0.80
			if len(leaves) == 1 && best.card == 1 {
				ev = EvidenceFDBUnique
				conf = 0.90
			}
			s.g.Edges = append(s.g.Edges, Edge{
				ID:          fmt.Sprintf("%s:%s->%s", best.devID, best.port, nodeID),
				FromID:      best.devID,
				FromLink:    best.port,
				ToID:        nodeID,
				Kind:        EdgeWired,
				Evidence:    ev,
				Confidence:  conf,
				SpeedMbps:   s.linkSpeed[devPortKey{best.devID, best.port}],
				MemberLinks: []string{},
			})
		} else if ci.devID != "" {
			s.g.Edges = append(s.g.Edges, Edge{
				ID:          fmt.Sprintf("%s:%s->%s", ci.devID, ci.link, nodeID),
				FromID:      ci.devID,
				FromLink:    ci.link,
				ToID:        nodeID,
				Kind:        EdgeWired,
				Evidence:    EvidenceFDBInferred,
				Confidence:  0.75,
				MemberLinks: []string{},
			})
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later
package events

import (
	"fmt"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// DiffSnapshots compares prev and curr snapshots for the same target and returns
// all significant state transition events. When prev is nil (first poll), no spurious
// transition flood is emitted except active critical SFP alerts.
func DiffSnapshots(prev, curr *driver.Snapshot) []Event {
	if curr == nil {
		return nil
	}
	ts := curr.CollectedAt.UTC()
	var out []Event
	// emit appends an event and returns it; the pointer is valid only until the next emit.
	emit := func(typ Type, sev Severity, devID, link, summary string) *Event {
		out = append(out, Event{Type: typ, Severity: sev, Timestamp: ts, TargetID: curr.TargetID, DeviceID: devID, LinkName: link, Summary: summary})
		return &out[len(out)-1]
	}
	sfpAlert := func(l model.Link, alert model.AlertState) {
		if sev, ok := sfpSeverity(alert); ok {
			emit(SfpAlert, sev, l.DeviceID, l.IfName, fmt.Sprintf("SFP optical alert (%s) on %s/%s", alert, l.DeviceID, l.IfName))
		}
	}

	if prev == nil {
		for _, l := range curr.Links {
			if l.SFP != nil && l.SFP.Present {
				sfpAlert(l, l.SFP.AlertState)
			}
		}
		return out
	}

	// 1. Device online/offline transitions.
	prevDevs := make(map[string]model.Device, len(prev.Devices))
	for _, d := range prev.Devices {
		prevDevs[d.ID] = d
	}
	for _, d := range curr.Devices {
		pd, ok := prevDevs[d.ID]
		switch {
		case d.Online && (!ok || !pd.Online):
			emit(DeviceOnline, SeverityInfo, d.ID, "", fmt.Sprintf("Device %s came online", d.Name))
		case !d.Online && ok && pd.Online:
			emit(DeviceOffline, SeverityCrit, d.ID, "", fmt.Sprintf("Device %s went offline", d.Name))
		}
	}

	currDevs := make(map[string]bool, len(curr.Devices))
	for _, d := range curr.Devices {
		currDevs[d.ID] = true
	}
	for _, pd := range prev.Devices {
		if pd.Online && !currDevs[pd.ID] {
			emit(DeviceOffline, SeverityCrit, pd.ID, "", fmt.Sprintf("Device %s went offline", pd.Name))
		}
	}

	// 2. Link up/down, STP state, and SFP optical alert transitions.
	type linkKey struct{ devID, ifName string }
	prevLinks := make(map[linkKey]model.Link, len(prev.Links))
	for _, l := range prev.Links {
		prevLinks[linkKey{l.DeviceID, l.IfName}] = l
	}
	for _, l := range curr.Links {
		pl, ok := prevLinks[linkKey{l.DeviceID, l.IfName}]
		if !ok {
			// New link: no link/STP baseline, but an active SFP alert still fires.
			pl = l
			pl.SFP = nil
		}
		if pl.LinkUp != l.LinkUp {
			if l.LinkUp {
				emit(LinkUp, SeverityInfo, l.DeviceID, l.IfName, fmt.Sprintf("Link %s/%s is up (%d Mbps)", l.DeviceID, l.IfName, l.SpeedMbps))
			} else {
				emit(LinkDown, SeverityWarn, l.DeviceID, l.IfName, fmt.Sprintf("Link %s/%s went down", l.DeviceID, l.IfName))
			}
		}
		if pl.StpState != l.StpState && l.StpState != "" {
			sev := SeverityInfo
			if l.StpState == model.StpBlocking || l.StpState == model.StpDiscarding {
				sev = SeverityWarn
			}
			emit(LinkStpChanged, sev, l.DeviceID, l.IfName, fmt.Sprintf("STP state changed on %s/%s: %s -> %s", l.DeviceID, l.IfName, pl.StpState, l.StpState)).Details = map[string]any{"old": pl.StpState, "new": l.StpState}
		}
		prevAlert := model.AlertOK
		if pl.SFP != nil {
			prevAlert = pl.SFP.AlertState
		}
		currAlert := model.AlertOK
		if l.SFP != nil {
			currAlert = l.SFP.AlertState
		}
		if prevAlert != currAlert {
			sfpAlert(l, currAlert)
		}
	}

	// 3. VPN tunnel transitions.
	type idKey struct{ devID, id string }
	prevVPNs := make(map[idKey]model.VpnTunnel, len(prev.VPNs))
	for _, v := range prev.VPNs {
		prevVPNs[idKey{v.DeviceID, v.ID}] = v
	}
	for _, v := range curr.VPNs {
		pv, ok := prevVPNs[idKey{v.DeviceID, v.ID}]
		if !ok || pv.Up == v.Up {
			continue
		}
		if v.Up {
			emit(VpnUp, SeverityInfo, v.DeviceID, "", fmt.Sprintf("VPN tunnel %s came up", v.Name))
		} else {
			emit(VpnDown, SeverityWarn, v.DeviceID, "", fmt.Sprintf("VPN tunnel %s went down", v.Name))
		}
	}

	// 4. Gateway transitions.
	prevGWs := make(map[idKey]model.GatewayStatus, len(prev.Gateways))
	for _, g := range prev.Gateways {
		prevGWs[idKey{g.DeviceID, g.Name}] = g
	}
	for _, g := range curr.Gateways {
		pg, ok := prevGWs[idKey{g.DeviceID, g.Name}]
		if !ok || pg.Online == g.Online {
			continue
		}
		if g.Online {
			emit(GatewayUp, SeverityInfo, g.DeviceID, "", fmt.Sprintf("Gateway %s is back online", g.Name))
		} else {
			emit(GatewayDown, SeverityCrit, g.DeviceID, "", fmt.Sprintf("Gateway %s went offline", g.Name))
		}
	}

	// 5. Client join/leave, keyed by MAC; a client missing from curr has left.
	prevOnline := make(map[string]bool, len(prev.Clients))
	for _, c := range prev.Clients {
		prevOnline[c.MAC] = prevOnline[c.MAC] || c.Online
	}
	currOnline := make(map[string]model.Client, len(curr.Clients))
	for _, c := range curr.Clients {
		if c.Online {
			currOnline[c.MAC] = c
		}
	}
	for _, c := range curr.Clients {
		if c.Online && !prevOnline[c.MAC] {
			prevOnline[c.MAC] = true // emit once per MAC
			emit(ClientJoined, SeverityInfo, c.AttachedDeviceID, "", fmt.Sprintf("Client %s joined", clientLabel(c)))
		}
	}
	left := map[string]bool{}
	for _, c := range prev.Clients {
		if _, ok := currOnline[c.MAC]; c.Online && !ok && !left[c.MAC] {
			left[c.MAC] = true
			emit(ClientLeft, SeverityInfo, c.AttachedDeviceID, "", fmt.Sprintf("Client %s left", clientLabel(c)))
		}
	}

	return out
}

// sfpSeverity maps an active SFP optical alert to its event severity; ok is false for none.
func sfpSeverity(a model.AlertState) (sev Severity, ok bool) {
	switch a {
	case model.AlertWarn:
		return SeverityWarn, true
	case model.AlertCrit:
		return SeverityCrit, true
	}
	return "", false
}

func clientLabel(c model.Client) string {
	if c.Hostname != "" {
		return c.Hostname + " (" + c.MAC + ")"
	}
	return c.MAC
}

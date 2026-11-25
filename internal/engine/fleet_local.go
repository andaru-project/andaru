// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
	npdriver "github.com/andaru-project/andaru/internal/netplane/driver"
	npmodel "github.com/andaru-project/andaru/internal/netplane/model"
	"github.com/andaru-project/andaru/internal/render"
)

const (
	fleetLocalTargetID = "local"
	fleetLocalDeviceID = "andaru:local"
)

// localHostSource supplies live local-appliance data to build the built-in root device snapshot.
type localHostSource struct {
	ReadLinks  func(context.Context) ([]host.LinkStatus, error)
	ReadConfig func(context.Context) (config.SnapshotView, error)
	Hostname   func() string
}

type localDeclaration struct {
	Links []struct {
		ID           string    `json:"id"`
		Name         string    `json:"name"`
		Kind         string    `json:"kind"`
		MTU          int       `json:"mtu"`
		Enabled      bool      `json:"enabled"`
		PermanentMAC string    `json:"permanentMac"`
		Parent       string    `json:"parent"`
		VLANID       *int      `json:"vlanId"`
		Members      *[]string `json:"members"`
	} `json:"links"`
	Segments []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Link string `json:"link"`
		Zone string `json:"zone"`
	} `json:"segments"`
	Zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"zones"`
	Rules []any `json:"rules"`
	NAT   struct {
		PortForwards []any `json:"portForwards"`
		OneToOne     []any `json:"oneToOne"`
	} `json:"nat"`
}

// buildLocalSnapshot converts Andaru's observed links and saved configuration into a Netplane snapshot
// without loopback HTTP polling.
func buildLocalSnapshot(ctx context.Context, src localHostSource, now time.Time) (*npdriver.Snapshot, error) {
	links, err := src.ReadLinks(ctx)
	if err != nil {
		return nil, err
	}
	hostname := "andaru"
	if src.Hostname != nil {
		if h := strings.TrimSpace(src.Hostname()); h != "" {
			hostname = h
		}
	} else if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		hostname = strings.TrimSpace(h)
	}

	var rawMACs []string
	mgmtIP := ""
	obsByName := make(map[string]host.LinkStatus, len(links))
	obsByMAC := make(map[string]host.LinkStatus, len(links))
	for _, al := range links {
		if al.LinkType == "loopback" {
			continue
		}
		obsByName[al.Name] = al
		// Only kindless Ethernet interfaces are physical NICs; VLANs, bridges, and bonds inherit
		// their parent's MAC and must not shadow the hardware link.
		if al.LinkType == "ether" && al.Kind == "" {
			if m := strings.ToLower(strings.TrimSpace(al.PermanentAddress)); m != "" {
				obsByMAC[m] = al
			} else if m := strings.ToLower(strings.TrimSpace(al.LinkAddress)); m != "" {
				if _, ok := obsByMAC[m]; !ok {
					obsByMAC[m] = al
				}
			}
		}
		rawMACs = append(rawMACs, al.LinkAddress)
		if mgmtIP == "" {
			for _, a := range al.Addresses {
				if a.Family == "ipv4" && a.Scope == "global" {
					mgmtIP = a.Address
					break
				}
			}
		}
	}

	macs, primaryMAC := npdriver.CollectMACs(rawMACs)
	snap := &npdriver.Snapshot{
		TargetID:    fleetLocalTargetID,
		CollectedAt: now,
		Devices: []npmodel.Device{{
			ID:         fleetLocalDeviceID,
			Driver:     "andaru",
			TargetID:   fleetLocalTargetID,
			Name:       hostname,
			Hostname:   hostname,
			Vendor:     "Andaru",
			Model:      "Andaru Firewall",
			Role:       npmodel.RoleFirewall,
			PrimaryMAC: primaryMAC,
			MACs:       macs,
			MgmtIP:     mgmtIP,
			Online:     true,
			LastSeen:   now,
			Capabilities: []npmodel.Capability{
				npmodel.CapLinks,
				npmodel.CapSegments,
				npmodel.CapZones,
				npmodel.CapGateway,
				npmodel.CapFirewall,
				npmodel.CapConfigPreview,
				npmodel.CapConfigApply,
				npmodel.CapConfigRollbackTimer,
				npmodel.CapConfigBackup,
				npmodel.CapDiagPing,
				npmodel.CapDiagTraceroute,
			},
		}},
	}

	linkIdxByName := make(map[string]int, len(links))
	for _, al := range links {
		if al.LinkType == "loopback" {
			continue
		}
		oper := mapLocalOperState(al.OperState)
		mtu := al.MTU
		linkIdxByName[al.Name] = len(snap.Links)
		snap.Links = append(snap.Links, npmodel.Link{
			DeviceID:  fleetLocalDeviceID,
			Index:     al.IfIndex,
			IfName:    al.Name,
			MAC:       al.LinkAddress,
			AdminUp:   hasLocalFlag(al.Flags, "UP"),
			LinkUp:    oper == npmodel.OperUp,
			OperState: oper,
			MTU:       &mtu,
			Media:     npmodel.MediaOther,
		})
	}

	var saved config.SnapshotView
	var decl localDeclaration
	hasConfig := false
	if src.ReadConfig != nil {
		if sv, err := src.ReadConfig(ctx); err == nil {
			if err := json.Unmarshal(sv.Value, &decl); err == nil {
				saved = sv
				hasConfig = true
			}
		}
	}

	coveredObs := map[string]bool{}
	if !hasConfig {
		snap.MarkUnavailable(npdriver.SectionZones)
		snap.MarkUnavailable(npdriver.SectionFirewalls)
	} else {
		// Map declared link IDs to OS interface names using the renderer's exact resolution rules:
		// physical Ethernet links match kindless hardware MACs; virtual links use render.KernelName.
		linkIfName := make(map[string]string, len(decl.Links))
		linkByID := make(map[string]int, len(decl.Links))
		for i, dl := range decl.Links {
			linkByID[dl.ID] = i
			ifName := dl.Name
			if dl.Kind == "ethernet" {
				if m := strings.ToLower(strings.TrimSpace(dl.PermanentMAC)); m != "" {
					if obs, ok := obsByMAC[m]; ok {
						ifName = obs.Name
					}
				}
			} else if derived := render.KernelName(dl.Kind, dl.ID); derived != "" {
				ifName = derived
			}
			linkIfName[dl.ID] = ifName
		}

		// Record bond/LAG groups and mark tagged VLANs on parent links.
		for _, dl := range decl.Links {
			if dl.Kind == "bond" && dl.Members != nil && len(*dl.Members) > 0 {
				members := make([]string, 0, len(*dl.Members))
				for _, mid := range *dl.Members {
					if name := linkIfName[mid]; name != "" {
						members = append(members, name)
					}
				}
				if len(members) > 0 {
					ifName := linkIfName[dl.ID]
					snap.LagGroups = append(snap.LagGroups, npmodel.LagGroup{
						DeviceID:    fleetLocalDeviceID,
						ID:          ifName,
						Name:        ifName,
						MemberLinks: members,
					})
				}
			}
			if dl.Kind == "vlan" && dl.VLANID != nil && dl.Parent != "" {
				parentName := linkIfName[dl.Parent]
				if idx, ok := linkIdxByName[parentName]; ok {
					if !slices.Contains(snap.Links[idx].TaggedVLANs, *dl.VLANID) {
						snap.Links[idx].TaggedVLANs = append(snap.Links[idx].TaggedVLANs, *dl.VLANID)
					}
				}
			}
		}

		segsByZone := map[string][]string{}
		for _, seg := range decl.Segments {
			if seg.Zone != "" {
				segsByZone[seg.Zone] = append(segsByZone[seg.Zone], seg.ID)
			}
			ifName := linkIfName[seg.Link]
			parentLink := ifName
			var vlanID *int
			adminUp := true
			kind := "ethernet"
			if idx, ok := linkByID[seg.Link]; ok {
				dl := decl.Links[idx]
				adminUp = dl.Enabled
				kind = dl.Kind
				if dl.Kind == "vlan" && dl.VLANID != nil {
					v := *dl.VLANID
					vlanID = &v
					if dl.Parent != "" {
						parentLink = linkIfName[dl.Parent]
					}
				}
			}
			var addrs []npmodel.IPAddress
			oper := npmodel.OperUnknown
			var mtu *int
			if obs, ok := obsByName[ifName]; ok {
				coveredObs[ifName] = true
				oper = mapLocalOperState(obs.OperState)
				m := obs.MTU
				mtu = &m
				addrs = mapLocalAddresses(obs.Addresses)
			}
			snap.Segments = append(snap.Segments, npmodel.Segment{
				DeviceID:   fleetLocalDeviceID,
				ID:         seg.ID,
				Name:       seg.Name,
				Kind:       kind,
				ParentLink: parentLink,
				VLANID:     vlanID,
				ZoneID:     seg.Zone,
				MTU:        mtu,
				AdminUp:    adminUp,
				OperState:  oper,
				Addresses:  addrs,
			})
		}

		for _, z := range decl.Zones {
			snap.Zones = append(snap.Zones, npmodel.Zone{
				DeviceID:   fleetLocalDeviceID,
				ID:         z.ID,
				Name:       z.Name,
				SegmentIDs: segsByZone[z.ID],
			})
		}

		policyCount := len(decl.Rules)
		natCount := len(decl.NAT.PortForwards) + len(decl.NAT.OneToOne)
		snap.Firewalls = append(snap.Firewalls, npmodel.FirewallState{
			DeviceID:        fleetLocalDeviceID,
			Engine:          "nftables",
			PolicyRuleCount: &policyCount,
			NatRuleCount:    &natCount,
			SavedVersion:    string(saved.SavedVersion),
			Services:        []npmodel.ServiceStatus{},
		})
	}

	// Include any observed addressed interfaces not already represented by a saved segment.
	for _, al := range links {
		if al.LinkType == "loopback" || len(al.Addresses) == 0 || coveredObs[al.Name] {
			continue
		}
		mtu := al.MTU
		oper := mapLocalOperState(al.OperState)
		snap.Segments = append(snap.Segments, npmodel.Segment{
			DeviceID:   fleetLocalDeviceID,
			ID:         al.Name,
			Name:       al.Name,
			Kind:       al.LinkType,
			ParentLink: al.Name,
			MTU:        &mtu,
			AdminUp:    hasLocalFlag(al.Flags, "UP"),
			OperState:  oper,
			Addresses:  mapLocalAddresses(al.Addresses),
		})
	}

	return snap, nil
}

func mapLocalAddresses(in []host.AddressStatus) []npmodel.IPAddress {
	out := make([]npmodel.IPAddress, 0, len(in))
	for _, a := range in {
		fam := npmodel.IPv4
		if strings.EqualFold(a.Family, "ipv6") {
			fam = npmodel.IPv6
		}
		out = append(out, npmodel.IPAddress{
			Family:       fam,
			Address:      a.Address,
			PrefixLength: a.PrefixLength,
			Scope:        a.Scope,
		})
	}
	return out
}

func mapLocalOperState(s string) npmodel.OperState {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "UP":
		return npmodel.OperUp
	case "DOWN":
		return npmodel.OperDown
	case "LOWERLAYERDOWN", "NO-CARRIER":
		return npmodel.OperNoCarrier
	default:
		return npmodel.OperUnknown
	}
}

func hasLocalFlag(flags []string, want string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, want) {
			return true
		}
	}
	return false
}

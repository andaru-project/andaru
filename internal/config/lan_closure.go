package config

import (
	"context"
	"encoding/json"
	"errors"
)

// managementLAN copies the management segment, its zone, and its link ancestry.
// An uplink — static IPv4 gateway, DHCPv4, or a PPPoE link — is refused. Same
// three conditions as renderer.uplink.
func managementLAN(d declaration, secrets map[string]string) ([]byte, error) {
	if d.Management == nil || d.Management.Segment == "" {
		return nil, errors.New("NO_MANAGEMENT")
	}
	var seg *segment
	for i := range d.Segments {
		if d.Segments[i].ID == d.Management.Segment {
			seg = &d.Segments[i]
			break
		}
	}
	if seg == nil {
		return nil, errors.New("NO_MANAGEMENT")
	}
	links := map[string]link{}
	for _, l := range d.Links {
		links[l.ID] = l
	}
	current := links[seg.Link]
	if seg.Addressing.IPv4.Gateway != "" || seg.Addressing.IPv4.Mode == "dhcp" || current.Kind == "pppoe" {
		return nil, errors.New("UPLINK_MANAGEMENT")
	}
	var ancestry []link
	seen := map[string]bool{}
	for id := seg.Link; id != "" && !seen[id]; id = links[id].Parent {
		seen[id] = true
		ancestry = append(ancestry, links[id])
	}
	var zones []zone
	for _, z := range d.Zones {
		if z.ID == seg.Zone {
			zones = append(zones, z)
		}
	}
	bound := map[string]string{}
	for _, l := range ancestry {
		if l.Password == nil {
			continue
		}
		if value, ok := secrets[l.Password.Secret]; ok {
			bound[l.Password.Secret] = value
		}
	}
	return json.Marshal(struct {
		Kind          string            `json:"kind"`
		SchemaVersion int               `json:"schemaVersion"`
		Management    management        `json:"management"`
		Links         []link            `json:"links"`
		Segments      []segment         `json:"segments"`
		Zones         []zone            `json:"zones"`
		Secrets       map[string]string `json:"secrets"`
	}{"lan-closure", 1, *d.Management, ancestry, []segment{*seg}, zones, bound})
}

// Directory is the state directory this store opened.
func (s *Store) Directory() string { return s.root }

// ManagementLAN returns the management closure for one saved version. An empty
// version is the saved head.
func (s *Store) ManagementLAN(ctx context.Context, version string) ([]byte, error) {
	id := Version(version)
	if version != "" {
		if _, err := ParseVersion(version); err != nil {
			return nil, err
		}
	}
	var raw []byte
	err := s.read(ctx, id, nil, func(state savedState, _ VersionInfo) error {
		secrets := map[string]string{}
		for name, secret := range state.Secrets.Scopes["core"] {
			secrets[name] = secret.Value
		}
		var e error
		raw, e = managementLAN(state.Config, secrets)
		return e
	})
	return raw, err
}

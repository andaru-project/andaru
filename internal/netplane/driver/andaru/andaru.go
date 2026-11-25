// SPDX-License-Identifier: AGPL-3.0-or-later
package andaru

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// Driver polls an Andaru firewall via its OpenAPI 3.1 REST API (HTTP/HTTPS or unix:// socket).
type Driver struct {
	// LocalDiagnostics runs a diagnostic on this box, for the engine's built-in target at LocalEndpoint.
	LocalDiagnostics func(context.Context, model.DiagnosticRequest) (*model.DiagnosticResult, error)
}

// New returns a new Andaru driver.
func New() *Driver { return &Driver{} }

// Kind returns "andaru".
func (d *Driver) Kind() string { return "andaru" }

// ValidateConfig checks Andaru target configuration.
func (d *Driver) ValidateConfig(cfg driver.TargetConfig) error {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("andaru target %q: endpoint is required (https://... or unix:///run/andaru/engine.sock)", cfg.ID)
	}
	if cfg.ResolvedSecrets["api_token"] != "" {
		return driver.CheckCredentialTransport(cfg)
	}
	return nil
}

// Poll reads observed links (/api/v1/status/links) and saved config metadata (/api/v1/config).
func (d *Driver) Poll(ctx context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}

	client, baseURL := buildClient(cfg)
	defer client.CloseIdleConnections()
	token := cfg.ResolvedSecrets["api_token"]

	var linksResp struct {
		Items []andaruLink `json:"items"`
	}
	if err := getJSON(ctx, client, baseURL+"/api/v1/status/links", token, &linksResp); err != nil {
		return nil, fmt.Errorf("andaru status/links: %w", err)
	}

	// Best-effort saved config summary (links/segments/zones/policy/nat/version).
	var savedResp andaruSavedConfig
	hasSaved := getJSON(ctx, client, baseURL+"/api/v1/config", token, &savedResp) == nil

	now := time.Now().UTC()
	var rawMACs []string
	mgmtIP := ""
	for _, al := range linksResp.Items {
		if al.LinkType == "loopback" {
			continue
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

	// Keyed by target so interface order or added interfaces never change identity; MACs stay join metadata.
	macs, primaryMAC := driver.CollectMACs(rawMACs)
	devID := "andaru:" + cfg.ID
	name := cfg.Name
	if name == "" {
		name = cfg.ID
	}

	caps := []model.Capability{
		model.CapLinks,
		model.CapSegments,
		model.CapZones,
		model.CapGateway,
		model.CapFirewall,
		model.CapConfigPreview,
		model.CapConfigApply,
		model.CapConfigRollbackTimer,
		model.CapConfigBackup,
		model.CapDiagPing,
		model.CapDiagTraceroute,
	}

	snap := &driver.Snapshot{
		TargetID:    cfg.ID,
		CollectedAt: now,
		Devices: []model.Device{{
			ID:           devID,
			Driver:       "andaru",
			TargetID:     cfg.ID,
			Name:         name,
			Hostname:     name,
			Vendor:       "Andaru",
			Model:        "Andaru Firewall",
			Role:         model.RoleFirewall,
			PrimaryMAC:   primaryMAC,
			MACs:         macs,
			MgmtIP:       mgmtIP,
			Online:       true,
			LastSeen:     now,
			Capabilities: caps,
		}},
	}

	for _, al := range linksResp.Items {
		if al.LinkType == "loopback" {
			continue
		}
		oper := mapOperState(al.OperState)
		mtu := al.MTU
		link := model.Link{
			DeviceID:  devID,
			Index:     al.IfIndex,
			IfName:    al.Name,
			MAC:       al.LinkAddress,
			AdminUp:   hasFlag(al.Flags, "UP"),
			LinkUp:    oper == model.OperUp,
			OperState: oper,
			MTU:       &mtu,
			Media:     model.MediaOther,
		}
		snap.Links = append(snap.Links, link)

		if len(al.Addresses) > 0 {
			addrs := make([]model.IPAddress, 0, len(al.Addresses))
			for _, a := range al.Addresses {
				fam := model.IPv4
				if strings.EqualFold(a.Family, "ipv6") {
					fam = model.IPv6
				}
				addrs = append(addrs, model.IPAddress{
					Family:       fam,
					Address:      a.Address,
					PrefixLength: a.PrefixLength,
					Scope:        a.Scope,
				})
			}
			snap.Segments = append(snap.Segments, model.Segment{
				DeviceID:   devID,
				ID:         al.Name,
				Name:       al.Name,
				Kind:       al.LinkType,
				ParentLink: al.Name,
				MTU:        &mtu,
				AdminUp:    link.AdminUp,
				OperState:  oper,
				Addresses:  addrs,
			})
		}
	}

	if !hasSaved { // keep the last zones and firewall summary instead of dropping them
		snap.MarkUnavailable(driver.SectionZones)
		snap.MarkUnavailable(driver.SectionFirewalls)
	} else {
		segsByZone := map[string][]string{}
		for _, seg := range savedResp.Value.Segments {
			if seg.Zone != "" {
				segsByZone[seg.Zone] = append(segsByZone[seg.Zone], seg.ID)
			}
		}
		for _, z := range savedResp.Value.Zones {
			snap.Zones = append(snap.Zones, model.Zone{
				DeviceID:   devID,
				ID:         z.ID,
				Name:       z.Name,
				SegmentIDs: segsByZone[z.ID],
			})
		}
		policyCount := len(savedResp.Value.Rules)
		natCount := len(savedResp.Value.NAT.PortForwards) + len(savedResp.Value.NAT.OneToOne)
		snap.Firewalls = append(snap.Firewalls, model.FirewallState{
			DeviceID:        devID,
			Engine:          "nftables",
			PolicyRuleCount: &policyCount,
			NatRuleCount:    &natCount,
			SavedVersion:    savedResp.SavedVersion,
			Services:        []model.ServiceStatus{},
		})
	}

	return snap, nil
}

func buildClient(cfg driver.TargetConfig) (*http.Client, string) {
	client := driver.NewHTTPClient(cfg, 10*time.Second)
	ep := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if strings.HasPrefix(ep, "unix://") {
		sockPath := strings.TrimPrefix(ep, "unix://")
		client.Transport.(*http.Transport).DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		}
		return client, "http://andaru.local"
	}
	return client, ep
}

func getJSON(ctx context.Context, client *http.Client, url, token string, out any) error {
	return driver.DoJSON(ctx, client, http.MethodGet, url, func(r *http.Request) {
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	}, nil, out)
}

func mapOperState(s string) model.OperState {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "UP":
		return model.OperUp
	case "DOWN":
		return model.OperDown
	case "LOWERLAYERDOWN", "NO-CARRIER":
		return model.OperNoCarrier
	default:
		return model.OperUnknown
	}
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, want) {
			return true
		}
	}
	return false
}

type andaruLink struct {
	IfIndex     int      `json:"ifIndex"`
	Name        string   `json:"name"`
	LinkType    string   `json:"linkType"`
	MTU         int      `json:"mtu"`
	OperState   string   `json:"operState"`
	Flags       []string `json:"flags"`
	LinkAddress string   `json:"linkAddress"`
	Addresses   []struct {
		Family       string `json:"family"`
		Address      string `json:"address"`
		PrefixLength int    `json:"prefixLength"`
		Scope        string `json:"scope"`
	} `json:"addresses"`
}

type andaruSavedConfig struct {
	State        string `json:"state"`
	SavedVersion string `json:"savedVersion"`
	Value        struct {
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
	} `json:"value"`
}

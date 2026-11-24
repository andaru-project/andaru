// SPDX-License-Identifier: AGPL-3.0-or-later
package pfsense

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// Driver polls a pfSense firewall over its REST API (v2) using a scoped least-privilege API key.
type Driver struct{}

// New returns a new pfSense REST API driver.
func New() *Driver { return &Driver{} }

// Kind returns "pfsense".
func (d *Driver) Kind() string { return "pfsense" }

// ValidateConfig checks required pfSense target configuration.
func (d *Driver) ValidateConfig(cfg driver.TargetConfig) error {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("pfsense target %q: endpoint is required", cfg.ID)
	}
	if cfg.ResolvedSecrets["api_key"] == "" {
		return fmt.Errorf("pfsense target %q: api_key secret is required", cfg.ID)
	}
	return driver.CheckCredentialTransport(cfg)
}

// Poll reads system status, interfaces, gateways, DHCP leases, ARP, services, and WireGuard peers.
func (d *Driver) Poll(ctx context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(cfg.Endpoint, "/")
	client := driver.NewHTTPClient(cfg, 15*time.Second)
	defer client.CloseIdleConnections()
	apiKey := cfg.ResolvedSecrets["api_key"]

	var sysResp struct {
		Data struct {
			Hostname    string    `json:"hostname"`
			Version     string    `json:"version"`
			Uptime      string    `json:"uptime"` // formatted, e.g. "1 Day 02 Hours 03 Minutes 04 Seconds"
			CPUUsage    float64   `json:"cpu_usage"`
			MemUsagePct float64   `json:"mem_usage"`
			TempC       float64   `json:"temp_c"`
			CPULoadAvg  []float64 `json:"cpu_load_avg"` // 1, 5, 15 minutes
		} `json:"data"`
	}
	if err := getJSON(ctx, client, baseURL+"/api/v2/status/system", apiKey, &sysResp); err != nil {
		return nil, fmt.Errorf("pfsense status/system: %w", err)
	}

	var ifResp struct {
		Data []struct {
			Name     string `json:"name"`
			Descr    string `json:"descr"`
			HwIf     string `json:"hwif"`
			Enable   bool   `json:"enable"`
			Status   string `json:"status"`
			MAC      string `json:"macaddr"`
			MTU      string `json:"mtu"` // pfREST StringField, may be "" or null
			IPAddr   string `json:"ipaddr"`
			Subnet   string `json:"subnet"`
			IPAddrV6 string `json:"ipaddrv6"`
			SubnetV6 string `json:"subnetv6"`
			InBytes  uint64 `json:"inbytes"`
			OutBytes uint64 `json:"outbytes"`
			InPkts   uint64 `json:"inpkts"`
			OutPkts  uint64 `json:"outpkts"`
			InErrs   uint64 `json:"inerrs"`
			OutErrs  uint64 `json:"outerrs"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, baseURL+"/api/v2/status/interfaces", apiKey, &ifResp); err != nil {
		return nil, fmt.Errorf("pfsense status/interfaces: %w", err)
	}

	// Best-effort calls: a failure marks its section unavailable so the store keeps the old data.
	var unavailable []driver.Section
	var gwResp struct {
		Data []struct {
			Name      string  `json:"name"`
			MonitorIP string  `json:"monitorip"`
			Status    string  `json:"status"`
			DelayMs   float64 `json:"delay"`
			LossPct   float64 `json:"loss"`
			Default   bool    `json:"default"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, baseURL+"/api/v2/status/gateways", apiKey, &gwResp); err != nil {
		unavailable = append(unavailable, driver.SectionGateways)
	}

	var arpResp struct {
		Data []struct {
			IP        string `json:"ip"`
			MAC       string `json:"mac"`
			Interface string `json:"interface"`
			Status    string `json:"status"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, baseURL+"/api/v2/diagnostics/arp_table", apiKey, &arpResp); err != nil {
		unavailable = append(unavailable, driver.SectionNeighbors)
	}

	var dhcpResp struct {
		Data []struct {
			IP       string `json:"ip"`
			MAC      string `json:"mac"`
			Hostname string `json:"hostname"`
			Type     string `json:"type"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, baseURL+"/api/v2/status/dhcp_server/leases", apiKey, &dhcpResp); err != nil {
		unavailable = append(unavailable, driver.SectionDHCP)
	}

	var svcResp struct {
		Data []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Status      bool   `json:"status"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, baseURL+"/api/v2/status/services", apiKey, &svcResp); err != nil {
		unavailable = append(unavailable, driver.SectionFirewalls)
	}

	var wgResp struct {
		Data []struct {
			Tun        string `json:"tunnel_device"`
			Descr      string `json:"descr"`
			PublicKey  string `json:"public_key"`
			Endpoint   string `json:"endpoint"`
			LatestHS   int64  `json:"latest_handshake"`
			TransferRx uint64 `json:"transfer_rx"`
			TransferTx uint64 `json:"transfer_tx"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, baseURL+"/api/v2/status/wireguard/peers", apiKey, &wgResp); err != nil {
		unavailable = append(unavailable, driver.SectionVPNs)
	}

	now := time.Now().UTC()
	var rawMACs []string
	mgmtIP := ""
	for _, iface := range ifResp.Data {
		rawMACs = append(rawMACs, iface.MAC)
		if mgmtIP == "" && strings.EqualFold(iface.Name, "lan") && iface.IPAddr != "" {
			mgmtIP = iface.IPAddr
		}
	}

	// Lowest MAC keeps PrimaryMAC independent of interface response order.
	macs, _ := driver.CollectMACs(rawMACs)
	primaryMAC := ""
	if len(macs) > 0 {
		primaryMAC = slices.Min(macs)
	}
	devID := "pfsense:" + cfg.ID
	name := cfg.Name
	if name == "" {
		name = sysResp.Data.Hostname
	}
	if name == "" {
		name = cfg.ID
	}

	caps := []model.Capability{
		model.CapLinks,
		model.CapSegments,
		model.CapGateway,
		model.CapFirewall,
		model.CapNeighbors,
		model.CapDHCP,
		model.CapFirmwareInfo,
		model.CapDiagPing,
	}
	var sensors []model.SensorReading
	if sysResp.Data.TempC > 0 {
		sensors = append(sensors, model.SensorReading{
			Name: "cpu", Kind: model.SensorTempC, Value: sysResp.Data.TempC, Unit: "C", Status: model.AlertOK,
		})
		caps = append(caps, model.CapSensors)
	}
	if len(wgResp.Data) > 0 {
		caps = append(caps, model.CapVPN)
	}

	var load1 float64
	if len(sysResp.Data.CPULoadAvg) > 0 {
		load1 = sysResp.Data.CPULoadAvg[0]
	}

	snap := &driver.Snapshot{
		Unavailable: unavailable,
		TargetID:    cfg.ID,
		CollectedAt: now,
		Devices: []model.Device{{
			ID:           devID,
			Driver:       "pfsense",
			TargetID:     cfg.ID,
			Name:         name,
			Hostname:     sysResp.Data.Hostname,
			Vendor:       "Netgate",
			Model:        "pfSense",
			Role:         model.RoleFirewall,
			PrimaryMAC:   primaryMAC,
			MACs:         macs,
			MgmtIP:       mgmtIP,
			Firmware:     model.FirmwareInfo{Current: sysResp.Data.Version},
			UptimeSec:    parseUptime(sysResp.Data.Uptime),
			Online:       true,
			LastSeen:     now,
			Capabilities: caps,
			SystemStats: model.SystemStats{
				CPUPct: sysResp.Data.CPUUsage,
				Load1:  load1,
			},
			Sensors: sensors,
		}},
	}

	seenLink := map[string]bool{}
	for idx, iface := range ifResp.Data {
		up := strings.EqualFold(iface.Status, "up")
		oper := model.OperDown
		if !iface.Enable {
			oper = model.OperDisabled
		} else if up {
			oper = model.OperUp
		}
		var mtu *int
		if v, err := strconv.Atoi(strings.TrimSpace(iface.MTU)); err == nil {
			mtu = &v
		}
		ifName := iface.HwIf
		if ifName == "" {
			ifName = iface.Name
		}
		// One physical link (and counter baseline) per hardware interface; each logical
		// interface still contributes its segment.
		if !seenLink[ifName] {
			seenLink[ifName] = true
			snap.Links = append(snap.Links, model.Link{
				DeviceID:  devID,
				Index:     idx + 1,
				IfName:    ifName,
				Label:     iface.Descr,
				MAC:       iface.MAC,
				AdminUp:   iface.Enable,
				LinkUp:    up,
				OperState: oper,
				MTU:       mtu,
				Media:     model.MediaOther,
				Counters: &model.PortCounters{
					RxBytes:   iface.InBytes,
					TxBytes:   iface.OutBytes,
					RxPackets: iface.InPkts,
					TxPackets: iface.OutPkts,
					RxErrors:  iface.InErrs,
					TxErrors:  iface.OutErrs,
				},
			})
		}

		addrs := []model.IPAddress{}
		if iface.IPAddr != "" {
			prefix, _ := strconv.Atoi(strings.TrimSpace(iface.Subnet))
			addrs = append(addrs, model.IPAddress{Family: model.IPv4, Address: iface.IPAddr, PrefixLength: prefix})
		}
		if iface.IPAddrV6 != "" {
			prefix, _ := strconv.Atoi(strings.TrimSpace(iface.SubnetV6))
			addrs = append(addrs, model.IPAddress{Family: model.IPv6, Address: iface.IPAddrV6, PrefixLength: prefix})
		}
		snap.Segments = append(snap.Segments, model.Segment{
			DeviceID:   devID,
			ID:         iface.Name,
			Name:       iface.Descr,
			Kind:       "ethernet",
			ParentLink: ifName,
			MTU:        mtu,
			AdminUp:    iface.Enable,
			OperState:  oper,
			Addresses:  addrs,
		})
	}

	for _, gw := range gwResp.Data {
		rtt := gw.DelayMs
		loss := gw.LossPct
		online := strings.EqualFold(gw.Status, "online") || strings.EqualFold(gw.Status, "none")
		snap.Gateways = append(snap.Gateways, model.GatewayStatus{
			DeviceID: devID,
			Name:     gw.Name,
			Family:   model.IPv4,
			// shortcut: status/gateways has no next-hop field (srcip is the probe source);
			// read routing/gateways to fill GatewayIP if topology needs it.
			MonitorIP: gw.MonitorIP,
			Online:    online,
			RTTMs:     &rtt,
			LossPct:   &loss,
			IsDefault: gw.Default,
		})
	}

	for _, a := range arpResp.Data {
		normMAC, err := model.NormalizeMAC(a.MAC)
		if err != nil {
			continue
		}
		snap.Neighbors = append(snap.Neighbors, model.NeighborEntry{
			DeviceID:    devID,
			Family:      model.IPv4,
			IP:          a.IP,
			MAC:         normMAC,
			SegmentName: a.Interface,
			State:       strings.ToLower(a.Status),
		})
	}

	for _, l := range dhcpResp.Data {
		normMAC, err := model.NormalizeMAC(l.MAC)
		if err != nil {
			continue
		}
		snap.DHCP = append(snap.DHCP, model.DhcpLease{
			DeviceID: devID,
			Family:   model.IPv4,
			IP:       l.IP,
			MAC:      normMAC,
			Hostname: l.Hostname,
			Static:   strings.EqualFold(l.Type, "static"),
		})
	}

	services := make([]model.ServiceStatus, 0, len(svcResp.Data))
	for _, sv := range svcResp.Data {
		st := "stopped"
		if sv.Status {
			st = "running"
		}
		services = append(services, model.ServiceStatus{Name: sv.Name, State: st, Summary: sv.Description})
	}
	// status/system reports no pf state counts: leave StatesUsed/StatesMax unset.
	snap.Firewalls = append(snap.Firewalls, model.FirewallState{
		DeviceID: devID,
		Engine:   "pf",
		Services: services,
	})

	for idx, p := range wgResp.Data {
		var hs *time.Time
		up := false
		if p.LatestHS > 0 {
			t := time.Unix(p.LatestHS, 0).UTC()
			hs = &t
			up = now.Sub(t) < 3*time.Minute
		}
		name := p.Descr
		if name == "" {
			name = fmt.Sprintf("%s-peer-%d", p.Tun, idx)
		}
		snap.VPNs = append(snap.VPNs, model.VpnTunnel{
			DeviceID:      devID,
			ID:            fmt.Sprintf("%s:%s", p.Tun, p.PublicKey),
			Name:          name,
			Kind:          "wireguard",
			Peer:          p.PublicKey,
			Endpoint:      p.Endpoint,
			Up:            up,
			LastHandshake: hs,
			RxBytes:       p.TransferRx,
			TxBytes:       p.TransferTx,
		})
	}

	return snap, nil
}

// parseUptime converts pfSense's get_uptime() text ("1 Day 02 Hours 03 Minutes 04 Seconds")
// to seconds; unknown text yields 0.
func parseUptime(s string) uint64 {
	f := strings.Fields(s)
	var total uint64
	for i := 0; i+1 < len(f); i += 2 {
		n, err := strconv.ParseUint(f[i], 10, 64)
		if err != nil {
			return 0
		}
		unit := strings.ToLower(f[i+1])
		switch {
		case strings.HasPrefix(unit, "day"):
			total += n * 86400
		case strings.HasPrefix(unit, "hour"):
			total += n * 3600
		case strings.HasPrefix(unit, "minute"):
			total += n * 60
		case strings.HasPrefix(unit, "second"):
			total += n
		default:
			return 0
		}
	}
	return total
}

func getJSON(ctx context.Context, client *http.Client, url, apiKey string, out any) error {
	return doJSON(ctx, client, http.MethodGet, url, apiKey, nil, out)
}

// doJSON sends body (when not nil) as JSON and decodes the response into out.
func doJSON(ctx context.Context, client *http.Client, method, url, apiKey string, body, out any) error {
	return driver.DoJSON(ctx, client, method, url, func(r *http.Request) {
		if apiKey != "" {
			r.Header.Set("X-API-Key", apiKey)
		}
	}, body, out)
}

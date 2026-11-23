// SPDX-License-Identifier: AGPL-3.0-or-later

// Package opnsense polls an OPNsense firewall over its core REST API, authenticated with an
// API key and secret. Endpoints and fields follow opnsense/core 25.1 to 26.7.
//
// OPNsense checks the request path against the user's privileges case-sensitively, and 25.1
// spells some paths in camelCase where 25.7 and later use snake_case: those calls try the
// snake_case path first and the camelCase one after a 403 or 404.
package opnsense

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// wgOnlineAge is how recent a WireGuard handshake must be for the peer to count as up; it is
// OPNsense's own threshold for "online" in its WireGuard status page.
const wgOnlineAge = 300 * time.Second

// Driver polls an OPNsense firewall.
type Driver struct {
	// spelling remembers, per target and endpoint, the path variant that answered, so a
	// 25.1 box is not sent (and does not log) a refused snake_case request on every poll.
	spelling sync.Map
	// now is the clock (tests set it); nil means time.Now.
	now func() time.Time
}

func (d *Driver) clock() time.Time {
	if d.now != nil {
		return d.now().UTC()
	}
	return time.Now().UTC()
}

// New returns a new OPNsense REST API driver.
func New() *Driver { return &Driver{} }

// Kind returns "opnsense".
func (d *Driver) Kind() string { return "opnsense" }

// ValidateConfig checks required OPNsense target configuration.
func (d *Driver) ValidateConfig(cfg driver.TargetConfig) error {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("opnsense target %q: endpoint is required", cfg.ID)
	}
	if cfg.ResolvedSecrets["api_key"] == "" || cfg.ResolvedSecrets["api_secret"] == "" {
		return fmt.Errorf("opnsense target %q: api_key and api_secret secrets are required", cfg.ID)
	}
	return driver.CheckCredentialTransport(cfg)
}

// api is one target's authenticated REST client.
type api struct {
	client      *http.Client
	base        string
	key, secret string
	target      string
	spelling    *sync.Map
}

func (d *Driver) newAPI(cfg driver.TargetConfig, timeout time.Duration) *api {
	return &api{
		client:   driver.NewHTTPClient(cfg, timeout),
		base:     strings.TrimRight(cfg.Endpoint, "/"),
		key:      cfg.ResolvedSecrets["api_key"],
		secret:   cfg.ResolvedSecrets["api_secret"],
		target:   cfg.ID,
		spelling: &d.spelling,
	}
}

// do sends body (when not nil) as JSON and decodes the response into out.
func (a *api) do(ctx context.Context, method, path string, body, out any) error {
	return driver.DoJSON(ctx, a.client, method, a.base+path, func(r *http.Request) { r.SetBasicAuth(a.key, a.secret) }, body, out)
}

// get tries each spelling of an endpoint, the one that last answered first, moving on only
// when the API refuses or lacks it.
func (a *api) get(ctx context.Context, out any, paths ...string) error {
	key := a.target + "|" + paths[0]
	if v, ok := a.spelling.Load(key); ok && v != paths[0] {
		paths = append([]string{v.(string)}, slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return p == v })...)
	}
	var err error
	var p string
	for _, p = range paths {
		if err = a.do(ctx, http.MethodGet, p, nil, out); err == nil {
			a.spelling.Store(key, p)
			return nil
		}
		if !absent(err) {
			break
		}
	}
	return fmt.Errorf("%s: %w", p, err)
}

// search reads every row of a search endpoint: a GET returns at most 9999 rows, so the
// request asks for all of them (rowCount -1) in a POST body.
func (a *api) search(ctx context.Context, out any, path string) error {
	if err := a.do(ctx, http.MethodPost, path, map[string]any{"rowCount": -1}, out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// absent reports a refusal (403: the API user lacks the privilege) or a missing endpoint
// (404: the plugin or service is not installed). An optional section is then empty, not
// stale, so the store does not carry its old rows over.
func absent(err error) bool {
	c := driver.HTTPStatus(err)
	return c == http.StatusForbidden || c == http.StatusNotFound
}

// numStr decodes a JSON string, number or null as text: OPNsense returns many counters as
// strings, and some fields changed between the two across releases.
type numStr string

func (n *numStr) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*n = numStr(s)
		return nil
	}
	*n = numStr(b)
	return nil
}

func (n numStr) uint() uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(string(n)), 10, 64)
	return v
}

func (n numStr) int64() (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(string(n)), 10, 64)
	return v, err == nil
}

// float reads the leading number of a value such as "4.2 ms" or "0.0 %".
func (n numStr) float() *float64 { return leadingFloat(string(n)) }

// leadingFloat reads the number a value starts with ("4.2 ms", "100.0%", "0.21"); "~"
// (unmonitored) and other text give nil.
func leadingFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	end := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' && r != '-' })
	if end >= 0 {
		s = s[:end]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

type ifaceRow struct {
	Device      string   `json:"device"`
	Identifier  string   `json:"identifier"`
	Description string   `json:"description"`
	Enabled     *bool    `json:"enabled"`
	Status      string   `json:"status"`
	Flags       []string `json:"flags"`
	MTU         numStr   `json:"mtu"`
	MAC         string   `json:"macaddr"`
	MACHW       string   `json:"macaddr_hw"`
	Media       string   `json:"media"`
	IsPhysical  bool     `json:"is_physical"`
	Addr4       string   `json:"addr4"`
	IPv4        []struct {
		IPAddr string `json:"ipaddr"`
	} `json:"ipv4"`
	IPv6 []struct {
		IPAddr string `json:"ipaddr"`
	} `json:"ipv6"`
	VLAN *struct {
		Tag    numStr `json:"tag"`
		Parent string `json:"parent"`
	} `json:"vlan"`
	Statistics map[string]numStr `json:"statistics"`
}

// leaseRow is a lease row from Kea, dnsmasq or ISC dhcpd; each backend fills its own fields.
type leaseRow struct {
	Address  string `json:"address"`
	HWAddr   string `json:"hwaddr"` // Kea, dnsmasq
	MAC      string `json:"mac"`    // ISC
	Hostname string `json:"hostname"`
	Expire   numStr `json:"expire"` // Kea, dnsmasq: epoch seconds
	IfName   string `json:"if_name"`
	If       string `json:"if"`
	Type     string `json:"type"`  // ISC: dynamic or static
	State    numStr `json:"state"` // Kea: non-zero is declined or reclaimed
}

// Poll reads identity, interfaces, gateways, ARP/NDP, DHCP leases, pf states and WireGuard.
func (d *Driver) Poll(ctx context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	a := d.newAPI(cfg, 15*time.Second)
	defer a.client.CloseIdleConnections()

	var sysInfo struct {
		Name     string   `json:"name"`
		Versions []string `json:"versions"`
	}
	if err := a.get(ctx, &sysInfo, "/api/diagnostics/system/system_information", "/api/diagnostics/system/systemInformation"); err != nil {
		return nil, fmt.Errorf("opnsense system information: %w", err)
	}
	var ifs struct {
		Rows []ifaceRow `json:"rows"`
	}
	if err := a.get(ctx, &ifs, "/api/interfaces/overview/interfaces_info/1"); err != nil {
		return nil, fmt.Errorf("opnsense interfaces: %w", err)
	}
	ifResp := ifs.Rows

	// Optional calls: a refused or missing endpoint leaves its section empty (see absent);
	// any other failure marks it unavailable so the store keeps the old data.
	snap := &driver.Snapshot{TargetID: cfg.ID}
	optional := func(sec driver.Section, err error) {
		if err != nil && !absent(err) {
			snap.MarkUnavailable(sec)
		}
	}
	var sysTime struct {
		Uptime  string `json:"uptime"`
		LoadAvg string `json:"loadavg"`
	}
	_ = a.get(ctx, &sysTime, "/api/diagnostics/system/system_time", "/api/diagnostics/system/systemTime")
	var sysRes struct {
		Memory struct {
			Total numStr `json:"total"`
			Used  numStr `json:"used"`
		} `json:"memory"`
	}
	_ = a.get(ctx, &sysRes, "/api/diagnostics/system/system_resources", "/api/diagnostics/system/systemResources")

	var gwResp struct {
		Items []struct {
			Name    string `json:"name"`
			Address string `json:"address"`
			Status  string `json:"status"`
			Loss    numStr `json:"loss"`
			Delay   numStr `json:"delay"`
			Monitor string `json:"monitor"`
		} `json:"items"`
		Status string `json:"status"`
	}
	// "failed" means configd gave no answer (a box without gateways also reads so, rarely).
	if err := a.get(ctx, &gwResp, "/api/routes/gateway/status"); err != nil {
		optional(driver.SectionGateways, err)
	} else if gwResp.Status == "failed" {
		snap.MarkUnavailable(driver.SectionGateways)
	}

	type neighborRow struct {
		IP        string `json:"ip"`
		MAC       string `json:"mac"`
		Intf      string `json:"intf"`
		Expired   bool   `json:"expired"`
		Permanent bool   `json:"permanent"`
	}
	var arpResp, ndpResp struct {
		Rows []neighborRow `json:"rows"`
	}
	// search_arp and search_ndp keep one spelling in every release.
	optional(driver.SectionNeighbors, a.search(ctx, &arpResp, "/api/diagnostics/interface/search_arp"))
	optional(driver.SectionNeighbors, a.search(ctx, &ndpResp, "/api/diagnostics/interface/search_ndp"))

	// Kea, dnsmasq (25.7 and later) and ISC (core up to 25.7, then a plugin) may each serve
	// leases. A failed one marks the whole section unavailable rather than drop its leases.
	var leases []leaseRow
	for _, p := range []string{"/api/kea/leases4/search", "/api/dnsmasq/leases/search", "/api/dhcpv4/leases/search_lease"} {
		var r struct {
			Rows []leaseRow `json:"rows"`
		}
		err := a.search(ctx, &r, p)
		optional(driver.SectionDHCP, err)
		leases = append(leases, r.Rows...)
	}

	var pfStates struct {
		Current numStr `json:"current"`
		Limit   numStr `json:"limit"`
		Result  string `json:"result"` // "failed" when configd gave no answer
	}
	errPF := a.get(ctx, &pfStates, "/api/diagnostics/firewall/pf_states")
	if errPF == nil && pfStates.Result == "failed" {
		errPF = errors.New("pf_states: failed")
	}

	var wgResp struct {
		Rows []struct {
			If              string `json:"if"`
			Type            string `json:"type"`
			PublicKey       string `json:"public-key"`
			Endpoint        string `json:"endpoint"`
			LatestHandshake numStr `json:"latest-handshake"`
			TransferRx      numStr `json:"transfer-rx"`
			TransferTx      numStr `json:"transfer-tx"`
			Name            string `json:"name"`
		} `json:"rows"`
	}
	errWG := a.search(ctx, &wgResp, "/api/wireguard/service/show")
	optional(driver.SectionVPNs, errWG)

	now := d.clock()
	devID := "opnsense:" + cfg.ID
	product, version := parseVersion(sysInfo.Versions)
	name := cmp.Or(cfg.Name, sysInfo.Name, cfg.ID)

	segOfDevice := map[string]string{}
	var rawMACs []string
	mgmtIP := ""
	for _, r := range ifResp {
		if r.Identifier != "" {
			segOfDevice[r.Device] = r.Identifier
		}
		if r.IsPhysical {
			rawMACs = append(rawMACs, cmp.Or(r.MACHW, r.MAC))
		}
		if r.Identifier == "lan" && mgmtIP == "" {
			mgmtIP, _, _ = strings.Cut(r.Addr4, "/")
		}
	}
	// Lowest MAC keeps PrimaryMAC independent of interface order, as for pfSense.
	macs, _ := driver.CollectMACs(rawMACs)
	primaryMAC := ""
	if len(macs) > 0 {
		primaryMAC = slices.Min(macs)
	}

	caps := []model.Capability{
		model.CapLinks, model.CapSegments, model.CapGateway, model.CapFirewall,
		model.CapNeighbors, model.CapDHCP, model.CapFirmwareInfo, model.CapDiagPing,
	}
	stats := model.SystemStats{MemTotalBytes: sysRes.Memory.Total.uint(), MemUsedBytes: sysRes.Memory.Used.uint()}
	if f := leadingFloat(strings.Split(sysTime.LoadAvg, ",")[0]); f != nil {
		stats.Load1 = *f
	}

	snap.CollectedAt = now
	snap.Devices = []model.Device{{
		ID:           devID,
		Driver:       "opnsense",
		TargetID:     cfg.ID,
		Name:         name,
		Hostname:     sysInfo.Name,
		Vendor:       "Deciso",
		Model:        product,
		Role:         model.RoleFirewall,
		PrimaryMAC:   primaryMAC,
		MACs:         macs,
		MgmtIP:       mgmtIP,
		Firmware:     model.FirmwareInfo{Current: version},
		UptimeSec:    parseUptime(sysTime.Uptime),
		Online:       true,
		LastSeen:     now,
		Capabilities: caps,
		SystemStats:  stats,
	}}

	for i, r := range ifResp {
		adminUp := slices.Contains(r.Flags, "up")
		if r.Enabled != nil {
			adminUp = adminUp && *r.Enabled
		}
		linkUp := r.Status == "up"
		oper := model.OperFromAdminLink(adminUp, linkUp)
		var mtu *int
		if v, ok := r.MTU.int64(); ok {
			mtu = new(int(v))
		}
		if r.IsPhysical {
			speed, duplex, media := parseMedia(r.Media)
			l := model.Link{
				DeviceID:  devID,
				Index:     i + 1,
				IfName:    r.Device,
				Label:     r.Description,
				MAC:       r.MAC,
				AdminUp:   adminUp,
				LinkUp:    linkUp,
				OperState: oper,
				Duplex:    duplex,
				MTU:       mtu,
				Media:     media,
			}
			if linkUp {
				l.SpeedMbps = speed
			}
			if st := r.Statistics; st != nil {
				l.Counters = &model.PortCounters{
					RxBytes:   st["bytes received"].uint(),
					TxBytes:   st["bytes transmitted"].uint(),
					RxPackets: st["packets received"].uint(),
					TxPackets: st["packets transmitted"].uint(),
					RxErrors:  st["input errors"].uint(),
					TxErrors:  st["output errors"].uint(),
					RxDropped: st["input queue drops"].uint(),
				}
			}
			snap.Links = append(snap.Links, l)
		}
		if r.Identifier == "" {
			continue // unassigned: a port, not a logical interface
		}
		seg := model.Segment{
			DeviceID:   devID,
			ID:         r.Identifier,
			Name:       r.Description,
			Kind:       segmentKind(r.Device),
			ParentLink: r.Device,
			MTU:        mtu,
			AdminUp:    adminUp,
			OperState:  oper,
			Addresses:  []model.IPAddress{},
		}
		if r.VLAN != nil {
			seg.Kind, seg.ParentLink = "vlan", r.VLAN.Parent
			if v, ok := r.VLAN.Tag.int64(); ok {
				seg.VLANID = new(int(v))
			}
		}
		for _, ip := range r.IPv4 {
			if a, ok := parsePrefix(ip.IPAddr, model.IPv4); ok {
				seg.Addresses = append(seg.Addresses, a)
			}
		}
		for _, ip := range r.IPv6 {
			if a, ok := parsePrefix(ip.IPAddr, model.IPv6); ok {
				seg.Addresses = append(seg.Addresses, a)
			}
		}
		snap.Segments = append(snap.Segments, seg)
	}

	for _, gw := range gwResp.Items {
		// "~" stands for an address the gateway does not have yet (dynamic, unmonitored).
		addr, monitor := strings.Trim(gw.Address, "~"), strings.Trim(gw.Monitor, "~")
		snap.Gateways = append(snap.Gateways, model.GatewayStatus{
			DeviceID:  devID,
			Name:      gw.Name,
			Family:    familyOf(cmp.Or(addr, monitor)),
			GatewayIP: addr,
			MonitorIP: monitor,
			// dpinger reports "none" for a healthy gateway; delay and loss still forward traffic.
			Online:  gw.Status != "down" && gw.Status != "force_down",
			RTTMs:   gw.Delay.float(),
			LossPct: gw.Loss.float(),
		})
	}

	addNeighbors := func(rows []neighborRow, family model.IPFamily) {
		for _, n := range rows {
			mac, err := model.NormalizeMAC(n.MAC)
			if err != nil {
				continue
			}
			ip, _, _ := strings.Cut(n.IP, "%") // NDP reports link-local addresses with a zone
			state := ""
			switch {
			case n.Permanent:
				state = "permanent"
			case family == model.IPv4 && !n.Expired:
				state = "reachable"
			}
			snap.Neighbors = append(snap.Neighbors, model.NeighborEntry{
				DeviceID: devID, Family: family, IP: ip, MAC: mac,
				SegmentName: cmp.Or(segOfDevice[n.Intf], n.Intf), State: state,
			})
		}
	}
	addNeighbors(arpResp.Rows, model.IPv4)
	addNeighbors(ndpResp.Rows, model.IPv6)

	for _, l := range leases {
		// A DHCPv6 client identified by a DUID-UUID has no MAC; keep its lease without one.
		mac, err := "", error(nil)
		if raw := cmp.Or(l.HWAddr, l.MAC); raw != "" {
			mac, err = model.NormalizeMAC(raw)
		}
		st, hasState := l.State.int64()
		exp, hasExpire := l.Expire.int64()
		// Kea also lists deleted (valid lifetime 0) and unreclaimed expired leases at state 0.
		if err != nil || l.Address == "" || (hasState && st != 0) || (hasExpire && exp > 0 && exp < now.Unix()) {
			continue
		}
		lease := model.DhcpLease{
			DeviceID: devID,
			Family:   familyOf(l.Address), // dnsmasq lists IPv6 leases too
			IP:       l.Address,
			MAC:      mac,
			Hostname: l.Hostname,
			Pool:     cmp.Or(l.IfName, l.If),
			Static:   l.Type == "static",
		}
		if hasExpire && exp > 0 {
			t := time.Unix(exp, 0).UTC()
			lease.ExpiresAt = &t
		}
		snap.DHCP = append(snap.DHCP, lease)
	}

	if errPF == nil {
		fw := model.FirewallState{DeviceID: devID, Engine: "pf", Services: []model.ServiceStatus{}}
		if v, ok := pfStates.Current.int64(); ok {
			fw.StatesUsed = &v
		}
		if v, ok := pfStates.Limit.int64(); ok {
			fw.StatesMax = &v
		}
		snap.Firewalls = append(snap.Firewalls, fw)
	} else {
		optional(driver.SectionFirewalls, errPF)
	}

	for _, p := range wgResp.Rows {
		if p.Type != "peer" {
			continue
		}
		var hs *time.Time
		up := false
		if v, ok := p.LatestHandshake.int64(); ok && v > 0 {
			t := time.Unix(v, 0).UTC()
			hs = &t
			up = now.Sub(t) <= wgOnlineAge
		}
		snap.VPNs = append(snap.VPNs, model.VpnTunnel{
			DeviceID:      devID,
			ID:            p.If + ":" + p.PublicKey,
			Name:          cmp.Or(p.Name, p.If+" peer"),
			Kind:          "wireguard",
			Peer:          p.PublicKey,
			Endpoint:      strings.TrimPrefix(p.Endpoint, "(none)"), // wg's text for no endpoint
			Up:            up,
			LastHandshake: hs,
			RxBytes:       p.TransferRx.uint(),
			TxBytes:       p.TransferTx.uint(),
		})
	}
	// Advertise VPN while WireGuard answers or fails transiently, so carried-over tunnels keep it.
	if len(snap.VPNs) > 0 || (errWG != nil && !absent(errWG)) {
		snap.Devices[0].Capabilities = append(snap.Devices[0].Capabilities, model.CapVPN)
	}
	return snap, nil
}

// familyOf returns the address family of an IP address, IPv4 for anything else.
func familyOf(s string) model.IPFamily {
	if addr, err := netip.ParseAddr(s); err == nil && addr.Is6() {
		return model.IPv6
	}
	return model.IPv4
}

// segmentKinds maps device name prefixes to segment kinds, longest prefix first; VLANs are
// set from the vlan field.
var segmentKinds = []struct{ prefix, kind string }{
	{"pppoe", "pppoe"}, {"lagg", "lag"}, {"bridge", "bridge"}, {"wg", "wireguard"}, {"lo", "loopback"},
	{"ppp", "other"}, {"pptp", "other"}, {"l2tp", "other"}, {"ovpn", "other"}, {"gif", "other"}, {"gre", "other"},
}

func segmentKind(device string) string {
	for _, k := range segmentKinds {
		if strings.HasPrefix(device, k.prefix) {
			return k.kind
		}
	}
	return "ethernet"
}

// parseVersion splits "OPNsense 26.7.9-amd64" into product and version.
func parseVersion(versions []string) (product, version string) {
	if len(versions) > 0 {
		if f := strings.Fields(versions[0]); len(f) >= 2 {
			version, _, _ = strings.Cut(f[len(f)-1], "-")
			return strings.Join(f[:len(f)-1], " "), version
		}
	}
	return "OPNsense", ""
}

// The word after the day count is translated with the GUI language ("12 Tage, 03:04:05").
var uptimeRE = regexp.MustCompile(`^(?:(\d+) \S+, )?(\d+):(\d{2}):(\d{2})$`)

// parseUptime reads system_time's uptime ("12 days, 03:04:05" or "03:04:05"); other text
// gives 0.
func parseUptime(s string) uint64 {
	m := uptimeRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n := func(i int) uint64 { v, _ := strconv.ParseUint(m[i], 10, 64); return v }
	return n(1)*86400 + n(2)*3600 + n(3)*60 + n(4)
}

var mediaRE = regexp.MustCompile(`(?i)\b(\d+)(G?)base-?(\w*)`)

// parseMedia reads ifconfig media text ("1000baseT <full-duplex>", "10Gbase-SR
// <full-duplex,rxpause,txpause>"): speed, duplex and port type. Optical and direct-attach
// media (SR, LR, SX, CR, Twinax, ...) are SFP cages, the rest copper by speed.
func parseMedia(text string) (speedMbps int, duplex string, media model.MediaType) {
	m := mediaRE.FindStringSubmatch(text)
	if m != nil {
		speedMbps, _ = strconv.Atoi(m[1])
		if m[2] != "" {
			speedMbps *= 1000
		}
	}
	switch {
	case strings.Contains(text, "full-duplex"):
		duplex = "full"
	case strings.Contains(text, "half-duplex"):
		duplex = "half"
	}
	pluggable := false
	if m != nil {
		t := strings.ToUpper(m[3])
		pluggable = t != "" && (!strings.HasPrefix(t, "T") || strings.HasPrefix(t, "TWINAX"))
	}
	return speedMbps, duplex, model.MediaForSpeed(speedMbps, pluggable)
}

// parsePrefix reads "192.0.2.1/24" or "fe80::1%igc1/64".
func parsePrefix(s string, family model.IPFamily) (model.IPAddress, bool) {
	addr, bits, ok := strings.Cut(s, "/")
	addr, _, _ = strings.Cut(addr, "%")
	if !ok || addr == "" {
		return model.IPAddress{}, false
	}
	n, err := strconv.Atoi(bits)
	if err != nil {
		return model.IPAddress{}, false
	}
	return model.IPAddress{Family: family, Address: addr, PrefixLength: n}, true
}

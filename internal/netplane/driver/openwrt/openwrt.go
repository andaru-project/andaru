// SPDX-License-Identifier: AGPL-3.0-or-later
package openwrt

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// Driver polls an OpenWrt router, AP, or DSA switch via uhttpd-mod-ubus HTTP JSON-RPC (/ubus).
// It keeps one HTTP client and ubus session per target across polls, logging in again only
// when rpcd rejects the session (rpcd expires idle sessions after 300 s by default).
type Driver struct {
	mu       sync.Mutex
	sessions map[string]*session
}

// session is the per-target state kept between polls. The poller serializes polls of one
// target, so its fields need no lock of their own.
type session struct {
	key    sessionKey
	client *http.Client
	sid    string
}

// sessionKey is what a session depends on: any change starts a new client and login.
type sessionKey struct {
	endpoint, username, password string
	insecure                     bool
}

// New returns a new OpenWrt ubus driver.
func New() *Driver { return &Driver{sessions: map[string]*session{}} }

func (d *Driver) session(cfg driver.TargetConfig) *session {
	key := sessionKey{cfg.Endpoint, cfg.ResolvedSecrets["username"], cfg.ResolvedSecrets["password"], cfg.BoolSetting("insecure_skip_verify", false)}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[cfg.ID]
	if s == nil || s.key != key {
		if s != nil {
			s.client.CloseIdleConnections()
		}
		s = &session{key: key, client: driver.NewHTTPClient(cfg, 15*time.Second)}
		d.sessions[cfg.ID] = s
	}
	return s
}

// Kind returns "openwrt".
func (d *Driver) Kind() string { return "openwrt" }

// ValidateConfig checks required OpenWrt target settings.
func (d *Driver) ValidateConfig(cfg driver.TargetConfig) error {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("openwrt target %q: endpoint is required", cfg.ID)
	}
	if cfg.ResolvedSecrets["username"] == "" || cfg.ResolvedSecrets["password"] == "" {
		return fmt.Errorf("openwrt target %q: username and password secrets are required", cfg.ID)
	}
	return driver.CheckCredentialTransport(cfg)
}

// Poll authenticates via ubus session.login and fetches board/info/device/interface/wireless/host hints.
func (d *Driver) Poll(ctx context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	sess := d.session(cfg)
	client := sess.client
	// Keep the session, not the connections: net/http does not replay a POST that a router
	// closing an idle kept-alive connection cuts mid-request.
	defer client.CloseIdleConnections()

	ubusURL := ubusEndpoint(cfg)
	login := func() error {
		sid, err := ubusLogin(ctx, client, ubusURL, cfg)
		sess.sid = sid
		return err
	}
	// A poll logs in at most once: up front when it holds no session, else when the first
	// call finds the kept session expired.
	relogged := sess.sid == ""
	if relogged {
		if err := login(); err != nil {
			return nil, err
		}
	}
	// uhttpd answers an expired session and an ACL refusal with the same -32002. The required
	// network.device.status call goes first, so once one call has succeeded this poll the
	// session is known good and any later denial is the account's ACL, never a reason to log in.
	verified := false
	call := func(object, method string, params map[string]any, out any) error {
		err := ubusCall(ctx, client, ubusURL, sess.sid, object, method, params, out)
		if err == nil || verified || !errors.Is(err, errAccessDenied) {
			verified = verified || err == nil
			return err
		}
		if relogged {
			// Denied right after a fresh login: start over next poll rather than keep a sid
			// that may be dead (rpcd restarted) or that the ACL refuses for this call.
			sess.sid = ""
			return err
		}
		relogged = true
		if err := login(); err != nil {
			return err
		}
		err = ubusCall(ctx, client, ubusURL, sess.sid, object, method, params, out)
		verified = err == nil
		if errors.Is(err, errAccessDenied) {
			sess.sid = "" // denied right after a fresh login, as above
		}
		return err
	}

	devStatus := map[string]ubusNetDev{}
	if err := call("network.device", "status", map[string]any{}, &devStatus); err != nil {
		return nil, fmt.Errorf("openwrt network.device.status: %w", err)
	}

	var board struct {
		Hostname string `json:"hostname"`
		Model    string `json:"model"`
		Release  struct {
			Description string `json:"description"`
			Version     string `json:"version"`
		} `json:"release"`
	}
	_ = call("system", "board", map[string]any{}, &board)

	var sysInfo struct {
		Uptime uint64    `json:"uptime"`
		Load   []float64 `json:"load"`
		Memory struct {
			Total uint64 `json:"total"`
			Free  uint64 `json:"free"`
		} `json:"memory"`
	}
	_ = call("system", "info", map[string]any{}, &sysInfo)

	var ifDump struct {
		Interface []ubusInterface `json:"interface"`
	}
	// Best-effort calls: a failure marks its section unavailable so the store keeps the old data.
	var unavailable []driver.Section
	if call("network.interface", "dump", map[string]any{}, &ifDump) != nil {
		unavailable = append(unavailable, driver.SectionSegments)
	}

	wirelessStatus := map[string]ubusRadio{}
	if call("network.wireless", "status", map[string]any{}, &wirelessStatus) != nil {
		unavailable = append(unavailable, driver.SectionRadios)
	}

	hostHints := map[string]struct {
		IPAddrs  []string `json:"ipaddrs"`
		IP6Addrs []string `json:"ip6addrs"`
		Name     string   `json:"name"`
	}{}
	if call("luci-rpc", "getHostHints", map[string]any{}, &hostHints) != nil {
		unavailable = append(unavailable, driver.SectionNeighbors)
	}

	now := time.Now().UTC()
	devNames := slices.SortedFunc(maps.Keys(devStatus), comparePortNames)

	var rawMACs []string
	for _, k := range devNames {
		if k != "lo" {
			rawMACs = append(rawMACs, devStatus[k].MacAddr)
		}
	}
	macs, primaryMAC := driver.CollectMACs(rawMACs)

	// Keyed by target so added interfaces never change identity; MACs stay join metadata.
	devID := "openwrt:" + cfg.ID
	name := cfg.Name
	if name == "" {
		name = board.Hostname
	}
	if name == "" {
		name = cfg.ID
	}

	hasWAN := slices.ContainsFunc(ifDump.Interface, func(i ubusInterface) bool { return i.Interface == "wan" || i.Interface == "wan6" })
	role := guessRole(len(wirelessStatus), len(ifDump.Interface), len(devNames), hasWAN)

	caps := []model.Capability{model.CapLinks, model.CapSegments, model.CapFirmwareInfo, model.CapDiagPing, model.CapDiagTraceroute}
	if len(wirelessStatus) > 0 {
		caps = append(caps, model.CapWireless)
	}
	if len(hostHints) > 0 {
		caps = append(caps, model.CapNeighbors)
	}

	var load1 float64
	if len(sysInfo.Load) > 0 {
		load1 = sysInfo.Load[0] / 65536.0
	}
	var memUsed uint64
	if sysInfo.Memory.Total >= sysInfo.Memory.Free {
		memUsed = sysInfo.Memory.Total - sysInfo.Memory.Free
	}

	mgmtIP := ""
	snap := &driver.Snapshot{
		Unavailable: unavailable,
		TargetID:    cfg.ID,
		CollectedAt: now,
	}

	// Links
	idx := 0
	for _, ifName := range devNames {
		if ifName == "lo" {
			continue
		}
		nd := devStatus[ifName]
		if nd.Type == "bridge" {
			continue
		}
		idx++
		speedMbps, duplex := parseOpenWrtSpeed(nd.Speed)
		media := guessMedia(ifName, speedMbps)
		mtu := nd.MTU
		oper := model.OperDown
		if nd.Up && nd.Carrier {
			oper = model.OperUp
		} else if nd.Up && !nd.Carrier {
			oper = model.OperNoCarrier
		}
		var fc *model.FlowControlState
		if f := nd.FlowControl; f != nil {
			// netifd reports the pause result as "negotiated" with pause autoneg on, "selected" without.
			active := f.Selected
			if f.Autoneg != nil && *f.Autoneg {
				active = f.Negotiated
			}
			fc = &model.FlowControlState{
				Rx:      slices.Contains(active, "rx"),
				Tx:      slices.Contains(active, "tx"),
				Autoneg: f.Autoneg,
			}
		}
		snap.Links = append(snap.Links, model.Link{
			DeviceID:    devID,
			Index:       idx,
			IfName:      ifName,
			MAC:         nd.MacAddr,
			AdminUp:     nd.Up,
			LinkUp:      nd.Up && nd.Carrier,
			OperState:   oper,
			SpeedMbps:   speedMbps,
			Duplex:      duplex,
			Autoneg:     nd.Autoneg,
			MTU:         &mtu,
			Media:       media,
			FlowControl: fc,
			Counters:    &nd.Statistics,
		})
	}

	// Segments
	for _, iface := range ifDump.Interface {
		if iface.Interface == "loopback" {
			continue
		}
		addrs := []model.IPAddress{}
		for _, a := range iface.IPv4Address {
			if mgmtIP == "" && iface.Interface == "lan" {
				mgmtIP = a.Address
			}
			addrs = append(addrs, model.IPAddress{
				Family:       model.IPv4,
				Address:      a.Address,
				PrefixLength: a.Mask,
				Source:       iface.Proto,
			})
		}
		v6 := iface.IPv6Address
		// Delegated-prefix LAN addresses are reported only under ipv6-prefix-assignment.
		for _, pa := range iface.IPv6PrefixAssignment {
			if la := pa.LocalAddress; la.Address != "" && !slices.Contains(v6, la) {
				v6 = append(v6, la)
			}
		}
		for _, a := range v6 {
			addrs = append(addrs, model.IPAddress{
				Family:       model.IPv6,
				Address:      a.Address,
				PrefixLength: a.Mask,
				Source:       iface.Proto,
			})
		}
		oper := model.OperDown
		if iface.Up {
			oper = model.OperUp
		}
		snap.Segments = append(snap.Segments, model.Segment{
			DeviceID:   devID,
			ID:         iface.Interface,
			Name:       iface.Interface,
			Kind:       iface.Proto,
			ParentLink: iface.L3Device,
			AdminUp:    iface.Up,
			OperState:  oper,
			Addresses:  addrs,
		})
	}

	// Wireless radios
	for _, rName := range slices.Sorted(maps.Keys(wirelessStatus)) {
		r := wirelessStatus[rName]
		ssids := []model.Ssid{}
		for _, vif := range r.Interfaces {
			ssids = append(ssids, model.Ssid{
				Name:     vif.Config.SSID,
				Enabled:  r.Up,
				Security: vif.Config.Encryption,
			})
		}
		snap.Radios = append(snap.Radios, model.WirelessRadio{
			DeviceID: devID,
			Name:     rName,
			Band:     r.Config.Band,
			SSIDs:    ssids,
		})
	}

	// Neighbors from host hints
	for rawMAC, hint := range hostHints {
		for _, ip := range hint.IPAddrs {
			snap.Neighbors = append(snap.Neighbors, model.NeighborEntry{
				DeviceID: devID, Family: model.IPv4, IP: ip, MAC: rawMAC, State: "reachable",
			})
		}
		for _, ip := range hint.IP6Addrs {
			snap.Neighbors = append(snap.Neighbors, model.NeighborEntry{
				DeviceID: devID, Family: model.IPv6, IP: ip, MAC: rawMAC, State: "reachable",
			})
		}
	}

	fw := board.Release.Description
	if fw == "" {
		fw = board.Release.Version
	}
	snap.Devices = []model.Device{{
		ID:           devID,
		Driver:       "openwrt",
		TargetID:     cfg.ID,
		Name:         name,
		Hostname:     board.Hostname,
		Vendor:       "OpenWrt",
		Model:        board.Model,
		Role:         role,
		PrimaryMAC:   primaryMAC,
		MACs:         macs,
		MgmtIP:       mgmtIP,
		Firmware:     model.FirmwareInfo{Current: fw},
		UptimeSec:    sysInfo.Uptime,
		Online:       true,
		LastSeen:     now,
		Capabilities: caps,
		SystemStats: model.SystemStats{
			MemUsedBytes:  memUsed,
			MemTotalBytes: sysInfo.Memory.Total,
			Load1:         load1,
		},
	}}

	return snap, nil
}

// ubusEndpoint returns the target's /ubus JSON-RPC URL.
func ubusEndpoint(cfg driver.TargetConfig) string {
	u := strings.TrimRight(cfg.Endpoint, "/")
	if !strings.HasSuffix(u, "/ubus") {
		u += "/ubus"
	}
	return u
}

// ubusLogin opens a new rpcd session and returns its id.
func ubusLogin(ctx context.Context, client *http.Client, ubusURL string, cfg driver.TargetConfig) (string, error) {
	var res struct {
		Session string `json:"ubus_rpc_session"`
	}
	err := ubusCall(ctx, client, ubusURL, "00000000000000000000000000000000", "session", "login", map[string]any{
		"username": cfg.ResolvedSecrets["username"],
		"password": cfg.ResolvedSecrets["password"],
	}, &res)
	if err != nil {
		return "", fmt.Errorf("openwrt ubus login: %w", err)
	}
	if res.Session == "" {
		return "", errors.New("openwrt ubus login: empty session")
	}
	return res.Session, nil
}

// errAccessDenied is uhttpd refusing a call: an expired or unknown session, or an ACL refusal.
var errAccessDenied = errors.New("ubus access denied")

// UBUS_STATUS_NOT_FOUND (no such object, method or, for file.exec, file) and
// UBUS_STATUS_PERMISSION_DENIED (rpcd refusing a file the session's ACL does not grant).
// The latter is not errAccessDenied: it never means the session expired.
const (
	ubusStatusNotFound         = 4
	ubusStatusPermissionDenied = 6
)

var (
	errNotFound         = errors.New("ubus not found")
	errPermissionDenied = errors.New("ubus permission denied")
)

func ubusCall(ctx context.Context, client *http.Client, url, sid, object, method string, params map[string]any, out any) error {
	var rpcResp struct {
		Result []json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	envelope := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "call", "params": []any{sid, object, method, params}}
	if err := driver.DoJSON(ctx, client, http.MethodPost, url, nil, envelope, &rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		if rpcResp.Error.Code == -32002 { // uhttpd: expired or unknown session, or an ACL refusal
			return fmt.Errorf("ubus error %d: %s: %w", rpcResp.Error.Code, rpcResp.Error.Message, errAccessDenied)
		}
		return fmt.Errorf("ubus error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if len(rpcResp.Result) < 1 {
		return fmt.Errorf("empty ubus result")
	}
	var code int
	if err := json.Unmarshal(rpcResp.Result[0], &code); err != nil || code != 0 {
		switch code {
		case ubusStatusNotFound:
			return fmt.Errorf("ubus call %s.%s returned status %d: %w", object, method, code, errNotFound)
		case ubusStatusPermissionDenied:
			return fmt.Errorf("ubus call %s.%s returned status %d: %w", object, method, code, errPermissionDenied)
		}
		return fmt.Errorf("ubus call %s.%s returned status %d", object, method, code)
	}
	if out != nil && len(rpcResp.Result) >= 2 {
		return json.Unmarshal(rpcResp.Result[1], out)
	}
	return nil
}

// comparePortNames orders names by prefix, then numeric suffix, so lan2 sorts before lan10.
func comparePortNames(a, b string) int {
	ap, an := splitPortNumber(a)
	bp, bn := splitPortNumber(b)
	if c := strings.Compare(ap, bp); c != 0 {
		return c
	}
	if c := cmp.Compare(an, bn); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// splitPortNumber splits a trailing decimal number off name; n is -1 when there is none.
func splitPortNumber(name string) (prefix string, n int) {
	prefix = strings.TrimRight(name, "0123456789")
	n, err := strconv.Atoi(name[len(prefix):])
	if err != nil {
		return name, -1
	}
	return prefix, n
}

func parseOpenWrtSpeed(raw string) (int, string) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return 0, "unknown"
	}
	duplex := "unknown"
	if strings.HasSuffix(s, "F") {
		duplex = "full"
		s = strings.TrimSuffix(s, "F")
	} else if strings.HasSuffix(s, "H") {
		duplex = "half"
		s = strings.TrimSuffix(s, "H")
	}
	var speed int
	_, _ = fmt.Sscanf(s, "%d", &speed)
	return speed, duplex
}

// guessMedia classifies a device by name and speed. Every non-bridge device is listed: a bond or
// team reports its members' sum, and tun devices report 10 Mb/s, so copper below 100 Mb/s is other.
func guessMedia(ifName string, speedMbps int) model.MediaType {
	lower := strings.ToLower(ifName)
	if strings.HasPrefix(lower, "bond") || strings.HasPrefix(lower, "team") {
		return model.MediaOther
	}
	pluggable := strings.Contains(lower, "sfp")
	if !pluggable && speedMbps < 100 {
		return model.MediaOther
	}
	return model.MediaForSpeed(speedMbps, pluggable)
}

type ubusNetDev struct {
	Type        string `json:"type"`
	Up          bool   `json:"up"`
	Carrier     bool   `json:"carrier"`
	MTU         int    `json:"mtu"`
	MacAddr     string `json:"macaddr"`
	Speed       string `json:"speed"`
	Autoneg     *bool  `json:"autoneg"`
	FlowControl *struct {
		Autoneg    *bool    `json:"autoneg"`
		Selected   []string `json:"selected"`
		Negotiated []string `json:"negotiated"`
	} `json:"flow-control"`
	// netifd's statistics keys match PortCounters' JSON names.
	Statistics model.PortCounters `json:"statistics"`
}

type ubusInterface struct {
	Interface   string `json:"interface"`
	Up          bool   `json:"up"`
	Proto       string `json:"proto"`
	L3Device    string `json:"l3_device"`
	IPv4Address []struct {
		Address string `json:"address"`
		Mask    int    `json:"mask"`
	} `json:"ipv4-address"`
	IPv6Address          []ubusAddress `json:"ipv6-address"`
	IPv6PrefixAssignment []struct {
		LocalAddress ubusAddress `json:"local-address"`
	} `json:"ipv6-prefix-assignment"`
}

type ubusAddress struct {
	Address string `json:"address"`
	Mask    int    `json:"mask"`
}

type ubusRadio struct {
	Up     bool `json:"up"`
	Config struct {
		Band string `json:"band"`
	} `json:"config"`
	Interfaces []struct {
		Config struct {
			SSID       string `json:"ssid"`
			Encryption string `json:"encryption"`
		} `json:"config"`
	} `json:"interfaces"`
}

// guessRole classifies an OpenWrt device. A device with a wan interface routes, however many
// ports it has; without one, many ports and no radios mean a switch.
func guessRole(radios, ifaces, devs int, hasWAN bool) model.DeviceRole {
	switch {
	case hasWAN:
		return model.RoleRouter
	case radios > 0 && ifaces <= 2:
		return model.RoleAP
	case devs >= 6 && radios == 0:
		return model.RoleSwitch
	}
	return model.RoleRouter
}

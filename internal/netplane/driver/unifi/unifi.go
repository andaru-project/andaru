// SPDX-License-Identifier: AGPL-3.0-or-later
package unifi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// Driver polls a UniFi Network controller (UniFi OS Server or standalone) over its local REST API.
type Driver struct {
	mu       sync.Mutex
	sessions map[string]*session
}

// session is the per-target client and login state kept between polls, so the controller
// sees one login per session lifetime instead of one per poll. The poller serializes polls
// of one target, so its fields need no lock of their own.
type session struct {
	key      sessionKey
	client   *http.Client // carries the login cookie in its jar
	loggedIn bool
}

// sessionKey is what a session depends on: any change starts a new client and login.
type sessionKey struct {
	endpoint, username, password, apiKey string
	unifiOS, insecure                    bool
}

// New returns a new UniFi Network controller driver.
func New() *Driver { return &Driver{sessions: map[string]*session{}} }

func (d *Driver) session(cfg driver.TargetConfig) *session {
	sec := cfg.ResolvedSecrets
	key := sessionKey{cfg.Endpoint, sec["username"], sec["password"], sec["api_key"],
		cfg.BoolSetting("is_unifi_os", true), cfg.BoolSetting("insecure_skip_verify", false)}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[cfg.ID]
	if s == nil || s.key != key {
		if s != nil {
			s.client.CloseIdleConnections()
		}
		c := driver.NewHTTPClient(cfg, 15*time.Second)
		c.Jar, _ = cookiejar.New(nil)
		s = &session{key: key, client: c}
		d.sessions[cfg.ID] = s
	}
	return s
}

// Kind returns "unifi".
func (d *Driver) Kind() string { return "unifi" }

// ValidateConfig checks required UniFi target settings.
func (d *Driver) ValidateConfig(cfg driver.TargetConfig) error {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("unifi target %q: endpoint is required", cfg.ID)
	}
	hasAPIKey := cfg.ResolvedSecrets["api_key"] != ""
	hasUserPass := cfg.ResolvedSecrets["username"] != "" && cfg.ResolvedSecrets["password"] != ""
	if !hasAPIKey && !hasUserPass {
		return fmt.Errorf("unifi target %q: provide either api_key or username+password secrets", cfg.ID)
	}
	return driver.CheckCredentialTransport(cfg)
}

// Poll authenticates against the UniFi controller and fetches devices and active clients.
func (d *Driver) Poll(ctx context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(cfg.Endpoint, "/")
	site := cfg.StringSetting("site", "default")
	isUniFiOS := cfg.BoolSetting("is_unifi_os", true)

	sess := d.session(cfg)
	client := sess.client
	// Keep the login cookie, not the connections, so an idle controller holds nothing for us.
	defer client.CloseIdleConnections()
	apiKey := cfg.ResolvedSecrets["api_key"]
	login := func() error {
		sess.loggedIn = false
		loginPath := "/api/login"
		if isUniFiOS {
			loginPath = "/api/auth/login"
		}
		loginBody, _ := json.Marshal(map[string]any{
			"username": cfg.ResolvedSecrets["username"],
			"password": cfg.ResolvedSecrets["password"],
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+loginPath, bytes.NewReader(loginBody))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("unifi login: %w", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("unifi login HTTP %d", resp.StatusCode)
		}
		sess.loggedIn = true
		return nil
	}
	// A poll logs in at most once: up front when it holds no login, else on the first 401.
	relogged := apiKey == "" && !sess.loggedIn
	if relogged {
		if err := login(); err != nil {
			return nil, err
		}
	}
	get := func(url string, out any) error {
		err := getJSON(ctx, client, url, apiKey, out)
		if apiKey != "" || relogged || driver.HTTPStatus(err) != http.StatusUnauthorized {
			return err
		}
		relogged = true
		if err := login(); err != nil {
			return err
		}
		return getJSON(ctx, client, url, apiKey, out)
	}

	prefix := ""
	if isUniFiOS {
		prefix = "/proxy/network"
	}

	var devResp struct {
		Data []unifiDevice `json:"data"`
	}
	if err := get(baseURL+prefix+"/api/s/"+site+"/stat/device", &devResp); err != nil {
		return nil, fmt.Errorf("unifi stat/device: %w", err)
	}

	var staResp struct {
		Data []unifiClient `json:"data"`
	}
	// Best effort: a failed client list keeps the stored one instead of publishing departures.
	staErr := get(baseURL+prefix+"/api/s/"+site+"/stat/sta", &staResp)

	now := time.Now().UTC()
	snap := &driver.Snapshot{
		TargetID:    cfg.ID,
		CollectedAt: now,
	}
	if staErr != nil {
		snap.MarkUnavailable(driver.SectionClients)
	}

	macToDevID := make(map[string]string, len(devResp.Data))
	for _, ud := range devResp.Data {
		normMAC, err := model.NormalizeMAC(ud.MAC)
		if err != nil {
			continue
		}
		devID := "unifi:" + normMAC
		macToDevID[normMAC] = devID

		role := mapRole(ud.Type)
		name := ud.Name
		if name == "" {
			name = ud.Model
		}
		caps := []model.Capability{model.CapFirmwareInfo}
		if len(ud.PortTable) > 0 {
			caps = append(caps, model.CapLinks, model.CapVLANs, model.CapSTP)
		}
		if len(ud.LLDPTable) > 0 {
			caps = append(caps, model.CapLLDP)
		}
		if len(ud.RadioTable) > 0 {
			caps = append(caps, model.CapWireless)
		}
		if ud.TotalMaxPower > 0 {
			caps = append(caps, model.CapPoE)
		}
		if role == model.RoleGateway {
			caps = append(caps, model.CapGateway)
		}

		var poeBudget *model.PoEBudget
		if ud.TotalMaxPower > 0 {
			poeBudget = &model.PoEBudget{TotalWatts: ud.TotalMaxPower}
		}

		var sensors []model.SensorReading
		if ud.HasTemperature && ud.GeneralTemperature > 0 {
			sensors = append(sensors, model.SensorReading{
				Name:   "chassis",
				Kind:   model.SensorTempC,
				Value:  ud.GeneralTemperature,
				Unit:   "C",
				Status: model.AlertOK,
			})
			caps = append(caps, model.CapSensors)
		}

		lastSeen := now
		if ud.LastSeen > 0 {
			lastSeen = time.Unix(ud.LastSeen, 0).UTC()
		}

		dev := model.Device{
			ID:         devID,
			Driver:     "unifi",
			TargetID:   cfg.ID,
			Name:       name,
			Hostname:   ud.Hostname,
			Vendor:     "Ubiquiti",
			Model:      ud.Model,
			Role:       role,
			PrimaryMAC: normMAC,
			MACs:       []string{normMAC},
			MgmtIP:     ud.IP,
			Firmware: model.FirmwareInfo{
				Current:   ud.Version,
				Available: ud.UpgradeToFirmware,
				Serial:    ud.Serial,
			},
			UptimeSec:    ud.Uptime,
			Online:       ud.State == 1,
			LastSeen:     lastSeen,
			Capabilities: caps,
			SystemStats: model.SystemStats{
				CPUPct:        ud.SystemStats.CPU.Float64(),
				MemUsedBytes:  ud.SysStats.MemUsed,
				MemTotalBytes: ud.SysStats.MemTotal,
				Load1:         ud.SysStats.LoadAvg1.Float64(),
			},
			Sensors:   sensors,
			PoEBudget: poeBudget,
		}

		// Ports
		var usedPoE float64
		for _, p := range ud.PortTable {
			media := mapMedia(p.Media)
			ifName := portName(p.PortIdx)
			duplex := "half"
			if p.FullDuplex {
				duplex = "full"
			}
			autoneg := p.Autoneg
			link := model.Link{
				DeviceID:  devID,
				Index:     p.PortIdx,
				IfName:    ifName,
				Label:     p.Name,
				AdminUp:   p.Enable,
				LinkUp:    p.Up,
				OperState: model.OperFromAdminLink(p.Enable, p.Up),
				SpeedMbps: p.Speed,
				Duplex:    duplex,
				Autoneg:   &autoneg,
				Media:     media,
				StpState:  mapStp(p.StpState),
				Counters: &model.PortCounters{
					RxBytes:   p.RxBytes,
					TxBytes:   p.TxBytes,
					RxPackets: p.RxPackets,
					TxPackets: p.TxPackets,
					RxErrors:  p.RxErrors,
					TxErrors:  p.TxErrors,
					RxDropped: p.RxDropped,
					TxDropped: p.TxDropped,
				},
			}
			if p.PortPoE {
				w := p.PoEPower.Float64()
				usedPoE += w
				link.PoE = &model.PortPoE{
					Supported: true,
					Enabled:   p.PoEEnable,
					State:     p.PoEMode,
					Watts:     w,
					Class:     p.PoEClass,
				}
			}
			if p.SfpFound {
				link.SFP = &model.SfpDom{
					Present:    true,
					Vendor:     p.SfpVendor,
					PartNumber: p.SfpPart,
					Serial:     p.SfpSerial,
					TempC:      p.SfpTemp.Ptr(),
					VoltageV:   p.SfpVoltage.Ptr(),
					BiasMa:     p.SfpCurrent.Ptr(),
					TxPowerDbm: p.SfpTxPower.Ptr(),
					RxPowerDbm: p.SfpRxPower.Ptr(),
				}
			}
			snap.Links = append(snap.Links, link)
		}
		if dev.PoEBudget != nil {
			dev.PoEBudget.UsedWatts = usedPoE
		}
		snap.Devices = append(snap.Devices, dev)

		// LLDP neighbors
		for _, n := range ud.LLDPTable {
			snap.LLDP = append(snap.LLDP, model.LldpNeighbor{
				DeviceID:      devID,
				LocalLink:     portName(n.LocalPortIdx),
				ChassisID:     n.ChassisID,
				RemotePort:    n.PortID,
				RemoteSysName: n.SystemName,
				RemoteMgmtIP:  n.MgmtIP,
			})
		}

		// Wireless mesh uplink
		if ud.Uplink != nil && ud.Uplink.Type == "wireless" && ud.Uplink.UplinkMAC != "" {
			snap.WirelessUplinks = append(snap.WirelessUplinks, model.WirelessUplink{
				DeviceID:   devID,
				PeerMAC:    ud.Uplink.UplinkMAC,
				SignalDbm:  ud.Uplink.Rssi,
				TxRateMbps: ud.Uplink.TxRate / 1000,
				RxRateMbps: ud.Uplink.RxRate / 1000,
			})
		}

		// Wireless radios & SSIDs
		for _, r := range ud.RadioTable {
			band := mapBand(r.Radio)
			ssids := []model.Ssid{}
			for _, v := range ud.VapTable {
				if v.Radio == r.Radio {
					ssids = append(ssids, model.Ssid{
						Name:        v.ESSID,
						Enabled:     v.Up,
						ClientCount: v.NumSta,
					})
				}
			}
			snap.Radios = append(snap.Radios, model.WirelessRadio{
				DeviceID:   devID,
				Name:       r.Name,
				Band:       band,
				Channel:    r.Channel,
				WidthMHz:   r.HT,
				TxPowerDbm: r.TxPower,
				SSIDs:      ssids,
			})
		}
	}

	// Clients
	for _, st := range staResp.Data {
		normMAC, err := model.NormalizeMAC(st.MAC)
		if err != nil {
			continue
		}
		connType := "wireless"
		attachedID := ""
		attachedLink := ""
		if st.IsWired {
			connType = "wired"
			if swMac, err := model.NormalizeMAC(st.SwMAC); err == nil {
				attachedID = macToDevID[swMac]
			}
			if st.SwPort > 0 {
				attachedLink = portName(st.SwPort)
			}
		} else {
			if apMac, err := model.NormalizeMAC(st.ApMAC); err == nil {
				attachedID = macToDevID[apMac]
			}
		}
		ips := []string{}
		if st.IP != "" {
			ips = append(ips, st.IP)
		}
		name := st.Name
		if name == "" {
			name = st.Hostname
		}
		c := model.Client{
			MAC:              normMAC,
			IPs:              ips,
			Hostname:         name,
			VendorOUI:        st.OUI,
			Online:           true,
			ConnectionType:   connType,
			AttachedDeviceID: attachedID,
			AttachedLink:     attachedLink,
			SSID:             st.ESSID,
			LastSeen:         now,
		}
		if st.Signal != 0 {
			sig := st.Signal
			c.SignalDbm = &sig
		}
		if st.TxRate > 0 {
			tx := st.TxRate / 1000
			c.TxRateMbps = &tx
		}
		if st.RxRate > 0 {
			rx := st.RxRate / 1000
			c.RxRateMbps = &rx
		}
		if st.VLAN > 0 {
			v := st.VLAN
			c.VLAN = &v
		}
		snap.Clients = append(snap.Clients, c)
	}

	return snap, nil
}

func getJSON(ctx context.Context, client *http.Client, url, apiKey string, out any) error {
	return driver.DoJSON(ctx, client, http.MethodGet, url, func(r *http.Request) {
		if apiKey != "" {
			r.Header.Set("X-API-KEY", apiKey)
		}
	}, nil, out)
}

func mapRole(typ string) model.DeviceRole {
	switch strings.ToLower(typ) {
	case "usw":
		return model.RoleSwitch
	case "uap":
		return model.RoleAP
	case "ugw", "uxg", "udm":
		return model.RoleGateway
	default:
		return model.RoleUnknown
	}
}

func mapMedia(m string) model.MediaType {
	switch strings.ToUpper(strings.TrimSpace(m)) {
	case "FE":
		return model.MediaFE
	case "GE":
		return model.MediaGE
	case "2.5GE":
		return model.Media25GE
	case "5GE":
		return model.Media5GE
	case "10GE":
		return model.Media10GE
	case "SFP":
		return model.MediaSFP
	case "SFP+":
		return model.MediaSFPPl
	case "SFP28":
		return model.MediaSFP28
	case "QSFP28":
		return model.MediaQSFP28
	default:
		return model.MediaOther
	}
}

func mapStp(s string) model.StpState {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "forwarding":
		return model.StpForwarding
	case "blocking":
		return model.StpBlocking
	case "discarding":
		return model.StpDiscarding
	case "learning":
		return model.StpLearning
	case "listening":
		return model.StpListening
	case "disabled":
		return model.StpDisabled
	default:
		return ""
	}
}

func mapBand(r string) string {
	switch strings.ToLower(r) {
	case "ng":
		return "2.4GHz"
	case "na":
		return "5GHz"
	case "6e":
		return "6GHz"
	default:
		return r
	}
}

// flexFloat decodes UniFi JSON fields that arrive as either string ("12.4") or number (12.4).
type flexFloat struct {
	val float64
	set bool
}

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" || s == `""` {
		return nil
	}
	// Nonfinite values would make the snapshot unmarshalable as JSON, so they count as absent.
	if v, err := strconv.ParseFloat(strings.Trim(s, `"`), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		f.val = v
		f.set = true
	}
	return nil
}

func (f flexFloat) Float64() float64 { return f.val }
func (f flexFloat) Ptr() *float64 {
	if !f.set {
		return nil
	}
	v := f.val
	return &v
}

type unifiDevice struct {
	MAC                string  `json:"mac"`
	Model              string  `json:"model"`
	Type               string  `json:"type"`
	Name               string  `json:"name"`
	Hostname           string  `json:"hostname"`
	IP                 string  `json:"ip"`
	Version            string  `json:"version"`
	UpgradeToFirmware  string  `json:"upgrade_to_firmware"`
	Serial             string  `json:"serial"`
	State              int     `json:"state"`
	Uptime             uint64  `json:"uptime"`
	LastSeen           int64   `json:"last_seen"`
	TotalMaxPower      float64 `json:"total_max_power"`
	HasTemperature     bool    `json:"has_temperature"`
	GeneralTemperature float64 `json:"general_temperature"`
	SysStats           struct {
		LoadAvg1 flexFloat `json:"loadavg_1"`
		MemTotal uint64    `json:"mem_total"`
		MemUsed  uint64    `json:"mem_used"`
	} `json:"sys_stats"`
	SystemStats struct {
		CPU flexFloat `json:"cpu"`
	} `json:"system-stats"`
	PortTable []struct {
		PortIdx    int       `json:"port_idx"`
		Name       string    `json:"name"`
		Media      string    `json:"media"`
		Enable     bool      `json:"enable"`
		Up         bool      `json:"up"`
		Speed      int       `json:"speed"`
		FullDuplex bool      `json:"full_duplex"`
		Autoneg    bool      `json:"autoneg"`
		StpState   string    `json:"stp_state"`
		PortPoE    bool      `json:"port_poe"`
		PoEEnable  bool      `json:"poe_enable"`
		PoEMode    string    `json:"poe_mode"`
		PoEClass   string    `json:"poe_class"`
		PoEPower   flexFloat `json:"poe_power"`
		SfpFound   bool      `json:"sfp_found"`
		SfpVendor  string    `json:"sfp_vendor"`
		SfpPart    string    `json:"sfp_part"`
		SfpSerial  string    `json:"sfp_serial"`
		SfpTemp    flexFloat `json:"sfp_temperature"`
		SfpVoltage flexFloat `json:"sfp_voltage"`
		SfpCurrent flexFloat `json:"sfp_current"`
		SfpTxPower flexFloat `json:"sfp_txpower"`
		SfpRxPower flexFloat `json:"sfp_rxpower"`
		RxBytes    uint64    `json:"rx_bytes"`
		TxBytes    uint64    `json:"tx_bytes"`
		RxPackets  uint64    `json:"rx_packets"`
		TxPackets  uint64    `json:"tx_packets"`
		RxErrors   uint64    `json:"rx_errors"`
		TxErrors   uint64    `json:"tx_errors"`
		RxDropped  uint64    `json:"rx_dropped"`
		TxDropped  uint64    `json:"tx_dropped"`
	} `json:"port_table"`
	LLDPTable []struct {
		LocalPortIdx int    `json:"local_port_idx"`
		ChassisID    string `json:"chassis_id"`
		PortID       string `json:"port_id"`
		SystemName   string `json:"lldp_system_name"`
		MgmtIP       string `json:"mgmt_ip"`
	} `json:"lldp_table"`
	Uplink *struct {
		Type      string `json:"type"`
		UplinkMAC string `json:"uplink_mac"`
		Rssi      int    `json:"rssi"`
		TxRate    int    `json:"tx_rate"`
		RxRate    int    `json:"rx_rate"`
	} `json:"uplink"`
	RadioTable []struct {
		Name    string `json:"name"`
		Radio   string `json:"radio"`
		Channel int    `json:"channel"`
		HT      int    `json:"ht"`
		TxPower int    `json:"tx_power"`
	} `json:"radio_table"`
	VapTable []struct {
		Radio  string `json:"radio"`
		ESSID  string `json:"essid"`
		Up     bool   `json:"up"`
		NumSta int    `json:"num_sta"`
	} `json:"vap_table"`
}

type unifiClient struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	OUI      string `json:"oui"`
	IsWired  bool   `json:"is_wired"`
	SwMAC    string `json:"sw_mac"`
	SwPort   int    `json:"sw_port"`
	ApMAC    string `json:"ap_mac"`
	ESSID    string `json:"essid"`
	Signal   int    `json:"signal"`
	TxRate   int    `json:"tx_rate"`
	RxRate   int    `json:"rx_rate"`
	VLAN     int    `json:"vlan"`
}

// portName is the link name of a UniFi switch port.
func portName(idx int) string { return "port" + strconv.Itoa(idx) }

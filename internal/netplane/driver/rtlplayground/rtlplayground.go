// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rtlplayground polls switches running the RTLPlayground firmware (RTL837x/RTL836x
// on an 8051 MCU). The device is weak: its web server holds one TCP connection (shared with
// the web UI), never keeps connections alive, builds each JSON reply in a 2.5 kB buffer, and
// keeps at most 4 login sessions, evicting the least recently used. Its Prometheus exporter
// has its own port and connection slot, shared with any Prometheus scraper.
//
// Written against the firmware's HTTP, JSON and exporter interfaces as of upstream
// logicog/RTLPlayground main in November 2026 and the andrebrait fork (v0.1.0-fe02652c),
// built with PROMETHEUS=1 and without UNIFI. Checked live on RTL8373 and RTL8367 boards.
//
// So the driver:
//   - reads link state, speed, counters, SFP DOM, temperatures and identity from the public
//     /metrics exporter, once per poll;
//   - only with a password, logs in when it holds no session and refreshes LLDP, LAG,
//     system information and the forwarding table on slow schedules, replaying the cached
//     results in every snapshot; a failing data set backs off on its own, and a refused login
//     or an unreachable web server pauses all JSON work;
//   - sends one request at a time with a pause between requests;
//   - never calls the endpoints that change state (/l2_del.json, /reset, /cmd, /config, uploads).
package rtlplayground

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

const (
	defaultMetricsPort = "9100"
	requestTimeout     = 15 * time.Second
	minPollInterval    = 30 * time.Second
	// Refresh periods of the data behind the login; each is cheap for the device but shares
	// the web UI's only connection slot.
	lldpEvery = 5 * time.Minute
	infoEvery = 10 * time.Minute
	fdbEvery  = 15 * time.Minute
	// A refused login or unreachable web server pauses all JSON work this long; a data set
	// that fails otherwise waits half its period, at least this long.
	retryAfter = 2 * time.Minute
	// A full forwarding table is 4096 entries at about 29 per page.
	maxFdbPages = 160
)

var (
	// requestGap spaces consecutive requests so the device and its web UI get the slot back.
	requestGap = 150 * time.Millisecond
	// scrapeRetryGap waits out a Prometheus scrape holding the exporter's only connection.
	scrapeRetryGap = time.Second
)

// Driver polls RTLPlayground switches. It keeps per-target session and cache state.
type Driver struct {
	mu      sync.Mutex
	targets map[string]*target
}

// schedule tracks one refreshed data set: its last success and the earliest next attempt.
type schedule struct{ ok, next time.Time }

// target is the state kept between polls of one switch. The poller serializes polls of one
// target, so its fields need no lock of their own. It lives in memory only, so each start of
// netplane takes a new login slot; the old one ages out on the switch (session_timeout).
type target struct {
	base     string    // the endpoint this state belongs to
	password string    // the password last used
	session  string    // "session" cookie value
	jsonNext time.Time // after a global failure, no JSON request (or login) before this

	lldpS, lagS, infoS, fdbS schedule
	lldp                     []lldpNeighbor
	lag                      *lagReply
	info                     *infoReply
	fdb                      []l2Entry
}

// expire drops cached data that has not refreshed for three periods, so a broken login
// cannot keep stale neighbours alive forever.
func (t *target) expire(now time.Time) {
	if now.Sub(t.lldpS.ok) > 3*lldpEvery {
		t.lldp = nil
	}
	if now.Sub(t.lagS.ok) > 3*lldpEvery {
		t.lag = nil
	}
	if now.Sub(t.infoS.ok) > 3*infoEvery {
		t.info = nil
	}
	if now.Sub(t.fdbS.ok) > 3*fdbEvery {
		t.fdb, t.fdbS.ok = nil, time.Time{}
	}
}

// New returns a new RTLPlayground switch driver.
func New() *Driver { return &Driver{targets: map[string]*target{}} }

// Kind returns "rtlplayground".
func (d *Driver) Kind() string { return "rtlplayground" }

// MinPollInterval keeps scheduled polls of this weak device at least 30 seconds apart.
func (d *Driver) MinPollInterval() time.Duration { return minPollInterval }

// ValidateConfig checks required RTLPlayground target settings.
func (d *Driver) ValidateConfig(cfg driver.TargetConfig) error {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("rtlplayground target %q: endpoint is required", cfg.ID)
	}
	if _, err := metricsURL(cfg); err != nil {
		return fmt.Errorf("rtlplayground target %q: %w", cfg.ID, err)
	}
	if cfg.ResolvedSecrets["password"] != "" {
		// The firmware has no TLS, so a password always needs allow_insecure_http.
		return driver.CheckCredentialTransport(cfg)
	}
	return nil
}

// metricsURL is the metrics_url setting, else the endpoint's host on the exporter port.
func metricsURL(cfg driver.TargetConfig) (string, error) {
	if raw := cfg.StringSetting("metrics_url", ""); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("invalid metrics_url %q", raw)
		}
		if u.User != nil {
			// The exporter is public; credentials here would only travel in clear text.
			return "", errors.New("metrics_url must not contain credentials")
		}
		return raw, nil
	}
	u, err := url.Parse(strings.TrimRight(cfg.Endpoint, "/"))
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid endpoint %q", cfg.Endpoint)
	}
	return "http://" + net.JoinHostPort(u.Hostname(), defaultMetricsPort) + "/metrics", nil
}

func (d *Driver) state(id string) *target {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.targets[id]
	if t == nil {
		t = &target{}
		d.targets[id] = t
	}
	return t
}

// Poll scrapes /metrics and, when a password is configured, refreshes the slow JSON data.
func (d *Driver) Poll(ctx context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	mURL, _ := metricsURL(cfg)
	c := newClient(cfg)
	defer c.http.CloseIdleConnections()

	samples, err := c.scrape(ctx, mURL)
	if err != nil {
		return nil, fmt.Errorf("rtlplayground metrics (is `prometheus on` set on the switch?): %w", err)
	}

	st := d.state(cfg.ID)
	now := time.Now()
	switch {
	case c.base != st.base || (c.password == "" && st.password != ""):
		// Another switch, or the password removed: start over. A dropped session keeps its
		// slot on the switch until it ages out (the firmware has no logout).
		*st = target{base: c.base}
	case c.password != st.password:
		st.jsonNext = time.Time{} // a corrected password need not wait out the old pause
	}
	st.password = c.password
	if c.password != "" && !now.Before(st.jsonNext) {
		// Best effort: a failure keeps that data set's cache. A refused login, a rejected
		// session or a transport failure (the web server's only slot busy or dead) pauses all
		// JSON work for retryAfter; an HTTP or parse error backs off only its own data set,
		// by half its period and at least retryAfter.
		refresh := func(s *schedule, every time.Duration, fetch func() error) bool {
			if now.Before(s.next) {
				return true
			}
			err := fetch()
			done := time.Now() // back-offs run from the failure, not from before a long walk
			switch {
			case err == nil:
				s.ok, s.next = done, done.Add(every)
			case ctx.Err() != nil:
				return false // cancelled by the caller, not the device's fault
			case errors.Is(err, errLoginRefused), errors.Is(err, errSessionRejected), errors.As(err, new(transportError)):
				st.jsonNext = done.Add(retryAfter)
				return false
			default:
				s.next = done.Add(max(retryAfter, every/2))
			}
			return true
		}
		_ = refresh(&st.lldpS, lldpEvery, func() error { return c.fetchLLDP(ctx, st) }) &&
			refresh(&st.lagS, lldpEvery, func() error { return c.fetchLAG(ctx, st) }) &&
			refresh(&st.infoS, infoEvery, func() error { return c.fetchInfo(ctx, st) }) &&
			refresh(&st.fdbS, fdbEvery, func() error { return c.fetchFDB(ctx, st) })
	}
	// Expire against the time the snapshot is built: the JSON work above can take a while.
	built := time.Now()
	st.expire(built)
	return buildSnapshot(cfg, samples, st, built), nil
}

// client sends one request at a time to one switch.
type client struct {
	http     *http.Client
	base     string
	password string
	sent     bool
}

func newClient(cfg driver.TargetConfig) *client {
	hc := driver.NewHTTPClient(cfg, requestTimeout)
	if tr, ok := hc.Transport.(*http.Transport); ok {
		tr.DisableKeepAlives = true // the firmware closes after every reply anyway
		tr.MaxConnsPerHost = 1
	}
	// /login answers with a 302 whose cookie is the result; never follow it.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client{http: hc, base: strings.TrimRight(cfg.Endpoint, "/"), password: cfg.ResolvedSecrets["password"]}
}

// do sends req after the pacing gap and returns the body, limited to limit bytes.
func (c *client) do(ctx context.Context, req *http.Request, limit int64) (*http.Response, []byte, error) {
	if c.sent {
		if err := driver.Sleep(ctx, requestGap); err != nil {
			return nil, nil, err
		}
	}
	c.sent = true
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return nil, nil, transportError{err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, transportError{err} // a reply stalled or cut mid-body
	}
	if int64(len(body)) > limit {
		return nil, nil, fmt.Errorf("response body exceeds %d-byte limit", limit)
	}
	return resp, body, nil
}

// transportError marks a failure to exchange a request with the switch at all, as opposed
// to an HTTP status or a malformed reply.
type transportError struct{ err error }

func (e transportError) Error() string { return e.err.Error() }
func (e transportError) Unwrap() error { return e.err }

// scrape reads /metrics, retrying a fast failure once: a busy exporter slot (a Prometheus
// scrape) drops the connection, and two failed polls would mark a healthy switch offline.
func (c *client) scrape(ctx context.Context, u string) ([]sample, error) {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			if serr := driver.Sleep(ctx, scrapeRetryGap); serr != nil {
				return nil, serr
			}
		}
		var req *http.Request
		if req, err = http.NewRequest(http.MethodGet, u, nil); err != nil {
			return nil, err
		}
		var resp *http.Response
		var body []byte
		if resp, body, err = c.do(ctx, req, 256<<10); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return nil, err // a dead switch: one timeout is enough
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		samples := parsePromText(body)
		if !hasPortSample(samples) {
			// A 200 that is not this exporter (an HTML page, a cut reply) must not replace the
			// last snapshot with a switch that has no ports.
			err = errors.New("reply has no switch port samples")
			continue
		}
		return samples, nil
	}
	return nil, err
}

func hasPortSample(samples []sample) bool {
	for _, s := range samples {
		if s.name == "node_network_info" || s.name == "node_network_carrier" {
			if _, ok := portOf(s.labels["device"]); ok {
				return true
			}
		}
	}
	return false
}

var (
	errLoginRefused    = errors.New("rtlplayground login refused (wrong password?)")
	errSessionRejected = errors.New("rtlplayground session rejected right after login (sessions evicted?)")
)

// login takes a session. It is only called without a valid session, so the driver holds at
// most one of the firmware's 4 slots at a time.
func (c *client) login(ctx context.Context, st *target) error {
	form := url.Values{"pwd": {c.password}}.Encode()
	req, err := http.NewRequest(http.MethodPost, c.base+"/login", strings.NewReader(form))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _, err := c.do(ctx, req, 4<<10)
	if err != nil {
		return err
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "session" && ck.Value != "" {
			st.session = ck.Value
			return nil
		}
	}
	// A failed login redirects to login.html without a cookie, whatever the status code.
	return errLoginRefused
}

// getJSON fetches an authenticated JSON endpoint, logging in first or again after a 401.
func (c *client) getJSON(ctx context.Context, st *target, path string, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		fresh := st.session == ""
		if fresh {
			if err := c.login(ctx, st); err != nil {
				return err
			}
		}
		req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
		if err != nil {
			return err
		}
		req.AddCookie(&http.Cookie{Name: "session", Value: st.session})
		resp, body, err := c.do(ctx, req, 16<<10)
		if err != nil {
			return err
		}
		switch resp.StatusCode {
		case http.StatusOK:
			return json.Unmarshal(body, out)
		case http.StatusUnauthorized:
			st.session = ""
			if fresh {
				// Rejected right after logging in: another login would only take another slot.
				return fmt.Errorf("rtlplayground %s: %w", path, errSessionRejected)
			}
			// The kept session expired or was evicted: log in again, once.
		default:
			return fmt.Errorf("rtlplayground %s: HTTP %d", path, resp.StatusCode)
		}
	}
	return fmt.Errorf("rtlplayground %s: %w", path, errSessionRejected)
}

type lldpNeighbor struct {
	Port     int    `json:"port"`
	Chassis  string `json:"chassis"`
	PortID   string `json:"portId"`
	SysName  string `json:"sysName"`
	PortDesc string `json:"portDesc"`
	MgmtAddr string `json:"mgmtAddr"`
}

type lagReply struct {
	Lags []struct {
		LagNum        int    `json:"lagNum"`
		Type          string `json:"type"`
		Members       []int  `json:"members"`
		ActiveMembers []int  `json:"active_members"`
	} `json:"lags"`
}

type infoReply struct {
	IPAddress  string `json:"ip_address"`
	MACAddress string `json:"mac_address"`
	Hostname   string `json:"hostname"`
	SwVer      string `json:"sw_ver"`
	HwVer      string `json:"hw_ver"`
	Uptime     string `json:"uptime"` // hex seconds, "0x0001a2b3"
}

type l2Entry struct {
	VLAN string `json:"vlan"` // hex
	MAC  string `json:"mac"`
	Type string `json:"type"` // "l" learned, "s" static
	Port int    `json:"port"` // physical port, 0 = CPU
	Idx  string `json:"idx"`  // hex table index
}

func (c *client) fetchLLDP(ctx context.Context, st *target) error {
	var l struct {
		Neighbors []lldpNeighbor `json:"neighbors"`
	}
	if err := c.getJSON(ctx, st, "/lldp.json", &l); err != nil {
		return err
	}
	st.lldp = l.Neighbors
	if st.lldp == nil {
		st.lldp = []lldpNeighbor{} // fetched, no neighbours: still reports the capability
	}
	return nil
}

func (c *client) fetchLAG(ctx context.Context, st *target) error {
	var lag lagReply
	if err := c.getJSON(ctx, st, "/lag.json", &lag); err != nil {
		return err
	}
	st.lag = &lag
	return nil
}

func (c *client) fetchInfo(ctx context.Context, st *target) error {
	var info infoReply
	if err := c.getJSON(ctx, st, "/information.json", &info); err != nil {
		return err
	}
	st.info = &info
	return nil
}

// fetchFDB walks the forwarding table the way the web UI does: each page starts after the
// last index returned, and the walk ends on an empty page or when an index repeats (the
// firmware's hash walk wraps around). Anything else (a `null` page from a busy RTL836x
// table, an unparsable index, the page cap) fails the walk and keeps the previous table.
func (c *client) fetchFDB(ctx context.Context, st *target) error {
	seen := map[string]bool{}
	var all []l2Entry
	idx := 0
	for page := 0; page < maxFdbPages; page++ {
		var rows *[]l2Entry
		if err := c.getJSON(ctx, st, "/l2.json?idx="+strconv.Itoa(idx), &rows); err != nil {
			return err
		}
		if rows == nil {
			return errors.New("rtlplayground /l2.json: table read failed (null page)")
		}
		if len(*rows) == 0 {
			st.fdb = all
			return nil
		}
		for _, r := range *rows {
			if seen[r.Idx] {
				st.fdb = all // wrapped around: the walk is complete
				return nil
			}
			seen[r.Idx] = true
			all = append(all, r)
		}
		last, err := strconv.ParseUint((*rows)[len(*rows)-1].Idx, 16, 16)
		if err != nil {
			return fmt.Errorf("rtlplayground /l2.json: bad idx: %w", err)
		}
		if int(last)+1 <= idx {
			st.fdb = all // the index went backwards: wrapped
			return nil
		}
		idx = int(last) + 1
	}
	return errors.New("rtlplayground /l2.json: page limit reached")
}

func ifName(port int) string { return "lan" + strconv.Itoa(port) }

// portOf parses the exporter's device="lanN" label.
func portOf(dev string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(dev, "lan"))
	return n, err == nil && strings.HasPrefix(dev, "lan") && n > 0
}

type portData struct {
	seen, carrier, adminDown bool
	alias                    string
	speedMbps                int
	counters                 model.PortCounters
	sfp                      *model.SfpDom
}

type metricsData struct {
	ports                           map[int]*portData
	hostname, release, machine, mac string
	bootTime, nodeTime              float64
	socTemp                         *float64
}

var counterSetters = map[string]func(*model.PortCounters, uint64){
	"node_network_receive_bytes_total":    func(c *model.PortCounters, v uint64) { c.RxBytes = v },
	"node_network_transmit_bytes_total":   func(c *model.PortCounters, v uint64) { c.TxBytes = v },
	"node_network_receive_packets_total":  func(c *model.PortCounters, v uint64) { c.RxPackets = v },
	"node_network_transmit_packets_total": func(c *model.PortCounters, v uint64) { c.TxPackets = v },
	"node_network_receive_errs_total":     func(c *model.PortCounters, v uint64) { c.RxErrors = v },
	"node_network_transmit_errs_total":    func(c *model.PortCounters, v uint64) { c.TxErrors = v },
	"node_network_receive_drop_total":     func(c *model.PortCounters, v uint64) { c.RxDropped = v },
	"node_network_transmit_drop_total":    func(c *model.PortCounters, v uint64) { c.TxDropped = v },
	"node_network_receive_frame_total":    func(c *model.PortCounters, v uint64) { c.CrcErrors = v },
}

func parseMetrics(samples []sample) metricsData {
	m := metricsData{ports: map[int]*portData{}}
	port := func(dev string) *portData {
		n, ok := portOf(dev)
		if !ok {
			return nil
		}
		if m.ports[n] == nil {
			m.ports[n] = &portData{}
		}
		return m.ports[n]
	}
	sfp := func(p *portData) *model.SfpDom {
		if p.sfp == nil {
			p.sfp = &model.SfpDom{Present: true}
		}
		return p.sfp
	}
	for _, s := range samples {
		// Samples without a device label first.
		switch s.name {
		case "node_uname_info":
			m.hostname, m.release, m.machine = s.labels["nodename"], s.labels["release"], s.labels["machine"]
			continue
		case "node_boot_time_seconds":
			m.bootTime = s.value
			continue
		case "node_time_seconds":
			m.nodeTime = s.value
			continue
		case "node_hwmon_temp_celsius":
			chip := s.labels["chip"]
			if chip == "thermal_thermal_zone0" {
				m.socTemp = finite(s.value)
			} else if dev, ok := strings.CutPrefix(chip, "sfp_"); ok {
				if p := port(dev); p != nil {
					sfp(p).TempC = finite(s.value)
				}
			}
			continue
		}
		p := port(s.labels["device"])
		if p == nil {
			continue
		}
		switch s.name {
		case "node_network_info":
			p.seen = true
			p.alias = s.labels["ifalias"]
			p.adminDown = s.labels["operstate"] == "down"
			m.mac = cmp.Or(m.mac, s.labels["address"])
		case "node_network_carrier":
			p.seen = true
			p.carrier = s.value == 1
		case "node_network_speed_bytes":
			if mbps := s.value * 8 / 1e6; mbps > 0 && mbps < 1e7 { // also rejects NaN
				p.speedMbps = int(mbps)
			}
		case "rtlplayground_sfp_info":
			d := sfp(p)
			d.Vendor, d.PartNumber, d.Serial = s.labels["vendor"], s.labels["model"], s.labels["serial"]
		case "rtlplayground_sfp_voltage_volts":
			sfp(p).VoltageV = finite(s.value)
		case "rtlplayground_sfp_tx_bias_amperes":
			sfp(p).BiasMa = finite(s.value * 1000)
		case "rtlplayground_sfp_tx_power_watts":
			sfp(p).TxPowerDbm = dbm(s.value)
		case "rtlplayground_sfp_rx_power_watts":
			sfp(p).RxPowerDbm = dbm(s.value)
		default:
			// Counters are exact 64-bit integers; float64 would round them above 2^53.
			if set, ok := counterSetters[s.name]; ok {
				if v, err := strconv.ParseUint(s.raw, 10, 64); err == nil {
					set(&p.counters, v)
				}
			}
		}
	}
	return m
}

// lagGroups maps the cached LAG reply onto groups and their member ports. A LACP group is up,
// at the summed speed, only through the members LACP actually aggregated.
func lagGroups(devID string, lag *lagReply, ports map[int]*portData) ([]model.LagGroup, map[int]string) {
	lagOf := map[int]string{}
	if lag == nil {
		return nil, lagOf
	}
	var groups []model.LagGroup
	for _, l := range lag.Lags {
		if len(l.Members) == 0 {
			continue
		}
		g := model.LagGroup{DeviceID: devID, ID: "lag" + strconv.Itoa(l.LagNum+1), Mode: l.Type}
		g.Name = g.ID
		carriers := l.Members
		if l.Type == "lacp" {
			carriers = l.ActiveMembers
		}
		for _, n := range l.Members {
			if n == 0 {
				continue // CPU port
			}
			lagOf[n] = g.ID
			g.MemberLinks = append(g.MemberLinks, ifName(n))
			if p := ports[n]; p != nil && p.carrier && slices.Contains(carriers, n) {
				g.LinkUp = true
				g.SpeedMbps += p.speedMbps
			}
		}
		groups = append(groups, g)
	}
	return groups, lagOf
}

func buildSnapshot(cfg driver.TargetConfig, samples []sample, st *target, now time.Time) *driver.Snapshot {
	devID := "rtlplayground:" + cfg.ID
	m := parseMetrics(samples)
	groups, lagOf := lagGroups(devID, st.lag, m.ports)

	nums := make([]int, 0, len(m.ports))
	for n, p := range m.ports {
		if p.seen {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	caps := []model.Capability{model.CapLinks, model.CapFirmwareInfo}
	links := make([]model.Link, 0, len(nums))
	hasSFP := false
	for _, n := range nums {
		p := m.ports[n]
		l := model.Link{
			DeviceID:   devID,
			Index:      n,
			IfName:     ifName(n),
			Label:      p.alias,
			AdminUp:    !p.adminDown,
			LinkUp:     p.carrier,
			OperState:  model.OperFromAdminLink(!p.adminDown, p.carrier),
			Media:      mediaFor(p.speedMbps, p.carrier, p.sfp != nil),
			LagGroupID: lagOf[n],
			SFP:        p.sfp,
			Counters:   &p.counters,
		}
		if p.carrier {
			l.SpeedMbps = p.speedMbps
		}
		hasSFP = hasSFP || p.sfp != nil
		links = append(links, l)
	}

	var sensors []model.SensorReading
	if m.socTemp != nil {
		caps = append(caps, model.CapSensors)
		sensors = append(sensors, model.SensorReading{Name: "asic", Kind: model.SensorTempC, Value: *m.socTemp, Unit: "C", Status: model.AlertOK})
	}
	if hasSFP {
		caps = append(caps, model.CapSfpDom)
	}
	if st.lldp != nil {
		caps = append(caps, model.CapLLDP)
	}
	if st.lag != nil {
		caps = append(caps, model.CapLAG)
	}
	if !st.fdbS.ok.IsZero() {
		caps = append(caps, model.CapFDB)
	}

	var uptime uint64
	if m.bootTime > 0 && m.nodeTime > m.bootTime {
		uptime = uint64(m.nodeTime - m.bootTime)
	}
	var mgmtIP string
	if info := st.info; info != nil {
		mgmtIP = info.IPAddress
		m.hostname = cmp.Or(m.hostname, info.Hostname)
		m.release = cmp.Or(m.release, info.SwVer)
		m.machine = cmp.Or(m.machine, info.HwVer)
		m.mac = cmp.Or(m.mac, info.MACAddress)
		// Without NTP sync the exporter omits boot time: age the cached value. A reboot shows
		// at the next info refresh either way.
		if uptime == 0 && !st.infoS.ok.IsZero() {
			if v, err := strconv.ParseUint(strings.TrimPrefix(info.Uptime, "0x"), 16, 64); err == nil {
				uptime = v + uint64(now.Sub(st.infoS.ok).Seconds())
			}
		}
	}
	var macs []string
	if m.mac != "" {
		macs = []string{m.mac}
	}

	utc := now.UTC()
	snap := &driver.Snapshot{
		TargetID:    cfg.ID,
		CollectedAt: utc,
		Devices: []model.Device{{
			ID:           devID,
			Driver:       "rtlplayground",
			TargetID:     cfg.ID,
			Name:         cmp.Or(cfg.Name, m.hostname, m.machine),
			Hostname:     m.hostname,
			Vendor:       "RTLPlayground",
			Model:        m.machine,
			Role:         model.RoleSwitch,
			PrimaryMAC:   m.mac,
			MACs:         macs,
			MgmtIP:       mgmtIP,
			Firmware:     model.FirmwareInfo{Current: m.release},
			UptimeSec:    uptime,
			Online:       true,
			LastSeen:     utc,
			Capabilities: caps,
			Sensors:      sensors,
		}},
		Links:     links,
		LagGroups: groups,
	}
	for _, n := range st.lldp {
		snap.LLDP = append(snap.LLDP, model.LldpNeighbor{
			DeviceID:      devID,
			LocalLink:     ifName(n.Port),
			ChassisID:     n.Chassis,
			RemotePort:    cmp.Or(n.PortID, n.PortDesc),
			RemoteSysName: n.SysName,
			RemoteMgmtIP:  n.MgmtAddr,
		})
	}
	for _, e := range st.fdb {
		if e.Port == 0 {
			continue // the switch's own CPU port
		}
		vlan, _ := strconv.ParseUint(e.VLAN, 16, 16)
		snap.FDB = append(snap.FDB, model.FdbEntry{DeviceID: devID, LinkName: ifName(e.Port), MAC: e.MAC, VLAN: int(vlan)})
	}
	return snap
}

// finite returns a pointer to v, or nil for NaN and infinities (an unavailable reading).
func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// dbm converts optical power in watts to dBm; zero or negative power has no dBm value.
func dbm(w float64) *float64 {
	if w <= 0 || math.IsNaN(w) || math.IsInf(w, 0) {
		return nil
	}
	return new(10 * math.Log10(w*1000))
}

// mediaFor infers the port media: the exporter reports no PHY type, only SFP modules and speed.
func mediaFor(speedMbps int, up, hasSFP bool) model.MediaType {
	if !hasSFP && !up {
		return model.MediaOther
	}
	return model.MediaForSpeed(speedMbps, hasSFP)
}

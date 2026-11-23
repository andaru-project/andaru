// SPDX-License-Identifier: AGPL-3.0-or-later
package rtlplayground

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

func init() { requestGap, scrapeRetryGap = 0, 0 }

// fakeSwitch serves firmware-shaped replies and records every request.
type fakeSwitch struct {
	mu          sync.Mutex
	paths       []string
	logins      int
	sessions    map[string]bool
	inflight    int
	maxConc     int
	fdb         []string // l2.json rows, as JSON objects, sorted by idx
	fdbWrap     bool     // pages continue from the table start, like the firmware's hash walk
	fdbNull     bool     // answer `null`, like a busy RTL836x table
	failMetrics int      // fail this many /metrics requests first
	metrics     []byte
}

func newFakeSwitch(t *testing.T) (*fakeSwitch, *httptest.Server) {
	t.Helper()
	m, err := os.ReadFile("testdata/metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSwitch{sessions: map[string]bool{}, metrics: m}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSwitch) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.RequestURI())
	f.inflight++
	if f.inflight > f.maxConc {
		f.maxConc = f.inflight
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inflight--; f.mu.Unlock() }()

	if r.URL.Path == "/metrics" {
		f.mu.Lock()
		fail := f.failMetrics > 0
		if fail {
			f.failMetrics--
		}
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(f.metrics)
		return
	}
	if r.URL.Path == "/login" {
		_ = r.ParseForm()
		if r.PostForm.Get("pwd") != "s3cret" {
			w.Header().Set("Location", "login.html")
			w.WriteHeader(http.StatusFound)
			return
		}
		f.mu.Lock()
		f.logins++
		id := fmt.Sprintf("%012x", f.logins)
		if c, err := r.Cookie("session"); err == nil && f.sessions[c.Value] {
			id = c.Value // the firmware reuses a still-valid cookie's slot
		}
		f.sessions[id] = true
		f.mu.Unlock()
		w.Header().Set("Set-Cookie", "session="+id+"; SameSite=Strict")
		w.Header().Set("Location", "index.html")
		w.WriteHeader(http.StatusFound)
		return
	}
	c, err := r.Cookie("session")
	f.mu.Lock()
	ok := err == nil && f.sessions[c.Value]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/lldp.json":
		_, _ = w.Write([]byte(`{"on":true,"tlv":255,"portStatus":[{"port":1,"name":"","permitted":true}],"neighbors":[` +
			`{"port":1,"chassis":"02:00:5e:00:53:aa","portId":"lan4","sysName":"core-sw","sysDesc":"x","portDesc":"","mgmtAddr":"192.0.2.1","caps":4,"pvid":1,"mtu":1522,"agg":0,"mau":30,"aneg":2,"warn":0,"poe":null,"poeClass":null,"poeReq":null,"ttl":118},` +
			`{"port":9,"chassis":"192.0.2.9","portId":"","sysName":"nas","sysDesc":"","portDesc":"eth0","mgmtAddr":"","caps":0,"pvid":1,"mtu":1522,"agg":0,"mau":0,"aneg":0,"warn":0,"poe":null,"poeClass":null,"poeReq":null,"ttl":90}]}`))
	case "/lag.json":
		_, _ = w.Write([]byte(`{"lags":[{"lagNum":0,"type":"static","members":[],"hash":"00000000"},` +
			`{"lagNum":1,"type":"lacp","members":[1,9],"active_members":[1],"valid":true,"aggregator":"000000000000","hash":"0000003f"}],` +
			`"ports":[{"p":1,"lag":1,"a":"3f","pt":"3f","rs":4,"rx":"0053","psys":"000000000000"}]}`))
	case "/information.json":
		_, _ = w.Write([]byte(`{"ip_address":"192.0.2.10","ip_gateway":"192.0.2.1","ip_netmask":"255.255.255.0","syslog_server":"0.0.0.0:514","session_timeout":3600,"ip_source":"static","mac_address":"02:00:5e:00:53:01","hostname":"switch-test","sw_ver":"v0.1.0","build_date":"2026-11-22 02:16:05","uptime":"0x0000a000","hw_ver":"MokerLink ZX-SWTGW218AS","pcb_ver":"","chip_temp":"56.5 C","flash_size":"2 MB","prometheus":true}`))
	case "/l2.json":
		idx, _ := strconv.Atoi(r.URL.Query().Get("idx"))
		f.mu.Lock()
		rows, wrap, null := f.fdb, f.fdbWrap, f.fdbNull
		f.mu.Unlock()
		if null {
			_, _ = w.Write([]byte("null"))
			return
		}
		var page []string
		start := len(rows)
		for i, row := range rows {
			var e l2Entry
			_ = json.Unmarshal([]byte(row), &e)
			if n, _ := strconv.ParseUint(e.Idx, 16, 16); int(n) >= idx {
				start = i
				break
			}
		}
		for i := 0; i < 2 && len(rows) > 0; i++ {
			j := start + i
			if j >= len(rows) {
				if !wrap {
					break
				}
				j %= len(rows)
			}
			page = append(page, rows[j])
		}
		_, _ = w.Write([]byte("[" + strings.Join(page, ",") + "]"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeSwitch) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

func cfgFor(srv *httptest.Server, password string) driver.TargetConfig {
	cfg := driver.TargetConfig{
		ID:       "sw1",
		Driver:   "rtlplayground",
		Endpoint: srv.URL,
		Settings: map[string]any{"metrics_url": srv.URL + "/metrics", "allow_insecure_http": true},
	}
	if password != "" {
		cfg.ResolvedSecrets = map[string]string{"password": password}
	}
	return cfg
}

func TestMetricsOnlyPoll(t *testing.T) {
	f, srv := newFakeSwitch(t)
	snap, err := New().Poll(context.Background(), cfgFor(srv, ""))
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if n := len(f.paths); n != 1 || f.paths[0] != "/metrics" {
		t.Fatalf("without a password only /metrics may be fetched, got %v", f.paths)
	}
	snap.Normalize()
	d := snap.Devices[0]
	if d.ID != "rtlplayground:sw1" || d.Hostname != "switch-test" || d.Model != "MokerLink ZX-SWTGW218AS" ||
		d.Firmware.Current != "v0.1.0-fe02652c" || d.PrimaryMAC != "02:00:5e:00:53:01" || d.UptimeSec != 40981 {
		t.Fatalf("device = %+v", d)
	}
	if len(d.Sensors) != 1 || d.Sensors[0].Value != 56.546 || d.Sensors[0].Status != model.AlertOK || d.Vendor != "RTLPlayground" {
		t.Fatalf("sensors = %+v", d.Sensors)
	}
	if len(snap.Links) != 4 {
		t.Fatalf("links = %d", len(snap.Links))
	}
	l1, l2, l3, l9 := snap.Links[0], snap.Links[1], snap.Links[2], snap.Links[3]
	if l1.IfName != "lan1" || !l1.LinkUp || l1.SpeedMbps != 2500 || l1.Media != model.Media25GE || l1.Label != `uplink "core"` ||
		l1.Counters.RxBytes != 1850697659 || l1.Counters.RxErrors != 3 {
		t.Fatalf("lan1 = %+v counters %+v", l1, l1.Counters)
	}
	if l2.OperState != model.OperNoCarrier || !l2.AdminUp || l2.SpeedMbps != 0 {
		t.Fatalf("lan2 = %+v", l2)
	}
	if l3.OperState != model.OperDisabled || l3.AdminUp {
		t.Fatalf("lan3 = %+v", l3)
	}
	if l9.Counters.RxBytes != 9007199254740993 {
		t.Fatalf("counter not exact: %d", l9.Counters.RxBytes)
	}
	s := l9.SFP
	if s == nil || l9.Media != model.MediaSFPPl || !l9.IsFiber || s.Vendor != "10Gtek" || s.Serial != "TEST0001" ||
		*s.TempC != 41.25 || *s.VoltageV != 3.3 || math.Abs(*s.BiasMa-6.5) > 1e-9 ||
		math.Abs(*s.TxPowerDbm-(-3.0103)) > 1e-3 || s.RxPowerDbm != nil {
		t.Fatalf("lan9 sfp = %+v", s)
	}
	if len(snap.LLDP) != 0 || len(snap.FDB) != 0 || len(snap.LagGroups) != 0 {
		t.Fatal("no login data expected without a password")
	}
}

func TestPollWithPasswordRefreshesSlowDataOnSchedule(t *testing.T) {
	f, srv := newFakeSwitch(t)
	f.fdb = []string{
		`{"vlan":"001","mac":"02:00:5e:00:53:10","type":"l","port":2,"lag":0,"idx":"0010"}`,
		`{"vlan":"00a","mac":"02:00:5e:00:53:11","type":"l","port":1,"lag":2,"idx":"0020"}`,
		`{"vlan":"001","mac":"02:00:5e:00:53:01","type":"s","port":0,"lag":0,"idx":"0030"}`,
	}
	drv := New()
	cfg := cfgFor(srv, "s3cret")
	snap, err := drv.Poll(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	snap.Normalize()
	if snap.Devices[0].MgmtIP != "192.0.2.10" {
		t.Fatalf("mgmt ip = %q", snap.Devices[0].MgmtIP)
	}
	if len(snap.LLDP) != 2 || snap.LLDP[0].LocalLink != "lan1" || snap.LLDP[0].ChassisID != "02:00:5e:00:53:aa" ||
		snap.LLDP[0].RemotePort != "lan4" || snap.LLDP[0].RemoteMgmtIP != "192.0.2.1" || snap.LLDP[1].RemotePort != "eth0" {
		t.Fatalf("lldp = %+v", snap.LLDP)
	}
	if len(snap.LagGroups) != 1 || snap.LagGroups[0].ID != "lag2" || snap.LagGroups[0].Mode != "lacp" ||
		len(snap.LagGroups[0].MemberLinks) != 2 || !snap.LagGroups[0].LinkUp || snap.LagGroups[0].SpeedMbps != 2500 {
		t.Fatalf("lags = %+v", snap.LagGroups)
	}
	if snap.Links[0].LagGroupID != "lag2" {
		t.Fatalf("lan1 lag = %q", snap.Links[0].LagGroupID)
	}
	// Two pages of two and one; the CPU-port entry is dropped.
	if len(snap.FDB) != 2 || snap.FDB[0].LinkName != "lan2" || snap.FDB[1].VLAN != 10 {
		t.Fatalf("fdb = %+v", snap.FDB)
	}
	if f.count("/l2.json") != 3 { // idx=0, idx=33, idx=49 (empty)
		t.Fatalf("l2 pages = %v", f.paths)
	}

	// A second poll inside the refresh periods fetches only /metrics and replays the cache.
	before := len(f.paths)
	snap2, err := drv.Poll(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.paths[before:]; len(got) != 1 || got[0] != "/metrics" {
		t.Fatalf("second poll requests = %v", got)
	}
	if len(snap2.LLDP) != 2 || len(snap2.FDB) != 2 || len(snap2.LagGroups) != 1 {
		t.Fatal("cached slow data not replayed")
	}
	if f.logins != 1 {
		t.Fatalf("logins = %d, want 1", f.logins)
	}
	if f.maxConc != 1 {
		t.Fatalf("concurrent requests = %d, want 1", f.maxConc)
	}
}

func TestSessionEvictionReloginsOnce(t *testing.T) {
	f, srv := newFakeSwitch(t)
	drv := New()
	cfg := cfgFor(srv, "s3cret")
	if _, err := drv.Poll(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	st := drv.state("sw1")
	// The firmware evicted our session; force a refresh.
	f.mu.Lock()
	f.sessions = map[string]bool{}
	f.mu.Unlock()
	st.lldpS.next = time.Time{}
	if _, err := drv.Poll(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 || st.session == "" {
		t.Fatalf("logins = %d session = %q", f.logins, st.session)
	}
}

func TestWrongPasswordKeepsMetrics(t *testing.T) {
	_, srv := newFakeSwitch(t)
	snap, err := New().Poll(context.Background(), cfgFor(srv, "wrong"))
	if err != nil {
		t.Fatalf("a refused login must not fail the poll: %v", err)
	}
	if len(snap.Links) != 4 || len(snap.LLDP) != 0 {
		t.Fatalf("links=%d lldp=%d", len(snap.Links), len(snap.LLDP))
	}
}

func TestStaleCacheExpires(t *testing.T) {
	st := &target{lldp: []lldpNeighbor{{Port: 1}}, lag: &lagReply{}, info: &infoReply{}, fdb: []l2Entry{{Port: 1}}}
	now := time.Now()
	st.lldpS.ok, st.infoS.ok, st.fdbS.ok = now.Add(-time.Minute), now.Add(-time.Hour), now.Add(-time.Hour)
	st.expire(now)
	if st.lldp == nil || st.info != nil || st.fdb != nil || !st.fdbS.ok.IsZero() {
		t.Fatalf("expire: lldp=%v info=%v fdb=%v", st.lldp, st.info, st.fdb)
	}
}

func TestValidateConfig(t *testing.T) {
	drv := New()
	if err := drv.ValidateConfig(driver.TargetConfig{ID: "r1"}); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	cfg := driver.TargetConfig{ID: "r1", Endpoint: "http://192.0.2.10", ResolvedSecrets: map[string]string{"password": "x"}}
	if err := drv.ValidateConfig(cfg); err == nil {
		t.Fatal("password over plain http accepted without allow_insecure_http")
	}
	cfg.Settings = map[string]any{"allow_insecure_http": true}
	if err := drv.ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if u, _ := metricsURL(cfg); u != "http://192.0.2.10:9100/metrics" {
		t.Fatalf("metrics url = %q", u)
	}
	if drv.MinPollInterval() < 30*time.Second {
		t.Fatal("min poll interval too short")
	}
}

func TestParsePromText(t *testing.T) {
	in := "# HELP x\nfoo{a=\"q\\\"x\\\\y\\nz\",b=\"\"} 1.5 1700000000\nbar 7\nbroken{a=\"x} 1\nbaz{} NaN\ncut 185"
	got := parsePromText([]byte(in))
	if len(got) != 3 || got[0].labels["a"] != "q\"x\\y\nz" || got[0].value != 1.5 || got[1].name != "bar" || !math.IsNaN(got[2].value) {
		t.Fatalf("got %v", got)
	}
}

func TestWrongPasswordBacksOff(t *testing.T) {
	f, srv := newFakeSwitch(t)
	drv := New()
	cfg := cfgFor(srv, "wrong")
	for i := 0; i < 3; i++ {
		if _, err := drv.Poll(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count("/login"); n != 1 {
		t.Fatalf("refused logins over 3 polls = %d, want 1 (stop the poll, then back off)", n)
	}
}

func TestFdbWalkWrapsAndNullPageKeepsTable(t *testing.T) {
	f, srv := newFakeSwitch(t)
	f.fdbWrap = true
	f.fdb = []string{
		`{"vlan":"001","mac":"02:00:5e:00:53:10","type":"l","port":2,"lag":0,"idx":"0010"}`,
		`{"vlan":"001","mac":"02:00:5e:00:53:11","type":"l","port":3,"lag":0,"idx":"0020"}`,
		`{"vlan":"001","mac":"02:00:5e:00:53:12","type":"l","port":4,"lag":0,"idx":"0030"}`,
	}
	drv := New()
	cfg := cfgFor(srv, "s3cret")
	snap, err := drv.Poll(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.FDB) != 3 || f.count("/l2.json") != 2 {
		t.Fatalf("fdb = %d entries in %d pages, want 3 in 2", len(snap.FDB), f.count("/l2.json"))
	}

	// A busy table answers `null`: the walk fails and the previous table stays.
	f.mu.Lock()
	f.fdbNull = true
	f.mu.Unlock()
	st := drv.state("sw1")
	st.lldpS.next, st.infoS.next, st.fdbS.next = time.Time{}, time.Time{}, time.Time{}
	snap, err = drv.Poll(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(st.fdbS.next); len(snap.FDB) != 3 || !st.jsonNext.IsZero() || d < fdbEvery/2-time.Second || d > fdbEvery/2 {
		t.Fatalf("after null page: fdb=%d jsonNext=%v fdb backoff=%v, want %v", len(snap.FDB), st.jsonNext, d, fdbEvery/2)
	}
}

func TestScrapeRetriesOnce(t *testing.T) {
	f, srv := newFakeSwitch(t)
	f.failMetrics = 1
	if _, err := New().Poll(context.Background(), cfgFor(srv, "")); err != nil {
		t.Fatalf("one busy scrape must be retried: %v", err)
	}
	f.failMetrics = 2
	if _, err := New().Poll(context.Background(), cfgFor(srv, "")); err == nil {
		t.Fatal("two failed scrapes must fail the poll")
	}
}

func TestClientIsGentle(t *testing.T) {
	c := newClient(driver.TargetConfig{Endpoint: "http://192.0.2.10"})
	tr := c.http.Transport.(*http.Transport)
	if !tr.DisableKeepAlives || tr.MaxConnsPerHost != 1 {
		t.Fatalf("transport keepalive=%v maxconns=%d", !tr.DisableKeepAlives, tr.MaxConnsPerHost)
	}
	_, srv := newFakeSwitch(t)
	defer func(g time.Duration) { requestGap = g }(requestGap)
	requestGap = 30 * time.Millisecond
	c = newClient(driver.TargetConfig{Endpoint: srv.URL})
	start := time.Now()
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
		if _, _, err := c.do(context.Background(), req, 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Fatalf("3 requests took %v, want at least two pacing gaps", el)
	}
}

func TestFailingDataSetDoesNotBlockOthersAndPasswordRemovalClears(t *testing.T) {
	f, _ := newFakeSwitch(t)
	f.fdb = []string{`{"vlan":"001","mac":"02:00:5e:00:53:10","type":"l","port":2,"lag":0,"idx":"0010"}`}
	breakLag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lag.json" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.serve(w, r)
	}))
	defer breakLag.Close()
	drv := New()
	cfg := cfgFor(breakLag, "s3cret")
	snap, err := drv.Poll(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(drv.state("sw1").lagS.next); d < max(retryAfter, lldpEvery/2)-time.Second {
		t.Fatalf("lag backoff = %v", d)
	}
	// LLDP, info and the FDB all refresh although /lag.json keeps failing.
	if snap.Devices[0].MgmtIP == "" || len(snap.FDB) != 1 || len(snap.LLDP) != 2 || len(snap.LagGroups) != 0 {
		t.Fatalf("ip=%q fdb=%d lldp=%d lags=%d", snap.Devices[0].MgmtIP, len(snap.FDB), len(snap.LLDP), len(snap.LagGroups))
	}

	cfg.ResolvedSecrets = nil
	snap, err = drv.Poll(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Devices[0].MgmtIP != "" || len(snap.FDB) != 0 || drv.state("sw1").session != "" {
		t.Fatal("removing the password must drop the session and cached data")
	}
}

func TestUnreachableWebServerPausesAllJSON(t *testing.T) {
	_, srv := newFakeSwitch(t)
	cfg := cfgFor(srv, "s3cret")
	dead := httptest.NewServer(http.NotFoundHandler())
	cfg.Endpoint = dead.URL
	dead.Close() // refused: the web server is down, the exporter answers
	drv := New()
	if _, err := drv.Poll(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	st := drv.state("sw1")
	if d := time.Until(st.jsonNext); d < retryAfter-time.Second || !st.lldpS.next.IsZero() || !st.infoS.next.IsZero() {
		t.Fatalf("a transport failure must pause all JSON work before info is tried: jsonNext=%v infoNext=%v", st.jsonNext, st.infoS.next)
	}
}

func TestPasswordChangeLiftsPause(t *testing.T) {
	_, srv := newFakeSwitch(t)
	drv := New()
	if _, err := drv.Poll(context.Background(), cfgFor(srv, "wrong")); err != nil {
		t.Fatal(err)
	}
	if drv.state("sw1").jsonNext.IsZero() {
		t.Fatal("expected a pause after a refused login")
	}
	snap, err := drv.Poll(context.Background(), cfgFor(srv, "s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.LLDP) == 0 {
		t.Fatal("a corrected password must not wait out the old pause")
	}
}

func TestUptimeAgesWithoutNTP(t *testing.T) {
	m, err := os.ReadFile("testdata/metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(m), "\n") {
		if !strings.HasPrefix(l, "node_boot_time_seconds") {
			lines = append(lines, l)
		}
	}
	now := time.Now()
	st := &target{info: &infoReply{Uptime: "0x00000064"}}
	st.infoS.ok = now.Add(-30 * time.Second)
	snap := buildSnapshot(driver.TargetConfig{ID: "sw1"}, parsePromText([]byte(strings.Join(lines, "\n"))), st, now)
	if got := snap.Devices[0].UptimeSec; got != 130 {
		t.Fatalf("uptime = %d, want 100 + 30", got)
	}
	st.infoS.ok = time.Time{}
	if got := buildSnapshot(driver.TargetConfig{ID: "sw1"}, nil, st, now).Devices[0].UptimeSec; got != 0 {
		t.Fatalf("uptime without a refresh time = %d, want 0", got)
	}
}

func TestFreshSessionRejectedLogsInOnce(t *testing.T) {
	f, _ := newFakeSwitch(t)
	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".json") {
			f.mu.Lock()
			f.paths = append(f.paths, r.URL.Path)
			f.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.serve(w, r)
	}))
	defer reject.Close()
	drv := New()
	if _, err := drv.Poll(context.Background(), cfgFor(reject, "s3cret")); err != nil {
		t.Fatal(err)
	}
	if f.logins != 1 || drv.state("sw1").jsonNext.IsZero() {
		t.Fatalf("logins = %d, want 1 and a pause", f.logins)
	}
}

func TestEndpointChangeResetsState(t *testing.T) {
	_, srv := newFakeSwitch(t)
	drv := New()
	if _, err := drv.Poll(context.Background(), cfgFor(srv, "s3cret")); err != nil {
		t.Fatal(err)
	}
	_, srv2 := newFakeSwitch(t)
	cfg := cfgFor(srv2, "s3cret")
	cfg.Settings["metrics_url"] = srv2.URL + "/metrics"
	st := drv.state("sw1")
	old := st.session
	if _, err := drv.Poll(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if st.base != srv2.URL || st.session == "" || (st.session == old && st.lldpS.ok.IsZero()) {
		t.Fatalf("state not rebuilt for the new endpoint: base=%q", st.base)
	}
}

func TestScrapeWithoutPortSamplesFails(t *testing.T) {
	f, srv := newFakeSwitch(t)
	f.metrics = []byte("<html><body>status page</body></html>\n")
	if _, err := New().Poll(context.Background(), cfgFor(srv, "")); err == nil {
		t.Fatal("a reply without switch port samples must fail the poll")
	}
}

func TestMetricsURLRejectsCredentials(t *testing.T) {
	cfg := driver.TargetConfig{ID: "r1", Endpoint: "http://192.0.2.10", Settings: map[string]any{"metrics_url": "http://u:p@192.0.2.10:9100/metrics"}}
	if err := New().ValidateConfig(cfg); err == nil {
		t.Fatal("metrics_url with credentials accepted")
	}
}

func TestNonFiniteReadingsDropped(t *testing.T) {
	in := "node_hwmon_temp_celsius{chip=\"thermal_thermal_zone0\",sensor=\"temp1\"} NaN\n" +
		"node_network_carrier{device=\"lan9\"} 1\n" +
		"rtlplayground_sfp_voltage_volts{device=\"lan9\"} +Inf\n"
	m := parseMetrics(parsePromText([]byte(in)))
	if m.socTemp != nil || m.ports[9].sfp == nil || m.ports[9].sfp.VoltageV != nil {
		t.Fatalf("non-finite readings kept: temp=%v sfp=%+v", m.socTemp, m.ports[9].sfp)
	}
}

func TestMediaFor(t *testing.T) {
	// The speed is recorded whether or not a port has carrier, so a down copper port is other.
	for _, tc := range []struct {
		speed      int
		up, hasSFP bool
		want       model.MediaType
	}{
		{1000, true, false, model.MediaGE}, {1000, false, false, model.MediaOther}, {0, true, false, model.MediaOther},
		{10000, false, true, model.MediaSFPPl}, {1000, false, true, model.MediaSFP},
	} {
		if got := mediaFor(tc.speed, tc.up, tc.hasSFP); got != tc.want {
			t.Errorf("mediaFor(%d, %v, %v) = %s, want %s", tc.speed, tc.up, tc.hasSFP, got, tc.want)
		}
	}
}

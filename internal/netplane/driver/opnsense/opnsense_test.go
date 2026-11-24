// SPDX-License-Identifier: AGPL-3.0-or-later
package opnsense

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// fixed is the test clock: fixture times are relative to it.
var fixed = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func testDriver() *Driver {
	d := New()
	d.now = func() time.Time { return fixed }
	return d
}

func target(url string) driver.TargetConfig {
	return driver.TargetConfig{
		ID:              "opn",
		Driver:          "opnsense",
		Endpoint:        url,
		Settings:        map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"api_key": "k", "api_secret": "s"},
	}
}

// fixtureServer answers with routes[path]; a missing route is a 404 like OPNsense's own.
func fixtureServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "k" || p != "s" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":401,"message":"Authentication Failed"}`))
			return
		}
		body, ok := routes[r.URL.Path]
		switch {
		case !ok:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errorMessage":"Endpoint not found"}`))
		case body == "403":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":403,"message":"Forbidden"}`))
		case body == "500":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errorMessage":"configd unavailable","errorTitle":"Error"}`))
		default:
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func baseRoutes() map[string]string {
	hs := strconv.FormatInt(fixed.Unix()-30, 10)
	exp := strconv.FormatInt(fixed.Unix()+86400, 10)
	return map[string]string{
		"/api/diagnostics/system/system_information": `{"name":"fw.example.lan","versions":["OPNsense 26.7.9-amd64","FreeBSD 14.3-RELEASE-p5"]}`,
		"/api/diagnostics/system/system_time":        `{"uptime":"12 days, 03:04:05","loadavg":"0.21, 0.30, 0.28"}`,
		"/api/diagnostics/system/system_resources":   `{"memory":{"total":"8589934592","used":1234567890}}`,
		"/api/interfaces/overview/interfaces_info/1": `{"total":5,"rowCount":5,"current":1,"rows":[
			{"device":"igc0","identifier":"wan","description":"WAN","enabled":true,"status":"up","flags":["up","running"],"mtu":"1500","macaddr":"00:0d:b9:00:00:01","macaddr_hw":"00:0d:b9:00:00:01","media":"1000baseT <full-duplex>","is_physical":true,"addr4":"198.51.100.2/24","ipv4":[{"ipaddr":"198.51.100.2/24"}],"ipv6":[],"vlan":null,
			 "statistics":{"bytes received":"9000","bytes transmitted":"5000","packets received":"90","packets transmitted":"50","input errors":"1","output errors":"2","input queue drops":"3"}},
			{"device":"igc1","identifier":"lan","description":"LAN","enabled":true,"status":"up","flags":["up","running"],"mtu":"1500","macaddr":"00:0d:b9:00:00:02","media":"10Gbase-SR <full-duplex>","is_physical":true,"addr4":"192.0.2.1/24","ipv4":[{"ipaddr":"192.0.2.1/24"}],"ipv6":[{"ipaddr":"fe80::1%igc1/64"}]},
			{"device":"igc2","identifier":"","description":"Unassigned Interface","status":"no carrier","flags":["up"],"mtu":"1500","macaddr":"00:0d:b9:00:00:03","media":"autoselect","is_physical":true},
			{"device":"vlan01","identifier":"opt1","description":"IoT","enabled":false,"status":"up","flags":["up","running"],"mtu":"1500","macaddr":"00:0d:b9:00:00:02","is_physical":false,"ipv4":[{"ipaddr":"10.20.0.1/24"}],"vlan":{"tag":"20","proto":"802.1q","parent":"igc1"}},
			{"device":"lagg0","identifier":"opt2","description":"Uplink","enabled":true,"status":"up","flags":["up","running"],"mtu":"9000","macaddr":"00:0d:b9:00:00:04","is_physical":false,"ipv4":[]}
		]}`,
		"/api/routes/gateway/status": `{"items":[
			{"name":"WAN_DHCP","address":"198.51.100.1","status":"none","loss":"0.0 %","delay":"4.2 ms","monitor":"1.1.1.1"},
			{"name":"WAN6","address":"2001:db8::1","status":"down","loss":"~","delay":"~","monitor":"~"},
			{"name":"LTE","address":"~","status":"delay","loss":"0.0 %","delay":"180.5 ms","monitor":"2001:db8:9::1"}],"status":"ok"}`,
		"/api/diagnostics/interface/search_arp": `{"rows":[{"mac":"DE:AD:BE:EF:00:01","ip":"192.0.2.50","intf":"igc1","expired":false,"permanent":false},{"mac":"00:0d:b9:00:00:02","ip":"192.0.2.1","intf":"igc1","permanent":true}]}`,
		"/api/diagnostics/interface/search_ndp": `{"rows":[{"mac":"de:ad:be:ef:00:01","ip":"fe80::50%igc1","intf":"igc1"}]}`,
		"/api/kea/leases4/search": `{"rows":[{"address":"192.0.2.50","hwaddr":"de:ad:be:ef:00:01","hostname":"nas","expire":"` + exp + `","if_name":"lan","state":"0"},
			{"address":"192.0.2.51","hwaddr":"de:ad:be:ef:00:09","hostname":"gone","expire":"` + exp + `","if_name":"lan","state":"2"},
			{"address":"192.0.2.52","hwaddr":"de:ad:be:ef:00:0a","hostname":"deleted","expire":"1000","if_name":"lan","state":"0"}]}`,
		"/api/dnsmasq/leases/search": `{"rows":[{"address":"192.0.2.60","hwaddr":"de:ad:be:ef:00:02","hostname":"tv","expire":` + exp + `,"if_name":"lan"},
			{"address":"2001:db8::60","hwaddr":"de:ad:be:ef:00:02","hostname":"tv","expire":` + exp + `,"if_name":"lan"},
			{"address":"2001:db8::61","hwaddr":"","hostname":"uuid-client","expire":` + exp + `,"if_name":"lan"}]}`,
		"/api/dhcpv4/leases/search_lease":     `{"rows":[{"address":"192.0.2.70","mac":"de:ad:be:ef:00:03","hostname":"printer","type":"static","if":"lan"}]}`,
		"/api/diagnostics/firewall/pf_states": `{"current":"1234","limit":"812000"}`,
		"/api/wireguard/service/show": `{"rows":[
			{"if":"wg0","type":"interface","public-key":"srv=","status":"up","name":"HomeVPN"},
			{"if":"wg0","type":"peer","public-key":"abc=","endpoint":"203.0.113.5:51820","latest-handshake":` + hs + `,"transfer-rx":1024,"transfer-tx":2048,"name":"phone"},
			{"if":"wg0","type":"peer","public-key":"old=","endpoint":"(none)","latest-handshake":0,"transfer-rx":0,"transfer-tx":0},
			{"if":"wg0","type":"peer","public-key":"idle=","endpoint":"203.0.113.6:51820","latest-handshake":` + strconv.FormatInt(fixed.Unix()-400, 10) + `,"transfer-rx":0,"transfer-tx":0}]}`,
	}
}

func TestOpnsensePoll(t *testing.T) {
	drv := testDriver()
	if err := drv.ValidateConfig(driver.TargetConfig{ID: "x", Endpoint: "https://fw", ResolvedSecrets: map[string]string{"api_key": "k"}}); err == nil {
		t.Fatal("expected an error without api_secret")
	}
	snap, err := drv.Poll(context.Background(), target(fixtureServer(t, baseRoutes()).URL))
	if err != nil {
		t.Fatal(err)
	}
	snap.Normalize()
	if len(snap.Unavailable) != 0 {
		t.Fatalf("unavailable = %v", snap.Unavailable)
	}
	d := snap.Devices[0]
	if d.ID != "opnsense:opn" || d.Hostname != "fw.example.lan" || d.Model != "OPNsense" || d.Firmware.Current != "26.7.9" ||
		d.UptimeSec != 12*86400+3*3600+4*60+5 || d.MgmtIP != "192.0.2.1" || d.PrimaryMAC != "00:0d:b9:00:00:01" ||
		d.SystemStats.MemTotalBytes != 8589934592 || d.SystemStats.MemUsedBytes != 1234567890 || d.SystemStats.Load1 != 0.21 {
		t.Fatalf("device = %+v", d)
	}
	if !slices.Equal(d.MACs, []string{"00:0d:b9:00:00:01", "00:0d:b9:00:00:02", "00:0d:b9:00:00:03"}) {
		t.Fatalf("MACs = %v", d.MACs)
	}
	for _, c := range []model.Capability{model.CapDiagPing, model.CapVPN, model.CapGateway} {
		if !slices.Contains(d.Capabilities, c) {
			t.Errorf("missing capability %s", c)
		}
	}

	// Physical ports only: the VLAN is a segment.
	if len(snap.Links) != 3 {
		t.Fatalf("links = %+v", snap.Links)
	}
	wan, lan, spare := snap.Links[0], snap.Links[1], snap.Links[2]
	if wan.IfName != "igc0" || wan.SpeedMbps != 1000 || wan.Duplex != "full" || wan.Media != model.MediaGE || wan.OperState != model.OperUp ||
		wan.Counters == nil || *wan.Counters != (model.PortCounters{RxBytes: 9000, TxBytes: 5000, RxPackets: 90, TxPackets: 50, RxErrors: 1, TxErrors: 2, RxDropped: 3}) {
		t.Fatalf("wan link = %+v counters %+v", wan, wan.Counters)
	}
	if lan.Media != model.MediaSFPPl || !lan.IsFiber || lan.SpeedMbps != 10000 {
		t.Fatalf("lan link = %+v", lan)
	}
	if spare.OperState != model.OperNoCarrier || spare.SpeedMbps != 0 || spare.Media != model.MediaOther {
		t.Fatalf("unassigned link = %+v", spare)
	}

	if len(snap.Segments) != 4 {
		t.Fatalf("segments = %+v", snap.Segments)
	}
	var iot model.Segment
	for _, s := range snap.Segments {
		if s.ID == "opt1" {
			iot = s
		}
		if (s.ID == "opt2") != (s.Kind == "lag") {
			t.Errorf("segment %s: kind %s", s.ID, s.Kind)
		}
		if s.ID == "lan" && (len(s.Addresses) != 2 || s.Addresses[1].Address != "fe80::1" || s.Addresses[1].PrefixLength != 64) {
			t.Fatalf("lan addresses = %+v", s.Addresses)
		}
	}
	if iot.Kind != "vlan" || iot.ParentLink != "igc1" || iot.VLANID == nil || *iot.VLANID != 20 || iot.OperState != model.OperDisabled {
		t.Fatalf("vlan segment = %+v", iot)
	}

	gws := snap.Gateways
	if len(gws) != 3 || !gws[0].Online || *gws[0].RTTMs != 4.2 || gws[0].GatewayIP != "198.51.100.1" ||
		gws[1].Online || gws[1].RTTMs != nil || gws[1].Family != model.IPv6 || gws[1].MonitorIP != "" ||
		!gws[2].Online || gws[2].GatewayIP != "" || gws[2].Family != model.IPv6 || *gws[2].RTTMs != 180.5 {
		t.Fatalf("gateways = %+v", gws)
	}
	if len(snap.Neighbors) != 3 || snap.Neighbors[0].MAC != "de:ad:be:ef:00:01" || snap.Neighbors[0].SegmentName != "lan" || snap.Neighbors[0].State != "reachable" ||
		snap.Neighbors[1].State != "permanent" || snap.Neighbors[2].IP != "fe80::50" || snap.Neighbors[2].Family != model.IPv6 {
		t.Fatalf("neighbors = %+v", snap.Neighbors)
	}
	// The reclaimed (state 2) and the deleted (expired) Kea leases are left out.
	dh := snap.DHCP
	if len(dh) != 5 || dh[3].MAC != "" || dh[3].IP != "2001:db8::61" || dh[0].Hostname != "nas" || dh[0].ExpiresAt == nil || dh[0].ExpiresAt.Unix() != fixed.Unix()+86400 || dh[0].Pool != "lan" ||
		dh[1].ExpiresAt == nil || dh[1].ExpiresAt.Unix() != fixed.Unix()+86400 || dh[2].Family != model.IPv6 ||
		!dh[4].Static || dh[4].MAC != "de:ad:be:ef:00:03" || dh[0].Static {
		t.Fatalf("dhcp = %+v", dh)
	}
	if fw := snap.Firewalls; len(fw) != 1 || *fw[0].StatesUsed != 1234 || *fw[0].StatesMax != 812000 {
		t.Fatalf("firewalls = %+v", fw)
	}
	v := snap.VPNs
	if len(v) != 3 || !v[0].Up || v[0].Name != "phone" || v[0].RxBytes != 1024 || v[1].Up || v[1].LastHandshake != nil || v[1].Endpoint != "" ||
		v[2].Up || v[2].LastHandshake == nil {
		t.Fatalf("vpns = %+v", v)
	}
}

// On 25.1 the privilege patterns are camelCase, so the snake_case path is refused.
func TestOpnsenseCamelCaseFallback(t *testing.T) {
	r := baseRoutes()
	r["/api/diagnostics/system/systemInformation"] = r["/api/diagnostics/system/system_information"]
	r["/api/diagnostics/system/system_information"] = "403"
	r["/api/diagnostics/system/systemTime"] = r["/api/diagnostics/system/system_time"]
	delete(r, "/api/diagnostics/system/system_time")
	var refused int
	srv := fixtureServer(t, r)
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/diagnostics/system/system_information" {
			refused++
		}
		inner.ServeHTTP(w, req)
	})
	drv := testDriver()
	for range 2 {
		snap, err := drv.Poll(context.Background(), target(srv.URL))
		if err != nil {
			t.Fatal(err)
		}
		if snap.Devices[0].Firmware.Current != "26.7.9" || snap.Devices[0].UptimeSec == 0 {
			t.Fatalf("device = %+v", snap.Devices[0])
		}
	}
	// The second poll goes straight to the spelling that answered.
	if refused != 1 {
		t.Fatalf("refused snake_case requests = %d, want 1", refused)
	}
}

func TestOpnsenseSectionsUnavailableOrAbsent(t *testing.T) {
	r := baseRoutes()
	// No DHCP server or WireGuard plugin installed: empty, not stale.
	delete(r, "/api/kea/leases4/search")
	delete(r, "/api/dnsmasq/leases/search")
	delete(r, "/api/dhcpv4/leases/search_lease")
	delete(r, "/api/wireguard/service/show")
	snap, err := testDriver().Poll(context.Background(), target(fixtureServer(t, r).URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Unavailable) != 0 || len(snap.DHCP) != 0 || len(snap.VPNs) != 0 || slices.Contains(snap.Devices[0].Capabilities, model.CapVPN) {
		t.Fatalf("absent services: unavailable=%v dhcp=%v vpns=%v", snap.Unavailable, snap.DHCP, snap.VPNs)
	}

	// A privilege the API user lacks (403) leaves its section empty too.
	r = baseRoutes()
	for _, p := range []string{"/api/kea/leases4/search", "/api/dnsmasq/leases/search", "/api/diagnostics/interface/search_ndp", "/api/wireguard/service/show"} {
		r[p] = "403"
	}
	snap, err = testDriver().Poll(context.Background(), target(fixtureServer(t, r).URL))
	if err != nil || len(snap.Unavailable) != 0 || len(snap.Neighbors) != 2 || len(snap.DHCP) != 1 {
		t.Fatalf("refused sections: err=%v unavailable=%v neighbors=%d dhcp=%d", err, snap.Unavailable, len(snap.Neighbors), len(snap.DHCP))
	}

	// Any other failure keeps the last data, even when another DHCP server answered.
	r = baseRoutes()
	for _, p := range []string{"/api/dnsmasq/leases/search", "/api/routes/gateway/status", "/api/diagnostics/interface/search_ndp", "/api/diagnostics/firewall/pf_states", "/api/wireguard/service/show"} {
		r[p] = "500"
	}
	snap, err = testDriver().Poll(context.Background(), target(fixtureServer(t, r).URL))
	if err != nil {
		t.Fatal(err)
	}
	want := []driver.Section{driver.SectionGateways, driver.SectionNeighbors, driver.SectionDHCP, driver.SectionFirewalls, driver.SectionVPNs}
	for _, s := range want {
		if !slices.Contains(snap.Unavailable, s) {
			t.Errorf("section %s not marked unavailable: %v", s, snap.Unavailable)
		}
	}
	if !slices.Contains(snap.Devices[0].Capabilities, model.CapVPN) {
		t.Error("a transient WireGuard failure dropped the VPN capability")
	}

	// pf_states answers 200 with result "failed" when configd gave no answer.
	r = baseRoutes()
	r["/api/diagnostics/firewall/pf_states"] = `{"result":"failed"}`
	if snap, err = testDriver().Poll(context.Background(), target(fixtureServer(t, r).URL)); err != nil || len(snap.Firewalls) != 0 || !slices.Contains(snap.Unavailable, driver.SectionFirewalls) {
		t.Fatalf("pf failed: err=%v firewalls=%v unavailable=%v", err, snap.Firewalls, snap.Unavailable)
	}

	// Status "failed" (configd gave no answer) keeps the last gateways.
	r = baseRoutes()
	r["/api/routes/gateway/status"] = `{"items":[],"status":"failed"}`
	if snap, err = testDriver().Poll(context.Background(), target(fixtureServer(t, r).URL)); err != nil || !slices.Equal(snap.Unavailable, []driver.Section{driver.SectionGateways}) {
		t.Fatalf("gateways failed: err=%v unavailable=%v", err, snap.Unavailable)
	}

	// The required calls fail the poll, with the reason.
	r = baseRoutes()
	r["/api/interfaces/overview/interfaces_info/1"] = "403"
	if _, err := testDriver().Poll(context.Background(), target(fixtureServer(t, r).URL)); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want a 403", err)
	}
	cfg := target(fixtureServer(t, baseRoutes()).URL)
	cfg.ResolvedSecrets["api_secret"] = "wrong"
	if _, err := testDriver().Poll(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "Authentication Failed") {
		t.Fatalf("err = %v, want the auth failure", err)
	}
}

func TestParseHelpers(t *testing.T) {
	if p, v := parseVersion([]string{"OPNsense Business 25.10-amd64"}); p != "OPNsense Business" || v != "25.10" {
		t.Errorf("parseVersion = %q %q", p, v)
	}
	for in, want := range map[string]uint64{"1 day, 00:00:01": 86401, "12 Tage, 03:04:05": 12*86400 + 11045, "03:04:05": 11045, "soon": 0} {
		if got := parseUptime(in); got != want {
			t.Errorf("parseUptime(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]string{"pppoe0": "pppoe", "ppp0": "other", "igc0": "ethernet", "wg1": "wireguard"} {
		if got := segmentKind(in); got != want {
			t.Errorf("segmentKind(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]model.MediaType{
		"2500Base-T <full-duplex>":     model.Media25GE,
		"10Gbase-Twinax <full-duplex>": model.MediaSFPPl,
		"1000baseSX <full-duplex>":     model.MediaSFP,
		"100baseTX <half-duplex>":      model.MediaFE,
	} {
		if _, _, got := parseMedia(in); got != want {
			t.Errorf("mediaType(%q) = %s, want %s", in, got, want)
		}
	}
}

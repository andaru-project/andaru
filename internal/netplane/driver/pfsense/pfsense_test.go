// SPDX-License-Identifier: AGPL-3.0-or-later
package pfsense

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

func TestPfSenseDriverPoll(t *testing.T) {
	nowUnix := time.Now().UTC().Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "pf-key-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v2/status/system":
			_, _ = w.Write([]byte(`{"data":{"hostname":"pf-core","version":"2.8.0","uptime":"1 Day 02 Hours 03 Minutes 04 Seconds","cpu_usage":8.5,"temp_c":41.0,"cpu_load_avg":[0.15,0.1,0.05]}}`))
		case "/api/v2/status/interfaces":
			_, _ = w.Write([]byte(`{"data":[
				{"name":"lan","descr":"LAN","hwif":"igc1","enable":true,"status":"up","macaddr":"00:11:22:33:44:02","mtu":"1500","ipaddr":"192.0.2.1","subnet":"24","ipaddrv6":"2001:db8::1","subnetv6":"64"},
				{"name":"wan","descr":"WAN","hwif":"igc0","enable":true,"status":"down","macaddr":"00:11:22:33:44:01","mtu":"1500","ipaddr":"198.51.100.2","subnet":"24","subnetv6":null,"inbytes":90000,"outbytes":50000},
				{"name":"opt1","descr":"OPT1","hwif":"igc0","enable":true,"status":"down","macaddr":"00:11:22:33:44:01","mtu":""}
			]}`))
		case "/api/v2/status/gateways":
			_, _ = w.Write([]byte(`{"data":[{"name":"WAN_DHCP","srcip":"198.51.100.1","monitorip":"1.1.1.1","status":"online","delay":4.2,"loss":0.0,"default":true}]}`))
		case "/api/v2/diagnostics/arp_table":
			_, _ = w.Write([]byte(`{"data":[{"ip":"192.0.2.50","mac":"de:ad:be:ef:00:01","interface":"lan","status":"expires"}]}`))
		case "/api/v2/status/dhcp_server/leases":
			_, _ = w.Write([]byte(`{"data":[{"ip":"192.0.2.50","mac":"de:ad:be:ef:00:01","hostname":"nas","type":"static"}]}`))
		case "/api/v2/status/services":
			_, _ = w.Write([]byte(`{"data":[{"name":"unbound","description":"DNS Resolver","status":true},{"name":"pfblockerng","description":"pfBlockerNG","status":true}]}`))
		case "/api/v2/status/wireguard/peers":
			_, _ = w.Write([]byte(`{"data":[{"tunnel_device":"tun_wg0","descr":"mobile","public_key":"abc=","endpoint":"203.0.113.5:51820","latest_handshake":` + strconv.FormatInt(nowUnix-10, 10) + `,"transfer_rx":1024,"transfer_tx":2048}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	drv := New()
	if err := drv.ValidateConfig(driver.TargetConfig{ID: "pf1"}); err == nil {
		t.Fatal("expected error for missing endpoint")
	}

	snap, err := drv.Poll(context.Background(), driver.TargetConfig{
		ID:              "pf-core",
		Driver:          "pfsense",
		Endpoint:        srv.URL,
		Settings:        map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"api_key": "pf-key-123"},
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	snap.Normalize()
	if len(snap.Devices) != 1 || snap.Devices[0].Role != model.RoleFirewall || snap.Devices[0].MgmtIP != "192.0.2.1" {
		t.Fatalf("unexpected device: %+v", snap.Devices)
	}
	// ID from the target key; PrimaryMAC is the lowest MAC, not the first listed.
	if caps := snap.Devices[0].Capabilities; !slices.Contains(caps, model.CapDiagPing) || slices.Contains(caps, model.CapDiagTraceroute) {
		t.Fatalf("diagnostic capabilities = %v, want ping only", caps)
	}
	if d := snap.Devices[0]; d.ID != "pfsense:pf-core" || d.PrimaryMAC != "00:11:22:33:44:01" || d.UptimeSec != 93784 || d.SystemStats.Load1 != 0.15 {
		t.Fatalf("unexpected device identity/telemetry: %+v", d)
	}
	// igc0 backs both wan and opt1: one link, three segments.
	if len(snap.Links) != 2 || len(snap.Segments) != 3 || len(snap.Gateways) != 1 || !snap.Gateways[0].Online {
		t.Fatalf("unexpected links/segments/gateways: %d/%d/%d", len(snap.Links), len(snap.Segments), len(snap.Gateways))
	}
	if snap.Gateways[0].GatewayIP != "" || snap.Gateways[0].MonitorIP != "1.1.1.1" {
		t.Fatalf("probe source reported as gateway: %+v", snap.Gateways[0])
	}
	lan, wan := snap.Links[0], snap.Links[1]
	if lan.MTU == nil || *lan.MTU != 1500 || snap.Segments[0].Addresses[0].PrefixLength != 24 || snap.Segments[0].Addresses[1].PrefixLength != 64 {
		t.Fatalf("string fields not decoded: %+v %+v", lan, snap.Segments[0])
	}
	if !wan.AdminUp || wan.LinkUp || wan.OperState != model.OperDown || !snap.Segments[1].AdminUp {
		t.Fatalf("enabled-but-down interface misreported: %+v", wan)
	}
	if snap.Segments[2].MTU != nil {
		t.Fatalf("empty mtu should be unset: %+v", snap.Segments[2])
	}
	if len(snap.VPNs) != 1 || !snap.VPNs[0].Up || snap.VPNs[0].ID != "tun_wg0:abc=" {
		t.Fatalf("unexpected VPNs: %+v", snap.VPNs)
	}
	if len(snap.Firewalls) != 1 || len(snap.Firewalls[0].Services) != 2 || snap.Firewalls[0].StatesUsed != nil || snap.Firewalls[0].StatesMax != nil {
		t.Fatalf("unexpected firewall state: %+v", snap.Firewalls)
	}
}

func TestPfSenseValidateRequiresAPIKey(t *testing.T) {
	err := New().ValidateConfig(driver.TargetConfig{
		ID: "pf1", Endpoint: "https://192.0.2.1",
		ResolvedSecrets: map[string]string{"password": "unused"},
	})
	if err == nil {
		t.Fatal("expected error: password alone cannot authenticate")
	}
}

func TestPfSenseFailedCallsMarkSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/status/system":
			_, _ = w.Write([]byte(`{"data":{"hostname":"pf"}}`))
		case "/api/v2/status/interfaces":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusInternalServerError) // every best-effort call fails
		}
	}))
	defer srv.Close()
	snap, err := New().Poll(context.Background(), driver.TargetConfig{
		ID: "pf1", Endpoint: srv.URL, Settings: map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"api_key": "k"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range []driver.Section{driver.SectionGateways, driver.SectionNeighbors, driver.SectionDHCP, driver.SectionFirewalls, driver.SectionVPNs} {
		if !slices.Contains(snap.Unavailable, sec) {
			t.Errorf("%s not marked: %v", sec, snap.Unavailable)
		}
	}
}

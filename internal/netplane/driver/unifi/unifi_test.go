// SPDX-License-Identifier: AGPL-3.0-or-later
package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

func TestUniFiDriverPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "TOKEN", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"unique_id":"admin"}`))
			return
		}
		if c, err := r.Cookie("TOKEN"); err != nil || c.Value != "session-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/proxy/network/api/s/default/stat/device":
			_, _ = w.Write([]byte(`{
				"data": [
					{
						"mac": "3c:78:95:1f:71:0d",
						"model": "USL8LP",
						"type": "usw",
						"name": "Shed Switch",
						"ip": "192.0.2.9",
						"version": "7.6.11",
						"serial": "3C78951F710D",
						"state": 1,
						"uptime": 3600,
						"total_max_power": 52.0,
						"has_temperature": true,
						"general_temperature": 44.5,
						"system-stats": {"cpu": "12.5"},
						"port_table": [
							{
								"port_idx": 1, "name": "Port 1", "media": "GE", "enable": true, "up": true,
								"speed": 1000, "full_duplex": true, "autoneg": true, "stp_state": "forwarding",
								"port_poe": true, "poe_enable": true, "poe_power": "6.4", "rx_bytes": 10000, "tx_bytes": 20000
							},
							{
								"port_idx": 9, "name": "SFP+ 9", "media": "SFP+", "enable": true, "up": true,
								"speed": 10000, "full_duplex": true, "sfp_found": true, "sfp_vendor": "FS",
								"sfp_rxpower": "-4.8", "sfp_temperature": "41.2"
							}
						],
						"lldp_table": [
							{"local_port_idx": 9, "chassis_id": "00:11:22:33:44:01", "port_id": "sfp0", "lldp_system_name": "core-fw"}
						]
					},
					{
						"mac": "84:78:48:12:99:50",
						"model": "UAPA6A9",
						"type": "uap",
						"name": "Office AP",
						"ip": "192.0.2.20",
						"state": 1,
						"radio_table": [{"name": "wifi1", "radio": "na", "channel": 36, "ht": 80, "tx_power": 20}],
						"vap_table": [{"radio": "na", "essid": "HomeWiFi", "up": true, "num_sta": 1}],
						"uplink": {"type": "wireless", "uplink_mac": "84:78:48:00:00:01", "rssi": -58, "tx_rate": 866000}
					}
				]
			}`))
		case "/proxy/network/api/s/default/stat/sta":
			_, _ = w.Write([]byte(`{
				"data": [
					{"mac": "de:ad:be:ef:00:02", "ip": "192.0.2.102", "hostname": "pixel", "is_wired": false, "ap_mac": "84:78:48:12:99:50", "essid": "HomeWiFi", "signal": -55, "tx_rate": 1200000, "vlan": 10}
				]
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	drv := New()
	if err := drv.ValidateConfig(driver.TargetConfig{ID: "t1"}); err == nil {
		t.Fatal("expected validation error for missing endpoint")
	}

	cfg := driver.TargetConfig{
		ID:       "t-unifi",
		Driver:   "unifi",
		Endpoint: srv.URL,
		Settings: map[string]any{"is_unifi_os": true, "site": "default", "allow_insecure_http": true},
		ResolvedSecrets: map[string]string{
			"username": "admin",
			"password": "secret",
		},
	}

	snap, err := drv.Poll(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	snap.Normalize()
	if len(snap.Devices) != 2 || len(snap.Links) != 2 || len(snap.LLDP) != 1 || len(snap.Radios) != 1 || len(snap.WirelessUplinks) != 1 || len(snap.Clients) != 1 {
		t.Fatalf("unexpected counts: devs=%d links=%d lldp=%d radios=%d uplinks=%d clients=%d",
			len(snap.Devices), len(snap.Links), len(snap.LLDP), len(snap.Radios), len(snap.WirelessUplinks), len(snap.Clients))
	}
	if snap.Devices[0].Role != model.RoleSwitch || snap.Devices[1].Role != model.RoleAP {
		t.Fatalf("unexpected roles: %s / %s", snap.Devices[0].Role, snap.Devices[1].Role)
	}
	if snap.Devices[0].PoEBudget == nil || snap.Devices[0].PoEBudget.UsedWatts != 6.4 {
		t.Fatalf("unexpected PoE budget: %+v", snap.Devices[0].PoEBudget)
	}
	if !snap.Links[1].IsFiber || snap.Links[1].SFP == nil || *snap.Links[1].SFP.RxPowerDbm != -4.8 {
		t.Fatalf("unexpected SFP link: %+v", snap.Links[1])
	}
	if snap.Clients[0].AttachedDeviceID != "unifi:84:78:48:12:99:50" || snap.Clients[0].SSID != "HomeWiFi" {
		t.Fatalf("unexpected client: %+v", snap.Clients[0])
	}
}

func TestUniFiMarksClientsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/s/default/stat/device" {
			_, _ = w.Write([]byte(`{"data":[{"mac":"3c:78:95:1f:71:0d","type":"usw","state":1}]}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	snap, err := New().Poll(context.Background(), driver.TargetConfig{
		ID: "u1", Endpoint: srv.URL,
		Settings:        map[string]any{"is_unifi_os": false, "allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"api_key": "k"},
	})
	if err != nil {
		t.Fatalf("a failed client list must not fail the poll: %v", err)
	}
	if len(snap.Devices) != 1 || !slices.Contains(snap.Unavailable, driver.SectionClients) {
		t.Fatalf("devices=%d unavailable=%v", len(snap.Devices), snap.Unavailable)
	}
}

func TestFlexFloatDropsNonfinite(t *testing.T) {
	for in, want := range map[string]*float64{
		`"12.5"`: ptr(12.5), `12.5`: ptr(12.5), `"NaN"`: nil, `"Inf"`: nil, `"-Infinity"`: nil, `"12.5abc"`: nil,
	} {
		var f flexFloat
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := f.Ptr(); (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%s: got %v, want %v", in, got, want)
		}
	}
}

func ptr(v float64) *float64 { return &v }

// TestUniFiReusesLogin checks that the login cookie serves many polls and that a 401 triggers
// exactly one re-login.
func TestUniFiReusesLogin(t *testing.T) {
	var mu sync.Mutex
	logins, token := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/auth/login" {
			logins++
			token = fmt.Sprintf("session-%d", logins)
			http.SetCookie(w, &http.Cookie{Name: "TOKEN", Value: token, Path: "/"})
			return
		}
		if c, err := r.Cookie("TOKEN"); err != nil || c.Value != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	drv := New()
	cfg := driver.TargetConfig{
		ID: "u1", Driver: "unifi", Endpoint: srv.URL,
		Settings:        map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"username": "admin", "password": "pw"},
	}
	for i := 0; i < 3; i++ {
		if _, err := drv.Poll(context.Background(), cfg); err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
	}
	if logins != 1 {
		t.Fatalf("logins after 3 polls = %d, want 1", logins)
	}
	mu.Lock()
	token = "revoked" // the controller expired the session
	mu.Unlock()
	if _, err := drv.Poll(context.Background(), cfg); err != nil {
		t.Fatalf("Poll after expiry: %v", err)
	}
	if logins != 2 {
		t.Fatalf("logins after expiry = %d, want 2", logins)
	}
	cfg.ResolvedSecrets = map[string]string{"username": "admin", "password": "new"}
	if _, err := drv.Poll(context.Background(), cfg); err != nil {
		t.Fatalf("Poll after a password change: %v", err)
	}
	if logins != 3 {
		t.Fatalf("logins after a password change = %d, want 3", logins)
	}
	// With an API key there is never a login, even on a 401.
	cfg.ResolvedSecrets = map[string]string{"api_key": "k"}
	_, _ = drv.Poll(context.Background(), cfg)
	if logins != 3 {
		t.Fatalf("an API key target logged in: logins = %d", logins)
	}
}

// TestUniFiPersistent401LogsInOncePerPoll checks the per-poll re-login limit.
func TestUniFiPersistent401LogsInOncePerPoll(t *testing.T) {
	var mu sync.Mutex
	logins := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/auth/login" {
			logins++
			http.SetCookie(w, &http.Cookie{Name: "TOKEN", Value: "x", Path: "/"})
			return
		}
		w.WriteHeader(http.StatusUnauthorized) // e.g. the account lacks Network access
	}))
	defer srv.Close()
	drv := New()
	cfg := driver.TargetConfig{
		ID: "u2", Driver: "unifi", Endpoint: srv.URL,
		Settings:        map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"username": "admin", "password": "pw"},
	}
	_, _ = drv.Poll(context.Background(), cfg) // up-front login, then a 401: no second login
	if logins != 1 {
		t.Fatalf("first poll logins = %d, want 1", logins)
	}
	_, _ = drv.Poll(context.Background(), cfg) // kept login, 401: one re-login
	if logins != 2 {
		t.Fatalf("second poll logins = %d, want 2", logins)
	}
}

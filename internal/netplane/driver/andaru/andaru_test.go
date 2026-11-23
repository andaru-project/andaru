// SPDX-License-Identifier: AGPL-3.0-or-later
package andaru

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

func TestAndaruDriverPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status/links":
			_, _ = w.Write([]byte(`{
				"items": [
					{"ifIndex": 1, "name": "lo", "linkType": "loopback", "mtu": 65536, "operState": "UNKNOWN", "flags": ["UP","LOOPBACK"], "addresses": [{"family":"ipv4","address":"127.0.0.1","prefixLength":8,"scope":"host"}]},
					{"ifIndex": 2, "name": "eth0", "linkType": "ether", "mtu": 1500, "operState": "UP", "flags": ["UP","BROADCAST","RUNNING"], "linkAddress": "00:11:22:33:44:01", "addresses": [
						{"family":"ipv4","address":"192.0.2.1","prefixLength":24,"scope":"global"},
						{"family":"ipv6","address":"2001:db8::1","prefixLength":64,"scope":"global"}
					]}
				]
			}`))
		case "/api/v1/config":
			_, _ = w.Write([]byte(`{
				"state": "saved",
				"version": 7,
				"zones": [{"id": "z-lan", "name": "lan", "segmentIds": ["eth0"]}],
				"policy": {"input": [{}, {}], "forward": [{}]},
				"nat": {"portForwards": [{}], "oneToOne": []}
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	drv := New()
	if err := drv.ValidateConfig(driver.TargetConfig{ID: "a1"}); err == nil {
		t.Fatal("expected error for missing endpoint")
	}

	snap, err := drv.Poll(context.Background(), driver.TargetConfig{
		ID:       "andaru-core",
		Name:     "core-fw",
		Driver:   "andaru",
		Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	snap.Normalize()
	if len(snap.Devices) != 1 || snap.Devices[0].Role != model.RoleFirewall || snap.Devices[0].MgmtIP != "192.0.2.1" {
		t.Fatalf("unexpected device: %+v", snap.Devices)
	}
	if snap.Devices[0].ID != "andaru:andaru-core" || snap.Devices[0].PrimaryMAC != "00:11:22:33:44:01" {
		t.Fatalf("device identity = %q (primary MAC %q), want target-keyed ID", snap.Devices[0].ID, snap.Devices[0].PrimaryMAC)
	}
	if len(snap.Links) != 1 || snap.Links[0].IfName != "eth0" || !snap.Links[0].LinkUp {
		t.Fatalf("unexpected links: %+v", snap.Links)
	}
	if len(snap.Segments) != 1 || len(snap.Segments[0].Addresses) != 2 {
		t.Fatalf("unexpected dual-stack segment: %+v", snap.Segments)
	}
	if len(snap.Zones) != 1 || snap.Zones[0].Name != "lan" {
		t.Fatalf("unexpected zones: %+v", snap.Zones)
	}
	if len(snap.Firewalls) != 1 || *snap.Firewalls[0].PolicyRuleCount != 3 || snap.Firewalls[0].SavedVersion != "7" {
		t.Fatalf("unexpected firewall state: %+v", snap.Firewalls)
	}
}

func TestAndaruUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "and")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "e.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/v1/status/links" {
			_, _ = w.Write([]byte(`{"items":[{"ifIndex":2,"name":"eth0","linkType":"ether","operState":"UP"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	snap, err := New().Poll(context.Background(), driver.TargetConfig{ID: "a1", Endpoint: "unix://" + sock, ResolvedSecrets: map[string]string{"api_token": "tok"}})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(snap.Links) != 1 || snap.Links[0].IfName != "eth0" {
		t.Fatalf("unexpected links: %+v", snap.Links)
	}
}

func TestAndaruRefusesTokenOverPlainHTTP(t *testing.T) {
	cfg := driver.TargetConfig{ID: "a1", Endpoint: "http://192.0.2.1", ResolvedSecrets: map[string]string{"api_token": "tok"}}
	if err := New().ValidateConfig(cfg); err == nil {
		t.Fatal("expected refusal to send api_token over plain http")
	}
	cfg.Settings = map[string]any{"allow_insecure_http": true}
	if err := New().ValidateConfig(cfg); err != nil {
		t.Fatalf("allow_insecure_http: %v", err)
	}
}

func TestAndaruFailedConfigMarksSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/status/links" {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	snap, err := New().Poll(context.Background(), driver.TargetConfig{ID: "a1", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(snap.Unavailable, driver.SectionZones) || !slices.Contains(snap.Unavailable, driver.SectionFirewalls) {
		t.Fatalf("unavailable = %v", snap.Unavailable)
	}
}

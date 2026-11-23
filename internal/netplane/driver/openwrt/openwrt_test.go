// SPDX-License-Identifier: AGPL-3.0-or-later
package openwrt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

func TestOpenWrtDriverPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var obj, method string
		_ = json.Unmarshal(req.Params[1], &obj)
		_ = json.Unmarshal(req.Params[2], &method)

		switch obj + "." + method {
		case "session.login":
			_, _ = w.Write([]byte(`{"result":[0,{"ubus_rpc_session":"sid-123"}]}`))
		case "system.board":
			_, _ = w.Write([]byte(`{"result":[0,{"hostname":"sw-lab","model":"RTL930x Switch","release":{"description":"OpenWrt 24.10.1"}}]}`))
		case "system.info":
			_, _ = w.Write([]byte(`{"result":[0,{"uptime":7200,"load":[6553],"memory":{"total":134217728,"free":67108864}}]}`))
		case "network.device.status":
			_, _ = w.Write([]byte(`{"result":[0,{
				"lo":{"type":"Network device","up":true,"carrier":true,"mtu":65536,"macaddr":"02:00:00:00:00:99"},
				"lan1":{"type":"Network device","up":true,"carrier":true,"mtu":1500,"macaddr":"02:51:0a:00:00:01","speed":"2500F","autoneg":true,"flow-control":{"autoneg":true,"supported":["rx","tx"],"link-advertising":["rx","tx"],"link-partner-advertising":["rx","tx"],"negotiated":["rx","tx"]},"statistics":{"rx_bytes":1000,"tx_bytes":2000,"rx_packets":10,"tx_packets":20,"rx_errors":1,"tx_errors":2,"rx_dropped":3,"tx_dropped":4,"collisions":7}},
				"lan10":{"type":"Network device","up":false,"carrier":false,"mtu":1500,"macaddr":"02:51:0a:00:00:01","flow-control":{"autoneg":false,"supported":["rx","tx"],"link-partner-advertising":[],"selected":["tx"]}},
				"lan2":{"type":"Network device","up":false,"carrier":false,"mtu":1500,"macaddr":"02:51:0a:00:00:01"},
				"sfp9":{"type":"Network device","up":true,"carrier":true,"mtu":1500,"macaddr":"02:51:0a:00:00:02","speed":"10000F","statistics":{"rx_bytes":50000,"tx_bytes":40000}}
			}]}`))
		case "network.interface.dump":
			_, _ = w.Write([]byte(`{"result":[0,{"interface":[
				{"interface":"lan","up":true,"proto":"static","l3_device":"br-lan","ipv4-address":[{"address":"192.0.2.10","mask":24}],"ipv6-address":[{"address":"2001:db8::10","mask":64}],"ipv6-prefix-assignment":[
					{"address":"2001:db8:1::","mask":64,"local-address":{"address":"2001:db8:1::1","mask":64}},
					{"address":"2001:db8:2::","mask":64,"local-address":{}}
				]}
			]}]}`))
		case "network.wireless.status":
			_, _ = w.Write([]byte(`{"result":[0,{}]}`))
		case "luci-rpc.getHostHints":
			_, _ = w.Write([]byte(`{"result":[0,{"DE:AD:BE:EF:00:01":{"ipaddrs":["192.0.2.50"],"name":"pc1"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"result":[4]}`))
		}
	}))
	defer srv.Close()

	drv := New()
	if err := drv.ValidateConfig(driver.TargetConfig{ID: "ow1"}); err == nil {
		t.Fatal("expected error for missing endpoint")
	}

	snap, err := drv.Poll(context.Background(), driver.TargetConfig{
		ID:       "ow-sw",
		Driver:   "openwrt",
		Endpoint: srv.URL,
		Settings: map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{
			"username": "root",
			"password": "secret",
		},
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	snap.Normalize()
	if len(snap.Devices) != 1 || snap.Devices[0].MgmtIP != "192.0.2.10" || snap.Devices[0].ID != "openwrt:ow-sw" || snap.Devices[0].PrimaryMAC != "02:51:0a:00:00:01" {
		t.Fatalf("unexpected device: %+v", snap.Devices)
	}
	if macs := snap.Devices[0].MACs; !slices.Equal(macs, []string{"02:51:0a:00:00:01", "02:51:0a:00:00:02"}) {
		t.Fatalf("device MACs = %v, want lan1 and sfp9 only (lo skipped, repeats dropped)", macs)
	}
	if c := snap.Links[0].Counters; c == nil || *c != (model.PortCounters{RxBytes: 1000, TxBytes: 2000, RxPackets: 10, TxPackets: 20, RxErrors: 1, TxErrors: 2, RxDropped: 3, TxDropped: 4}) {
		t.Fatalf("lan1 counters = %+v", c)
	}
	if caps := snap.Devices[0].Capabilities; !slices.Contains(caps, model.CapDiagPing) || !slices.Contains(caps, model.CapDiagTraceroute) {
		t.Fatalf("diagnostic capabilities missing: %v", caps)
	}
	var names []string
	for _, l := range snap.Links {
		names = append(names, l.IfName)
	}
	if got := strings.Join(names, ","); got != "lan1,lan2,lan10,sfp9" {
		t.Fatalf("link order = %s, want lan1,lan2,lan10,sfp9", got)
	}
	if snap.Links[0].SpeedMbps != 2500 || !snap.Links[3].IsFiber || snap.Links[3].Media != model.MediaSFPPl {
		t.Fatalf("unexpected links: %+v", snap.Links)
	}
	if fc := snap.Links[0].FlowControl; fc == nil || !fc.Rx || !fc.Tx {
		t.Fatalf("negotiated flow control = %+v, want rx+tx", fc)
	}
	if fc := snap.Links[2].FlowControl; fc == nil || fc.Rx || !fc.Tx {
		t.Fatalf("selected flow control = %+v, want tx only", fc)
	}
	if len(snap.Segments) != 1 || len(snap.Segments[0].Addresses) != 3 || snap.Segments[0].Addresses[2].Address != "2001:db8:1::1" {
		t.Fatalf("unexpected segments: %+v", snap.Segments)
	}
	if len(snap.Neighbors) != 1 || snap.Neighbors[0].MAC != "de:ad:be:ef:00:01" {
		t.Fatalf("unexpected neighbors: %+v", snap.Neighbors)
	}
}

func TestOpenWrtEmptySessionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":[0,{"ubus_rpc_session":""}]}`))
	}))
	defer srv.Close()
	_, err := New().Poll(context.Background(), driver.TargetConfig{
		ID: "ow1", Endpoint: srv.URL,
		Settings:        map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"username": "root", "password": "pw"},
	})
	if err == nil || err.Error() != "openwrt ubus login: empty session" {
		t.Fatalf("want plain empty-session error, got %v", err)
	}
}

func TestOpenWrtRefusesCredentialsOverPlainHTTP(t *testing.T) {
	err := New().ValidateConfig(driver.TargetConfig{
		ID: "ow1", Endpoint: "http://192.0.2.1",
		ResolvedSecrets: map[string]string{"username": "root", "password": "pw"},
	})
	if err == nil || !strings.Contains(err.Error(), "allow_insecure_http") {
		t.Fatalf("want plain-http refusal, got %v", err)
	}
}

// TestOpenWrtReusesSession checks that one ubus session serves many polls, that an expired
// session triggers exactly one re-login, and that changed credentials start a new session.
func TestOpenWrtReusesSession(t *testing.T) {
	var mu sync.Mutex
	logins, valid := 0, map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var sid, obj, method string
		_ = json.Unmarshal(req.Params[0], &sid)
		_ = json.Unmarshal(req.Params[1], &obj)
		_ = json.Unmarshal(req.Params[2], &method)
		mu.Lock()
		defer mu.Unlock()
		if obj+"."+method == "session.login" {
			logins++
			sid := fmt.Sprintf("sid-%d", logins)
			valid[sid] = true
			_, _ = fmt.Fprintf(w, `{"result":[0,{"ubus_rpc_session":%q}]}`, sid)
			return
		}
		if !valid[sid] {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Access denied"}}`))
			return
		}
		if obj+"."+method == "luci-rpc.getHostHints" { // outside this account's ACL
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Access denied"}}`))
			return
		}
		if obj+"."+method == "network.device.status" {
			_, _ = w.Write([]byte(`{"result":[0,{"lan1":{"up":true,"carrier":true,"macaddr":"02:51:0a:00:00:01"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":[0,{}]}`))
	}))
	defer srv.Close()

	drv := New()
	cfg := driver.TargetConfig{
		ID: "ow1", Driver: "openwrt", Endpoint: srv.URL,
		Settings:        map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"username": "root", "password": "pw"},
	}
	poll := func() {
		t.Helper()
		snap, err := drv.Poll(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if !slices.Contains(snap.Unavailable, driver.SectionNeighbors) { // getHostHints is denied
			t.Fatalf("unavailable = %v", snap.Unavailable)
		}
	}
	poll()
	poll()
	poll()
	// The ACL-denied call is learned with the poll's own login, so no extra login.
	if logins != 1 {
		t.Fatalf("logins after 3 polls = %d, want 1", logins)
	}
	mu.Lock()
	valid = map[string]bool{} // rpcd expired the session
	mu.Unlock()
	poll()
	if logins != 2 {
		t.Fatalf("logins after expiry = %d, want 2", logins)
	}
	cfg.ResolvedSecrets = map[string]string{"username": "root", "password": "new"}
	poll()
	if logins != 3 {
		t.Fatalf("logins after a password change = %d, want 3", logins)
	}
}

// TestOpenWrtSessionLossAndReloginFailure checks that a session lost right after login is
// dropped (the next poll logs in again instead of reusing a dead sid), and that a failed
// re-login is reported.
func TestOpenWrtSessionLossAndReloginFailure(t *testing.T) {
	var mu sync.Mutex
	logins, loginOK, killAfterLogin := 0, true, false
	valid := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var sid, obj, method string
		_ = json.Unmarshal(req.Params[0], &sid)
		_ = json.Unmarshal(req.Params[1], &obj)
		_ = json.Unmarshal(req.Params[2], &method)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case obj+"."+method == "session.login":
			logins++
			if !loginOK {
				_, _ = w.Write([]byte(`{"result":[6]}`))
				return
			}
			sid := fmt.Sprintf("sid-%d", logins)
			valid[sid] = !killAfterLogin // rpcd restarts right after handing out the sid
			_, _ = fmt.Fprintf(w, `{"result":[0,{"ubus_rpc_session":%q}]}`, sid)
		case !valid[sid]:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Access denied"}}`))
		case obj+"."+method == "network.device.status":
			_, _ = w.Write([]byte(`{"result":[0,{"lan1":{"up":true,"carrier":true}}]}`))
		default:
			_, _ = w.Write([]byte(`{"result":[0,{}]}`))
		}
	}))
	defer srv.Close()
	drv := New()
	cfg := driver.TargetConfig{
		ID: "ow2", Driver: "openwrt", Endpoint: srv.URL,
		Settings:        map[string]any{"allow_insecure_http": true},
		ResolvedSecrets: map[string]string{"username": "u", "password": "p"},
	}
	mu.Lock()
	killAfterLogin = true
	mu.Unlock()
	if _, err := drv.Poll(context.Background(), cfg); err == nil {
		t.Fatal("a session lost right after login must fail the poll")
	}
	if sid := drv.sessions["ow2"].sid; sid != "" {
		t.Fatalf("a dead sid must be dropped, kept %q", sid)
	}
	mu.Lock()
	killAfterLogin = false
	mu.Unlock()
	if _, err := drv.Poll(context.Background(), cfg); err != nil {
		t.Fatalf("the next poll must log in again and recover: %v", err)
	}
	if logins != 2 {
		t.Fatalf("logins = %d, want 2", logins)
	}
	mu.Lock()
	valid, loginOK = map[string]bool{}, false // session expired and the password now fails
	mu.Unlock()
	_, err := drv.Poll(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("want the re-login error, got %v", err)
	}
}

func TestGuessMedia(t *testing.T) {
	for _, tc := range []struct {
		name  string
		speed int
		want  model.MediaType
	}{
		{"tun0", 10, model.MediaOther}, {"lan1", 100, model.MediaFE}, {"lan2", 2500, model.Media25GE},
		{"sfp1", 2500, model.MediaSFPPl}, {"sfp2", 1000, model.MediaSFP}, {"bond0", 40000, model.MediaOther}, {"bond-lan", 20000, model.MediaOther}, {"team0", 40000, model.MediaOther},
	} {
		if got := guessMedia(tc.name, tc.speed); got != tc.want {
			t.Errorf("guessMedia(%q, %d) = %s, want %s", tc.name, tc.speed, got, tc.want)
		}
	}
}

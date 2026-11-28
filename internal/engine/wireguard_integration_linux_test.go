//go:build integration

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func wgTestKey(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

// Over the real store: the interface's routes keep its keys out of every answer, derive its public key, refuse to
// delete what a route names, export a peer's configuration (its pre-shared key audited), and join the kernel's state.
func TestWireGuardRoutes(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()
	stateDir := filepath.Join(privateTestDir(t), "state")
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	audits, err := audit.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { audits.Close() })
	dump := ""
	wg := &wireguardAPI{store: store, audits: audits, stateDir: stateDir, status: func(ctx context.Context, file string) error {
		status, err := generation.ParseWireGuardDump([]byte(dump), time.Unix(1_800_000_100, 0))
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(status)
		return os.WriteFile(file, raw, 0o600)
	}}
	configuration := newSavedHandler(store, host.ReadLinks, host.ReadRealizerFacts, http.NotFoundHandler(), nil)
	h := newHandlerWithDeps(handlerDeps{readLinks: host.ReadLinks, configuration: configuration, wireguardAPI: wg})
	call := func(method, path, body, tag string) *httptest.ResponseRecorder {
		r := rootRequest(method, path)
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/json")
		if tag != "" {
			r.Header.Set("If-Match", tag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), wgTestKey(1)) || strings.Contains(w.Body.String(), wgTestKey(2)) && !strings.HasSuffix(path, "/config") {
			t.Fatalf("%s %s leaked a key: %s", method, path, w.Body)
		}
		return w
	}
	batch := call("POST", "/api/v1/config/changes", `{"operations":[{"op":"create","collection":"zones","localId":"z","value":{"name":"vpn"}},`+
		`{"op":"create","collection":"vpn.wireguard","localId":"wg","value":{"name":"road","ifname":"wg0","enabled":true,"zone":"@z","listenPort":51820,"addresses":["10.200.0.1/24","fd00:200::1/64"],`+
		`"privateKeyRef":{"secret":"WG0_KEY","set":"`+wgTestKey(1)+`","expectedVersion":"0"},`+
		`"peers":[{"name":"laptop","enabled":true,"publicKey":"`+wgTestKey(9)+`","allowedIps":["10.200.0.2/32"],"persistentKeepalive":25,"presharedKeyRef":{"secret":"WG0_PSK","set":"`+wgTestKey(2)+`","expectedVersion":"0"}},`+
		`{"name":"phone","enabled":true,"publicKey":"`+wgTestKey(8)+`","allowedIps":["10.200.0.3/32"]}]}},`+
		`{"op":"create","collection":"routes","value":{"name":"site","destination":"10.50.0.0/16","target":{"kind":"wireguard","id":"@wg"},"enabled":true}}]}`, "")
	var result config.SaveResult
	if batch.Code != 200 || json.Unmarshal(batch.Body.Bytes(), &result) != nil {
		t.Fatalf("batch %d %s", batch.Code, batch.Body)
	}
	id := result.Created["wg"]
	public, _ := config.WireGuardPublicKey(wgTestKey(1))
	got := call("GET", "/api/v1/config/vpn/wireguard/"+id, "", "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"publicKey":"`+public+`"`) || !strings.Contains(got.Body.String(), `"privateKeyRef":{"secret":"WG0_KEY","isSet":true`) {
		t.Fatalf("get %d %s", got.Code, got.Body)
	}
	var view struct {
		Value struct {
			Peers []struct{ ID, Name string } `json:"peers"`
		} `json:"value"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &view)
	peerID := ""
	for _, p := range view.Value.Peers {
		if p.Name == "laptop" {
			peerID = p.ID
		}
	}
	if !strings.HasPrefix(peerID, "wgp_") {
		t.Fatalf("peers %+v", view.Value.Peers)
	}
	created := call("POST", "/api/v1/config/vpn/wireguard", `{"name":"off","ifname":"wg1","enabled":false,"listenPort":51821,"privateKeyRef":{"secret":"WG1_KEY","set":"`+wgTestKey(3)+`","expectedVersion":"0"}}`, "")
	if created.Code != 201 || !strings.HasPrefix(created.Header().Get("Location"), "/api/v1/config/vpn/wireguard/wg_") {
		t.Fatalf("create %d %s %v", created.Code, created.Body, created.Header())
	}
	if w := call("GET", "/api/v1/config/vpn/wireguard", "", ""); w.Code != 200 || strings.Count(w.Body.String(), `"ifname"`) != 2 {
		t.Fatalf("list %d %s", w.Code, w.Body)
	}
	if w := call("PUT", "/api/v1/config/vpn/wireguard/"+id, `{"name":"road","ifname":"wg0","enabled":true,"listenPort":1}`, ""); w.Code != 428 {
		t.Fatalf("put without If-Match %d %s", w.Code, w.Body)
	}
	if w := call("DELETE", "/api/v1/config/vpn/wireguard/"+id, "", got.Header().Get("ETag")); w.Code != 422 || !strings.Contains(w.Body.String(), "remains referenced") {
		t.Fatalf("delete of a routed interface %d %s", w.Code, w.Body)
	}

	// The peer's configuration, without and with its pre-shared key.
	conf := call("POST", "/api/v1/vpn/wireguard/"+id+"/peers/"+peerID+"/config", `{"endpoint":"vpn.example.com","dns":["10.200.0.1"]}`, "")
	want := "[Interface]\nPrivateKey = <PRIVATE_KEY>\nAddress = 10.200.0.2/32\nDNS = 10.200.0.1\n\n[Peer]\nPublicKey = " + public +
		"\nAllowedIPs = 10.200.0.0/24, fd00:200::/64\nEndpoint = vpn.example.com:51820\nPersistentKeepalive = 25\n"
	if conf.Code != 200 || conf.Body.String() != want || conf.Header().Get("Content-Type") != "text/plain; charset=utf-8" || conf.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("config %d %v\n%s", conf.Code, conf.Header(), conf.Body)
	}
	withPSK := call("POST", "/api/v1/vpn/wireguard/"+id+"/peers/"+peerID+"/config", `{"endpoint":"[2001:db8::1]:443","includePsk":true}`, "")
	if withPSK.Code != 200 || !strings.Contains(withPSK.Body.String(), "PresharedKey = "+wgTestKey(2)+"\n") || !strings.Contains(withPSK.Body.String(), "Endpoint = [2001:db8::1]:443\n") {
		t.Fatalf("config with psk %d\n%s", withPSK.Code, withPSK.Body)
	}
	events, err := audits.ListEvents(ctx, 0, 100)
	exported := 0
	for _, e := range events {
		if e.Action == audit.ActionWireGuardPSKExported && e.Target == id && e.Result == audit.ResultSuccess {
			exported++
		}
	}
	if err != nil || exported != 1 {
		t.Fatalf("audit %v %+v", err, events)
	}
	var phoneID string
	for _, p := range view.Value.Peers {
		if p.Name == "phone" {
			phoneID = p.ID
		}
	}
	if w := call("POST", "/api/v1/vpn/wireguard/"+id+"/peers/"+phoneID+"/config", `{"includePsk":true}`, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "PRESHARED_KEY_UNAVAILABLE") {
		t.Fatalf("no psk %d %s", w.Code, w.Body)
	}
	for path, code := range map[string]int{
		"/api/v1/vpn/wireguard/" + id + "/peers/wgp_00000000-0000-4000-8000-000000000000/config":    404,
		"/api/v1/vpn/wireguard/wg_00000000-0000-4000-8000-000000000000/peers/" + peerID + "/config": 404,
		"/api/v1/vpn/wireguard/" + id + "/peers/" + peerID:                                          404,
	} {
		if w := call("POST", path, "", ""); w.Code != code {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}

	// Status: the running peer is named, an unknown one is shown bare, and a saved one not running is offline.
	dump = "wg0\tPRIV=\t" + public + "\t51820\toff\n" +
		"wg0\t" + wgTestKey(9) + "\t(none)\t198.51.100.7:40000\t10.200.0.2/32\t1800000000\t10\t20\t25\n" +
		"wg0\t" + wgTestKey(7) + "\t(none)\t(none)\t(none)\t0\t0\t0\toff\n"
	status := call("GET", "/api/v1/status/vpn/wireguard", "", "")
	var sv struct {
		Items []wireguardStatus `json:"items"`
	}
	if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &sv) != nil || len(sv.Items) != 2 || strings.Contains(status.Body.String(), "PRIV=") {
		t.Fatalf("status %d %s", status.Code, status.Body)
	}
	var road wireguardStatus
	for _, it := range sv.Items {
		if it.ID == id {
			road = it
		}
	}
	if len(road.Peers) != 3 || road.Peers[0].ID != peerID || !road.Peers[0].Online || road.Peers[0].LatestHandshakeAt == nil || road.Peers[0].RxBytes != 10 ||
		road.Peers[1].ID != "" || road.Peers[1].Online || road.Peers[2].ID != phoneID || road.Peers[2].Online {
		t.Fatalf("road %+v", road)
	}
	if entries, _ := os.ReadDir(stateDir); strings.Contains(func() string {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return strings.Join(names, " ")
	}(), "wg-status-") {
		t.Fatal("the status file stayed behind")
	}
	// The views the schema serves describe what the routes answer.
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(call("GET", "/api/v1/schema", "", "").Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	if err = compiler.AddResource("https://andaru.invalid/api/v1/schema", instance); err != nil {
		t.Fatal(err)
	}
	for view, path := range map[string]string{"snapshotView": "/api/v1/config", "collectionView": "/api/v1/config/vpn/wireguard"} {
		sch, err := compiler.Compile("https://andaru.invalid/api/v1/schema#/$defs/" + view)
		if err != nil {
			t.Fatal(err)
		}
		served, _ := jsonschema.UnmarshalJSON(bytes.NewReader(call("GET", path, "", "").Body.Bytes()))
		if err = sch.Validate(served); err != nil {
			t.Fatalf("%s violates %s: %v", path, view, err)
		}
	}
	wg.status = func(context.Context, string) error { return io.ErrUnexpectedEOF }
	if w := call("GET", "/api/v1/status/vpn/wireguard", "", ""); w.Code != 503 {
		t.Fatalf("status without realize %d %s", w.Code, w.Body)
	}
}

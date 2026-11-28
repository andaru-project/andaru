//go:build integration

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Over the real store: the connections' routes keep secrets out of every answer and refuse to delete what a route
// names; status joins strongSwan's SAs with the saved connections; initiate and terminate are refused unless the
// running strongSwan holds an enabled connection, and are audited.
func TestIPsecRoutes(t *testing.T) {
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
	runtime := generation.IPsecStatus{Connections: []string{}, SAs: []generation.IPsecSA{}}
	var controlled []string
	var controlErr error
	api := &ipsecAPI{store: store, audits: audits, stateDir: stateDir,
		status: func(_ context.Context, file string) error {
			raw, _ := json.Marshal(runtime)
			return os.WriteFile(file, raw, 0o600)
		},
		control: func(_ context.Context, action, id string) error {
			controlled = append(controlled, action+" "+id)
			return controlErr
		}}
	configuration := newSavedHandler(store, host.ReadLinks, host.ReadRealizerFacts, http.NotFoundHandler(), nil)
	h := newHandlerWithDeps(handlerDeps{readLinks: host.ReadLinks, configuration: configuration, ipsecAPI: api})
	const psk, password = "TEST_ONLY_IPSEC_PSK", "TEST_ONLY_EAP_PASSWORD"
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
		if strings.Contains(w.Body.String(), psk) || strings.Contains(w.Body.String(), password) {
			t.Fatalf("%s %s leaked a secret: %s", method, path, w.Body)
		}
		return w
	}
	batch := call("POST", "/api/v1/config/changes", `{"operations":[{"op":"create","collection":"zones","localId":"z","value":{"name":"vpn"}},`+
		`{"op":"create","collection":"vpn.ipsec","localId":"site","value":{"name":"site-a","enabled":true,"mode":"route-based","ifname":"ipsec0","xfrmIfId":42,"zone":"@z","remoteAddress":"198.51.100.7","ikeVersion":2,`+
		`"auth":{"localType":"psk","remoteType":"psk","localId":"gw.example.com","pskRef":{"secret":"IPSEC_SITE","set":"`+psk+`","expectedVersion":"0"}}}},`+
		`{"op":"create","collection":"vpn.ipsec","localId":"road","value":{"name":"road","enabled":true,"mode":"road-warrior","ifname":"ipsec1","xfrmIfId":43,"zone":"@z","ikeVersion":2,"virtualPool":["10.99.0.0/24"],`+
		`"auth":{"localType":"psk","remoteType":"eap-mschapv2","localId":"gw.example.com","pskRef":{"secret":"IPSEC_SITE"},"eapUsers":[{"username":"alice","passwordRef":{"secret":"IPSEC_ALICE","set":"`+password+`","expectedVersion":"0"}}]}}},`+
		`{"op":"create","collection":"routes","value":{"name":"site","destination":"10.40.0.0/16","target":{"kind":"ipsec","id":"@site"},"enabled":true}}]}`, "")
	var result config.SaveResult
	if batch.Code != 200 || json.Unmarshal(batch.Body.Bytes(), &result) != nil {
		t.Fatalf("batch %d %s", batch.Code, batch.Body)
	}
	site, road := result.Created["site"], result.Created["road"]
	got := call("GET", "/api/v1/config/vpn/ipsec/"+site, "", "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"pskRef":{"secret":"IPSEC_SITE","isSet":true`) || !strings.Contains(got.Body.String(), `"dpdDelaySeconds":30`) {
		t.Fatalf("get %d %s", got.Code, got.Body)
	}
	created := call("POST", "/api/v1/config/vpn/ipsec", `{"name":"branch","enabled":false,"mode":"policy-based","remoteAddress":"branch.example.net","ikeVersion":2,"localSubnets":["192.0.2.0/24"],"remoteSubnets":["10.30.0.0/16"],`+
		`"auth":{"localType":"psk","remoteType":"psk","localId":"gw","pskRef":{"secret":"IPSEC_BRANCH","set":"x","expectedVersion":"0"}}}`, "")
	if created.Code != 201 || !strings.HasPrefix(created.Header().Get("Location"), "/api/v1/config/vpn/ipsec/ips_") {
		t.Fatalf("create %d %s %v", created.Code, created.Body, created.Header())
	}
	branch := strings.TrimPrefix(created.Header().Get("Location"), "/api/v1/config/vpn/ipsec/")
	if w := call("GET", "/api/v1/config/vpn/ipsec", "", ""); w.Code != 200 || strings.Count(w.Body.String(), `"mode"`) != 3 {
		t.Fatalf("list %d %s", w.Code, w.Body)
	}
	if w := call("DELETE", "/api/v1/config/vpn/ipsec/"+site, "", got.Header().Get("ETag")); w.Code != 422 || !strings.Contains(w.Body.String(), "remains referenced") {
		t.Fatalf("delete of a routed connection %d %s", w.Code, w.Body)
	}

	// Status: each saved connection with its SAs; active while one is established.
	runtime = generation.IPsecStatus{ServiceActive: true, Connections: []string{site, road}, SAs: []generation.IPsecSA{
		{Name: site, UniqueID: "1", State: "ESTABLISHED", LocalHost: "203.0.113.2", RemoteHost: "198.51.100.7", EstablishedSec: 60,
			ChildSAs: []generation.IPsecChildSA{{Name: site, State: "INSTALLED", LocalTS: []string{"0.0.0.0/0"}, RemoteTS: []string{"10.40.0.0/16"}, BytesIn: 5, BytesOut: 6, RekeySec: 3000}}},
		{Name: "ips_00000000-0000-4000-8000-000000000000", State: "ESTABLISHED", ChildSAs: []generation.IPsecChildSA{}},
	}}
	status := call("GET", "/api/v1/status/vpn/ipsec", "", "")
	var sv struct {
		ServiceActive bool          `json:"serviceActive"`
		Items         []ipsecStatus `json:"items"`
	}
	if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &sv) != nil || !sv.ServiceActive || len(sv.Items) != 3 {
		t.Fatalf("status %d %s", status.Code, status.Body)
	}
	for _, it := range sv.Items {
		switch it.ID {
		case site:
			if !it.Applied || !it.Active || it.Mode != "route-based" || len(it.IKESAs) != 1 || it.IKESAs[0].ChildSAs[0].BytesOut != 6 || it.IKESAs[0].ChildSAs[0].RemoteTS[0] != "10.40.0.0/16" {
				t.Fatalf("site %+v", it)
			}
		case road:
			if !it.Applied || it.Active || len(it.IKESAs) != 0 || it.IKESAs == nil {
				t.Fatalf("road %+v", it)
			}
		default:
			if it.Applied || it.Active || len(it.IKESAs) != 0 || it.IKESAs == nil {
				t.Fatalf("%s %+v", it.ID, it)
			}
		}
	}
	if entries, _ := os.ReadDir(stateDir); slices.ContainsFunc(entries, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), "ipsec-status-") }) {
		t.Fatal("the status file stayed behind")
	}

	// Actions.
	act := func(id, action string) *httptest.ResponseRecorder {
		return call("POST", "/api/v1/vpn/ipsec/"+id+"/"+action, "", "")
	}
	for name, c := range map[string]struct {
		id, action string
		code       int
		why        string
	}{
		"unknown":  {"ips_00000000-0000-4000-8000-000000000000", "initiate", 404, "NOT_FOUND"},
		"disabled": {branch, "initiate", 409, "IPSEC_DISABLED"},
		"warrior":  {road, "initiate", 409, "IPSEC_NOT_INITIATOR"},
	} {
		if w := act(c.id, c.action); w.Code != c.code || !strings.Contains(w.Body.String(), c.why) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	runtime.Connections = []string{road}
	if w := act(site, "initiate"); w.Code != 409 || !strings.Contains(w.Body.String(), "IPSEC_NOT_APPLIED") {
		t.Fatalf("not applied: %d %s", w.Code, w.Body)
	}
	runtime.ServiceActive = false
	if w := act(site, "terminate"); w.Code != 409 || !strings.Contains(w.Body.String(), "IPSEC_SERVICE_STOPPED") {
		t.Fatalf("stopped: %d %s", w.Code, w.Body)
	}
	if len(controlled) != 0 {
		t.Fatalf("a refused action reached strongSwan: %q", controlled)
	}
	runtime = generation.IPsecStatus{ServiceActive: true, Connections: []string{site, road}, SAs: []generation.IPsecSA{}}
	if w := act(site, "initiate"); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
		t.Fatalf("initiate %d %s", w.Code, w.Body)
	}
	if w := act(road, "terminate"); w.Code != 200 {
		t.Fatalf("terminate %d %s", w.Code, w.Body)
	}
	controlErr = errors.New("swanctl: establishing CHILD_SA failed")
	if w := act(site, "initiate"); w.Code != 502 || !strings.Contains(w.Body.String(), "IPSEC_CONTROL_FAILED") {
		t.Fatalf("failed initiate %d %s", w.Code, w.Body)
	}
	if !slices.Equal(controlled, []string{"initiate " + site, "terminate " + road, "initiate " + site}) {
		t.Fatalf("controlled %q", controlled)
	}
	if w := call("GET", "/api/v1/vpn/ipsec/"+site+"/initiate", "", ""); w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatalf("GET initiate %d", w.Code)
	}
	if w := call("POST", "/api/v1/vpn/ipsec/"+site+"/reload", "", ""); w.Code != 404 {
		t.Fatalf("unknown action %d", w.Code)
	}
	events, err := audits.ListEvents(ctx, 0, 100)
	count := map[string]int{}
	for _, e := range events {
		if (e.Action == audit.ActionIPsecInitiated || e.Action == audit.ActionIPsecTerminated) && e.Result != audit.ResultPending {
			count[string(e.Action)+" "+e.Target+" "+string(e.Result)]++
		}
	}
	if err != nil || count["vpn.ipsec.initiated "+site+" success"] != 1 || count["vpn.ipsec.terminated "+road+" success"] != 1 || count["vpn.ipsec.initiated "+site+" failure"] != 1 || len(count) != 3 {
		t.Fatalf("audit %v %v", err, count)
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
	for view, path := range map[string]string{"snapshotView": "/api/v1/config", "collectionView": "/api/v1/config/vpn/ipsec"} {
		sch, err := compiler.Compile("https://andaru.invalid/api/v1/schema#/$defs/" + view)
		if err != nil {
			t.Fatal(err)
		}
		served, _ := jsonschema.UnmarshalJSON(bytes.NewReader(call("GET", path, "", "").Body.Bytes()))
		if err = sch.Validate(served); err != nil {
			t.Fatalf("%s violates %s: %v", path, view, err)
		}
	}
	api.status = func(context.Context, string) error { return io.ErrUnexpectedEOF }
	if w := call("GET", "/api/v1/status/vpn/ipsec", "", ""); w.Code != 503 {
		t.Fatalf("status without realize %d %s", w.Code, w.Body)
	}
}

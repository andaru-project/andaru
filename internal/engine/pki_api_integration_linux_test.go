//go:build integration

package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/pki"
)

func TestPKIRoutes(t *testing.T) {
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
	events, err := bus.Open(bus.Options{Path: filepath.Join(t.TempDir(), "bus.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	var mu sync.Mutex
	var renewed []bus.Event
	stop, err := events.Subscribe("certificate/renewed", func(_ string, payload []byte) {
		var e bus.Event
		_ = json.Unmarshal(payload, &e)
		mu.Lock()
		renewed = append(renewed, e)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	configuration := newSavedHandler(store, host.ReadLinks, host.ReadRealizerFacts, http.NotFoundHandler(), nil)
	h := newHandlerWithDeps(handlerDeps{readLinks: host.ReadLinks, configuration: configuration,
		pkiAPI: &pkiAPI{store: store, coordinator: newSecurityCoordinator(nil, audits, stateDir), events: events, configuration: configuration}})
	call := func(method, path, body, tag string) *httptest.ResponseRecorder {
		r := rootRequest(method, path)
		r.Body = io.NopCloser(strings.NewReader(body))
		if tag != "" {
			r.Header.Set("If-Match", tag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	created := func(w *httptest.ResponseRecorder) (string, map[string]any) {
		t.Helper()
		if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), "PRIVATE KEY") {
			t.Fatalf("create: %d %s", w.Code, w.Body)
		}
		var v struct {
			Value   map[string]any `json:"value"`
			Version string         `json:"version"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Value["id"].(string), v.Value
	}

	rootID, root := created(call("POST", "/api/v1/pki/cas/generate", `{"name":"Root","commonName":"Andaru Root","organization":"Lab","keyAlgorithm":"ecdsa-p384","validDays":3650}`, ""))
	if !strings.HasPrefix(rootID, "ca_") || root["keyRef"].(map[string]any)["isSet"] != true {
		t.Fatalf("root CA %v", root)
	}
	interID, _ := created(call("POST", "/api/v1/pki/cas/generate", `{"name":"Issuing","commonName":"Andaru Issuing","keyAlgorithm":"ecdsa-p256","validDays":365,"parentCaId":"`+rootID+`"}`, ""))
	crtID, crt := created(call("POST", "/api/v1/pki/certificates/generate", `{"name":"fw","caId":"`+interID+`","commonName":"fw.example.com","sans":["fw.example.com","192.0.2.1"],"usage":"server","keyAlgorithm":"rsa-2048","validDays":90}`, ""))
	if crt["caId"] != interID || crt["keyAlgorithm"] != "rsa-2048" || !strings.Contains(crt["chainPem"].(string), "CERTIFICATE") || len(crt["sans"].([]any)) != 2 {
		t.Fatalf("certificate %v", crt)
	}
	pem := call("GET", "/api/v1/pki/certificates/"+crtID+"/pem", "", "")
	if pem.Code != 200 || pem.Header().Get("Content-Type") != "application/x-pem-file" || strings.Count(pem.Body.String(), "BEGIN CERTIFICATE") != 2 || strings.Contains(pem.Body.String(), "PRIVATE") {
		t.Fatalf("pem: %d %q %s", pem.Code, pem.Header().Get("Content-Type"), pem.Body)
	}
	if call("GET", "/api/v1/pki/cas/"+rootID+"/pem", "", "").Code != 200 || call("GET", "/api/v1/pki/cas/ca_missing/pem", "", "").Code != 404 {
		t.Fatal("CA pem")
	}

	ca, _ := pki.NewCA(pki.CARequest{CommonName: "External", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, nil)
	issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
	leaf, _ := pki.NewLeaf(pki.LeafRequest{CommonName: "ext.example.com", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, issuer)
	other, _ := pki.NewLeaf(pki.LeafRequest{CommonName: "other.example.com", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, issuer)
	body, _ := json.Marshal(map[string]string{"name": "ext", "certPem": string(leaf.CertPEM), "chainPem": string(ca.CertPEM), "privateKeyPem": string(leaf.KeyPEM)})
	importedID, _ := created(call("POST", "/api/v1/pki/certificates/import", string(body), ""))
	body, _ = json.Marshal(map[string]string{"name": "mismatch", "certPem": string(leaf.CertPEM), "privateKeyPem": string(other.KeyPEM)})
	if w := call("POST", "/api/v1/pki/certificates/import", string(body), ""); w.Code != 422 || strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Fatalf("mismatched import: %d %s", w.Code, w.Body)
	}
	tckID, _ := created(call("POST", "/api/v1/pki/tls-crypt-keys/generate", `{"name":"tc"}`, ""))
	for _, bad := range []struct{ path, body string }{
		{"/api/v1/pki/certificates/generate", `{"name":"x","caId":"ca_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f","commonName":"x","usage":"server","keyAlgorithm":"ecdsa-p256","validDays":1}`},
		{"/api/v1/pki/cas/generate", `{"name":"x","commonName":"x","keyAlgorithm":"ed25519","validDays":1}`},
		{"/api/v1/pki/tls-crypt-keys/generate", `{"name":"x","extra":1}`},
	} {
		if w := call("POST", bad.path, bad.body, ""); w.Code != 422 && w.Code != 400 {
			t.Fatalf("%s %s: %d %s", bad.path, bad.body, w.Code, w.Body)
		}
	}

	// The configuration routes list the objects and refuse a delete that would leave a reference.
	if w := call("GET", "/api/v1/config/pki/certificates", "", ""); w.Code != 200 || strings.Count(w.Body.String(), `"id":"crt_`) != 2 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	tag := func(path string) string { return call("GET", path, "", "").Header().Get("ETag") }
	if w := call("DELETE", "/api/v1/config/pki/cas/"+interID, "", tag("/api/v1/config/pki/cas/"+interID)); w.Code != 422 {
		t.Fatalf("delete referenced CA: %d %s", w.Code, w.Body)
	}
	if w := call("DELETE", "/api/v1/config/pki/tls-crypt-keys/"+tckID, "", tag("/api/v1/config/pki/tls-crypt-keys/"+tckID)); w.Code != 200 {
		t.Fatalf("delete tls-crypt key: %d %s", w.Code, w.Body)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(renewed) != 2 || renewed[0].Type != "certificate.renewed.v1" {
		t.Fatalf("renewed events %+v", renewed)
	}
	data, _ := json.Marshal(renewed[1].Data)
	if !strings.Contains(string(data), `"certificateId":"`+importedID+`"`) || !strings.Contains(string(data), `"source":"import"`) || strings.Contains(string(data), "PRIVATE") {
		t.Fatalf("import event %s", data)
	}
	list, err := audits.ListEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range list {
		seen[string(e.Action)+" "+e.Target+" "+string(e.Result)]++
	}
	for _, want := range []string{
		"certificate.created " + rootID + " success", "certificate.created " + crtID + " success",
		"certificate.imported " + importedID + " success", "certificate.deleted " + interID + " failure",
		"certificate.deleted " + tckID + " success",
	} {
		if seen[want] == 0 {
			t.Errorf("audit lacks %q: %v", want, seen)
		}
	}
}

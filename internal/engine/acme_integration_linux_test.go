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
	"testing"

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/pki"
)

// Over the real store: the ACME collections' routes keep their secrets out of every answer, a replace without them
// keeps them, and an order's first issuance creates its certificate and binds the order to it.
func TestACMERoutesAndIssuance(t *testing.T) {
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
	pkiH := &pkiAPI{store: store, coordinator: newSecurityCoordinator(nil, audits, stateDir), events: events}
	configuration := newSavedHandlerWithPKI(store, host.ReadLinks, host.ReadRealizerFacts, http.NotFoundHandler(), nil, pkiH)
	h := newHandlerWithDeps(handlerDeps{readLinks: host.ReadLinks, configuration: configuration, pkiAPI: pkiH})
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
	object := func(w *httptest.ResponseRecorder, status int) (string, string) {
		t.Helper()
		if w.Code != status || strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "cf-token") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var v struct {
			Value   map[string]any `json:"value"`
			Version string         `json:"version"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Value["id"].(string), v.Version
	}
	accountID, _ := object(call("POST", "/api/v1/config/acme/accounts", `{"name":"le","directoryUrl":"https://ca.example/directory","email":"a@example.com"}`, ""), http.StatusCreated)
	orderBody := `{"name":"edge","accountId":"` + accountID + `","domains":["edge.example.com"],"keyAlgorithm":"ecdsa-p256","enabled":true,` +
		`"challenge":{"type":"dns-01","dns01":{"provider":"cloudflare","cloudflare":{"apiTokenRef":{"secret":"acme_cf","set":"cf-token","expectedVersion":"0"}}}}}`
	orderID, version := object(call("POST", "/api/v1/config/acme/orders", orderBody, ""), http.StatusCreated)
	replace := `{"name":"edge","accountId":"` + accountID + `","domains":["edge.example.com"],"keyAlgorithm":"ecdsa-p256","enabled":true,` +
		`"challenge":{"type":"dns-01","dns01":{"provider":"cloudflare","cloudflare":{}}}}`
	object(call("PUT", "/api/v1/config/acme/orders/"+orderID, replace, `"`+orderID+`.`+version+`"`), http.StatusOK)
	if w := call("GET", "/api/v1/config/acme/orders", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"apiTokenRef":{"secret":"acme_cf","isSet":true`) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	settings, err := store.ACME(ctx)
	if err != nil || len(settings.Orders) != 1 || settings.Orders[0].DNS01.APIToken != "cf-token" || settings.Orders[0].Account.Key == "" {
		t.Fatalf("settings %+v %v", settings, err)
	}

	// Issue with a stand-in andaru-outbound.
	ca, _ := pki.NewCA(pki.CARequest{CommonName: "CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, nil)
	issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
	c := newACMECoordinator(store, stateDir, t.TempDir(), events, nil, audits, nil)
	if _, err := events.Subscribe(acme.ResultFilter, c.onResult); err != nil {
		t.Fatal(err)
	}
	if _, err := events.Subscribe(acme.OrderFilter, func(_ string, payload []byte) {
		var e struct{ Data acme.Job }
		_ = json.Unmarshal(payload, &e)
		go func() {
			res := signTestJob(issuer, ca.CertPEM, e.Data)
			raw, _ := bus.NewEvent(acme.ResultType, "andaru-outbound", res)
			_ = events.Publish(acme.ResultTopic(e.Data.OrderID), raw, false)
		}()
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.trigger(ctx, orderID, false); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	settings, _ = store.ACME(ctx)
	certID := settings.Orders[0].CertificateID
	if !strings.HasPrefix(certID, "crt_") || settings.Orders[0].CertificateNotAfter.IsZero() {
		t.Fatalf("order not bound: %+v", settings.Orders[0])
	}
	obj, err := store.PKIObject(ctx, "pki.certificates", certID)
	if err != nil || obj.Name != "edge" || obj.Key == "" {
		t.Fatalf("certificate %+v %v", obj, err)
	}
	if s, _ := c.status(ctx); s.Items[0].State != "succeeded" {
		t.Fatalf("status %+v", s)
	}
	// Renewing replaces it in place.
	if err := c.trigger(ctx, orderID, false); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	renewed, _ := store.PKIObject(ctx, "pki.certificates", certID)
	if renewed.CertPEM == obj.CertPEM || renewed.Key == obj.Key {
		t.Fatal("renewal did not replace the certificate")
	}
	// The bound certificate and the account cannot be deleted while the order names them.
	for _, path := range []string{"/api/v1/config/pki/certificates/" + certID, "/api/v1/config/acme/accounts/" + accountID} {
		w := call("GET", path, "", "")
		var v struct{ Version string }
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		id := path[strings.LastIndex(path, "/")+1:]
		if w := call("DELETE", path, "", `"`+id+`.`+v.Version+`"`); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "remains referenced") {
			t.Fatalf("delete %s: %d %s", path, w.Code, w.Body)
		}
	}
}

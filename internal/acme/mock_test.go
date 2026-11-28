package acme

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	xacme "golang.org/x/crypto/acme"

	"github.com/andaru-project/andaru/internal/pki"
)

// mockCA is an in-process RFC 8555 directory: enough of one for x/crypto/acme's client, validating HTTP-01 against
// the responder and DNS-01 against the test's TXT records.
type mockCA struct {
	t      *testing.T
	srv    *httptest.Server
	ca     pki.Issuer
	caPEM  []byte
	eabKID string
	eabKey []byte
	// http01 is the responder's address; txt resolves a name's TXT records.
	http01  string
	txt     func(name string) []string
	account crypto.PublicKey

	mu       sync.Mutex
	nonce    int
	accounts int
	authz    map[string]*mockAuthz
	order    *mockOrder
	sawEAB   bool
	// existingAccount makes /account return HTTP 200 (RFC 8555 existing account) instead of 201.
	existingAccount bool
	// spoofAuthzDomain, when set, replaces the first order authorization's domain with a hostile value.
	spoofAuthzDomain string
}

type mockAuthz struct {
	domain, token, status string
	wildcard              bool
}

type mockOrder struct {
	authz  []string
	status string
	cert   []byte
}

func newMockCA(t *testing.T, accountKey crypto.Signer) *mockCA {
	t.Helper()
	issued, err := pki.NewCA(pki.CARequest{CommonName: "Mock ACME CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := pki.ParseIssuer(issued.CertPEM, issued.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockCA{t: t, ca: issuer, caPEM: issued.CertPEM, authz: map[string]*mockAuthz{}, account: accountKey.Public()}
	m.srv = httptest.NewServer(m)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockCA) url(p string) string { return m.srv.URL + p }

func (m *mockCA) keyAuth(token string) string {
	thumb, err := xacme.JWKThumbprint(m.account)
	if err != nil {
		m.t.Fatal(err)
	}
	return token + "." + thumb
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (m *mockCA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nonce++
	w.Header().Set("Replay-Nonce", fmt.Sprintf("nonce%d", m.nonce))
	if r.URL.Path == "/directory" {
		writeJSONTest(w, 200, map[string]any{"newNonce": m.url("/nonce"), "newAccount": m.url("/account"), "newOrder": m.url("/order"),
			"revokeCert": m.url("/revoke"), "keyChange": m.url("/key"), "meta": map[string]any{"termsOfService": m.url("/tos"), "externalAccountRequired": m.eabKID != ""}})
		return
	}
	if r.URL.Path == "/nonce" {
		w.WriteHeader(200)
		return
	}
	var jws struct{ Protected, Payload, Signature string }
	body, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodPost || json.Unmarshal(body, &jws) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	payload, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
	switch {
	case r.URL.Path == "/account":
		var req struct {
			OnlyReturnExisting bool                                           `json:"onlyReturnExisting"`
			EAB                *struct{ Protected, Payload, Signature string } `json:"externalAccountBinding"`
		}
		_ = json.Unmarshal(payload, &req)
		if req.OnlyReturnExisting && m.existingAccount {
			w.Header().Set("Location", m.url("/acct/1"))
			writeJSONTest(w, 200, map[string]any{"status": "valid", "orders": m.url("/acct/1/orders")})
			return
		}
		if m.eabKID != "" && !m.existingAccount {
			if req.EAB == nil {
				problem(w, "externalAccountRequired", "EAB required")
				return
			}
			var prot struct {
				KID string `json:"kid"`
			}
			raw, _ := base64.RawURLEncoding.DecodeString(req.EAB.Protected)
			_ = json.Unmarshal(raw, &prot)
			mac := hmac.New(sha256.New, m.eabKey)
			mac.Write([]byte(req.EAB.Protected + "." + req.EAB.Payload))
			if prot.KID != m.eabKID || b64(mac.Sum(nil)) != req.EAB.Signature {
				problem(w, "unauthorized", "bad EAB")
				return
			}
			m.sawEAB = true
		}
		status := 201
		if m.existingAccount {
			status = 200
		} else {
			m.accounts++
		}
		w.Header().Set("Location", m.url("/acct/1"))
		writeJSONTest(w, status, map[string]any{"status": "valid", "orders": m.url("/acct/1/orders")})
	case r.URL.Path == "/order":
		var req struct {
			Identifiers []struct{ Type, Value string } `json:"identifiers"`
		}
		_ = json.Unmarshal(payload, &req)
		o := &mockOrder{status: "pending"}
		for i, id := range req.Identifiers {
			tok := b64([]byte(fmt.Sprintf("token-for-authorization-%d", i)))
			name := fmt.Sprintf("/authz/%d", i)
			val := id.Value
			if i == 0 && m.spoofAuthzDomain != "" {
				val = m.spoofAuthzDomain
			}
			wild := strings.HasPrefix(val, "*.")
			m.authz[name] = &mockAuthz{domain: strings.TrimPrefix(val, "*."), token: tok, status: "pending", wildcard: wild}
			o.authz = append(o.authz, m.url(name))
		}
		m.order = o
		w.Header().Set("Location", m.url("/order/1"))
		writeJSONTest(w, 201, m.orderJSON())
	case r.URL.Path == "/order/1":
		writeJSONTest(w, 200, m.orderJSON())
	case strings.HasPrefix(r.URL.Path, "/authz/"):
		writeJSONTest(w, 200, m.authzJSON(r.URL.Path))
	case strings.HasPrefix(r.URL.Path, "/chal/"):
		rest := strings.TrimPrefix(r.URL.Path, "/chal/")
		kind, name, _ := strings.Cut(rest, "/")
		a := m.authz["/authz/"+name]
		if a == nil {
			http.NotFound(w, r)
			return
		}
		if len(payload) > 0 && a.status == "pending" { // a POST-as-GET reads; {} responds to the challenge
			a.status = "invalid"
			if m.validate(kind, a) {
				a.status = "valid"
			}
		}
		writeJSONTest(w, 200, m.challengeJSON(kind, name, a))
	case r.URL.Path == "/finalize":
		var req struct {
			CSR string `json:"csr"`
		}
		_ = json.Unmarshal(payload, &req)
		der, _ := base64.RawURLEncoding.DecodeString(req.CSR)
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil || m.order.status != "ready" {
			problem(w, "badCSR", "bad csr or order not ready")
			return
		}
		serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
		tmpl := &x509.Certificate{SerialNumber: serial.Add(serial, big.NewInt(1)), Subject: csr.Subject, DNSNames: csr.DNSNames,
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		cert, err := x509.CreateCertificate(rand.Reader, tmpl, m.ca.Cert, csr.PublicKey, m.ca.Key)
		if err != nil {
			problem(w, "serverInternal", err.Error())
			return
		}
		m.order.cert = cert
		m.order.status = "valid"
		writeJSONTest(w, 200, m.orderJSON())
	case r.URL.Path == "/cert/1":
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		w.WriteHeader(200)
		_, _ = w.Write(append(encodeDER([][]byte{m.order.cert}), m.caPEM...))
	default:
		http.NotFound(w, r)
	}
}

func (m *mockCA) validate(kind string, a *mockAuthz) bool {
	switch kind {
	case "http-01":
		resp, err := http.Get("http://" + m.http01 + challengePath + a.token)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode == 200 && string(b) == m.keyAuth(a.token)
	case "dns-01":
		sum := sha256.Sum256([]byte(m.keyAuth(a.token)))
		for _, v := range m.txt("_acme-challenge." + a.domain) {
			if v == b64(sum[:]) {
				return true
			}
		}
	}
	return false
}

func (m *mockCA) orderJSON() map[string]any {
	ready := true
	for _, u := range m.order.authz {
		if m.authz[strings.TrimPrefix(u, m.srv.URL)].status != "valid" {
			ready = false
		}
	}
	if m.order.status == "pending" && ready {
		m.order.status = "ready"
	}
	o := map[string]any{"status": m.order.status, "authorizations": m.order.authz, "finalize": m.url("/finalize")}
	if m.order.status == "valid" {
		o["certificate"] = m.url("/cert/1")
	}
	return o
}

func (m *mockCA) authzJSON(path string) map[string]any {
	a := m.authz[path]
	name := strings.TrimPrefix(path, "/authz/")
	var chals []any
	for _, kind := range []string{"http-01", "dns-01"} {
		if kind == "http-01" && a.wildcard {
			continue
		}
		chals = append(chals, m.challengeJSON(kind, name, a))
	}
	return map[string]any{"identifier": map[string]string{"type": "dns", "value": a.domain}, "status": a.status, "challenges": chals, "wildcard": a.wildcard}
}

func (m *mockCA) challengeJSON(kind, name string, a *mockAuthz) map[string]any {
	status := a.status
	return map[string]any{"type": kind, "url": m.url("/chal/" + kind + "/" + name), "token": a.token, "status": status}
}

func problem(w http.ResponseWriter, typ, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(403)
	_ = json.NewEncoder(w).Encode(map[string]string{"type": "urn:ietf:params:acme:error:" + typ, "detail": detail})
}

func writeJSONTest(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

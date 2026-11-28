package acme

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/pki"
)

func accountKey(t *testing.T) (crypto.Signer, string) {
	t.Helper()
	k, err := pki.GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func newJob(t *testing.T, keyPEM, dir string, domains []string, chal Challenge) (Job, crypto.Signer) {
	t.Helper()
	leaf, err := pki.GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := NewCSR(leaf, domains)
	if err != nil {
		t.Fatal(err)
	}
	return Job{JobID: "job1", OrderID: "aco_1", AccountID: "aca_1", DirectoryURL: dir, Email: "admin@example.com", AccountKey: keyPEM,
		Domains: domains, CSR: csr, Challenge: chal}, leaf
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func checkIssued(t *testing.T, res Result, leaf crypto.Signer, domains []string) {
	t.Helper()
	if !res.OK {
		t.Fatalf("issue failed: %s", res.Error)
	}
	_, cert, err := pki.Inspect([]byte(res.CertPEM))
	if err != nil {
		t.Fatal(err)
	}
	if !pki.Matches(cert, leaf) || !slices.Equal(cert.DNSNames, domains) {
		t.Fatalf("certificate does not match the CSR: %v", cert.DNSNames)
	}
	if !strings.Contains(res.ChainPEM, "BEGIN CERTIFICATE") || res.AccountURI == "" {
		t.Fatalf("chain %q, account %q", res.ChainPEM, res.AccountURI)
	}
}

func TestIssueHTTP01WithEAB(t *testing.T) {
	key, keyPEM := accountKey(t)
	ca := newMockCA(t, key)
	ca.eabKID, ca.eabKey = "kid-1", []byte("0123456789abcdef0123456789abcdef")
	ca.http01 = freeAddr(t)
	job, leaf := newJob(t, keyPEM, ca.url("/directory"), []string{"edge.example.com", "www.example.com"}, Challenge{Type: "http-01"})
	job.EABKeyID, job.EABHMAC = "kid-1", base64.RawURLEncoding.EncodeToString(ca.eabKey)
	is := &Issuer{HTTP01Addr: ca.http01}
	res := is.Issue(context.Background(), job)
	checkIssued(t, res, leaf, job.Domains)
	if !ca.sawEAB || ca.accounts != 1 {
		t.Fatalf("EAB %v, accounts %d", ca.sawEAB, ca.accounts)
	}
	if res.JobID != "job1" || res.OrderID != "aco_1" || res.AccountID != "aca_1" {
		t.Fatalf("result identity: %+v", res)
	}
	// The responder is closed once the order is done.
	if c, err := net.DialTimeout("tcp", ca.http01, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the HTTP-01 responder is still listening")
	}
	// A known account is not registered again.
	job.AccountURI = res.AccountURI
	job2, leaf2 := newJob(t, keyPEM, ca.url("/directory"), []string{"edge.example.com"}, Challenge{Type: "http-01"})
	job2.AccountURI = res.AccountURI
	checkIssued(t, is.Issue(context.Background(), job2), leaf2, job2.Domains)
	if ca.accounts != 1 {
		t.Fatalf("registered %d times", ca.accounts)
	}
}

func TestIssueRefusedWithoutEAB(t *testing.T) {
	key, keyPEM := accountKey(t)
	ca := newMockCA(t, key)
	ca.eabKID, ca.eabKey = "kid-1", []byte("secret")
	job, _ := newJob(t, keyPEM, ca.url("/directory"), []string{"edge.example.com"}, Challenge{Type: "http-01"})
	res := (&Issuer{HTTP01Addr: freeAddr(t)}).Issue(context.Background(), job)
	if res.OK || !strings.Contains(res.Error, "externalAccountRequired") {
		t.Fatalf("got %+v", res)
	}
	if strings.Contains(res.Error, "secret") {
		t.Fatal("error leaks a credential")
	}
}

func TestResponderServesOnlyTokens(t *testing.T) {
	addr := freeAddr(t)
	r, err := startResponder(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	token := "abcdefghijklmnopqrstuvwxyz012345"
	if !r.add(token, "answer") || r.add("bad/token", "x") || r.add("short", "x") {
		t.Fatal("token validation")
	}
	get := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+addr+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("GET", challengePath+token); code != 200 || body != "answer" {
		t.Fatalf("token: %d %q", code, body)
	}
	for _, c := range []struct{ method, path string }{{"POST", challengePath + token}, {"GET", "/"}, {"GET", challengePath + "unknowntokenunknowntoken"},
		{"GET", challengePath + token + "/x"}, {"GET", "/.well-known/other/" + token}} {
		if code, _ := get(c.method, c.path); code != 404 {
			t.Fatalf("%s %s: %d", c.method, c.path, code)
		}
	}
}

// fakeDNS is a provider's zone: the TXT records it holds, which the resolver stub reads.
type fakeDNS struct {
	mu      sync.Mutex
	records map[string][]string
	deletes int
	headers []http.Header
}

func (f *fakeDNS) add(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.records == nil {
		f.records = map[string][]string{}
	}
	f.records[name] = append(f.records[name], value)
}

func (f *fakeDNS) lookup(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.records[name]), nil
}

func (f *fakeDNS) txt(name string) []string { v, _ := f.lookup(context.Background(), name); return v }

// providerAPI mocks the six providers' APIs, each only well enough for one zone, example.com.
func providerAPI(t *testing.T, dns *fakeDNS) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dns.mu.Lock()
		dns.headers = append(dns.headers, r.Header.Clone())
		dns.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		str := func(k string) string { s, _ := body[k].(string); return s }
		p := r.URL.Path
		switch {
		// cloudflare
		case r.Method == "GET" && p == "/cf/zones":
			if r.URL.Query().Get("name") == "example.com" {
				writeJSONTest(w, 200, map[string]any{"result": []any{map[string]string{"id": "z1"}}})
			} else {
				writeJSONTest(w, 200, map[string]any{"result": []any{}})
			}
		case r.Method == "POST" && p == "/cf/zones/z1/dns_records":
			dns.add(str("name"), str("content"))
			writeJSONTest(w, 200, map[string]any{"result": map[string]string{"id": "r1"}})
		case r.Method == "DELETE" && p == "/cf/zones/z1/dns_records/r1":
			dns.deletes++
		// hetzner
		case r.Method == "GET" && p == "/hz/zones":
			if r.URL.Query().Get("name") != "example.com" {
				http.NotFound(w, r)
				return
			}
			writeJSONTest(w, 200, map[string]any{"zones": []any{map[string]string{"id": "hz1", "name": "example.com"}}})
		case r.Method == "POST" && p == "/hz/records":
			dns.add(str("name")+".example.com", str("value"))
			writeJSONTest(w, 200, map[string]any{"record": map[string]string{"id": "hr1"}})
		case r.Method == "DELETE" && p == "/hz/records/hr1":
			dns.deletes++
		// digitalocean
		case r.Method == "GET" && strings.HasPrefix(p, "/do/domains/"):
			if p != "/do/domains/example.com" {
				http.NotFound(w, r)
				return
			}
			writeJSONTest(w, 200, map[string]any{"domain": map[string]string{"name": "example.com"}})
		case r.Method == "POST" && p == "/do/domains/example.com/records":
			dns.add(str("name")+".example.com", str("data"))
			writeJSONTest(w, 201, map[string]any{"domain_record": map[string]any{"id": 7}})
		case r.Method == "DELETE" && p == "/do/domains/example.com/records/7":
			dns.deletes++
			w.WriteHeader(204)
		// porkbun
		case strings.HasPrefix(p, "/pb/"):
			if str("apikey") != "pk" || str("secretapikey") != "sk" {
				writeJSONTest(w, 400, map[string]string{"status": "ERROR"})
				return
			}
			switch {
			case p == "/pb/dns/retrieve/example.com":
				writeJSONTest(w, 200, map[string]any{"status": "SUCCESS"})
			case strings.HasPrefix(p, "/pb/dns/retrieve/"):
				writeJSONTest(w, 400, map[string]any{"status": "ERROR"})
			case p == "/pb/dns/create/example.com":
				dns.add(str("name")+".example.com", str("content"))
				writeJSONTest(w, 200, map[string]any{"status": "SUCCESS", "id": 99})
			case p == "/pb/dns/delete/example.com/99":
				dns.deletes++
				writeJSONTest(w, 200, map[string]any{"status": "SUCCESS"})
			default:
				http.NotFound(w, r)
			}
		// acme-dns
		case r.Method == "POST" && p == "/acmedns/update":
			if r.Header.Get("X-Api-User") != "user" || r.Header.Get("X-Api-Key") != "pass" || str("subdomain") != "sub" {
				w.WriteHeader(401)
				return
			}
			dns.add("_acme-challenge.example.com", str("txt"))
			writeJSONTest(w, 200, map[string]string{"txt": str("txt")})
		// webhook
		case r.Method == "POST" && p == "/hook":
			if r.Header.Get("Authorization") != "Bearer hook" {
				w.WriteHeader(401)
				return
			}
			if str("action") == "present" {
				dns.add(str("fqdn"), str("value"))
			} else {
				dns.deletes++
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestIssueDNS01Providers(t *testing.T) {
	cases := []struct {
		dns01   DNS01
		header  string
		value   string
		deletes int
	}{
		{DNS01{Provider: "cloudflare", APIToken: "cf"}, "Authorization", "Bearer cf", 1},
		{DNS01{Provider: "hetzner", APIToken: "hz"}, "Auth-Api-Token", "hz", 1},
		{DNS01{Provider: "digitalocean", APIToken: "do"}, "Authorization", "Bearer do", 1},
		{DNS01{Provider: "porkbun", APIKey: "pk", SecretKey: "sk"}, "", "", 1},
		{DNS01{Provider: "acme-dns", Username: "user", Password: "pass", Subdomain: "sub"}, "X-Api-User", "user", 0},
		{DNS01{Provider: "webhook", Authorization: "Bearer hook", AllowPrivateIP: true}, "Authorization", "Bearer hook", 1},
	}
	for _, c := range cases {
		t.Run(c.dns01.Provider, func(t *testing.T) {
			key, keyPEM := accountKey(t)
			dns := &fakeDNS{}
			api := providerAPI(t, dns)
			ca := newMockCA(t, key)
			ca.txt = dns.txt
			p := c.dns01
			p.ServerURL, p.URL = api.URL+"/acmedns", api.URL+"/hook"
			job, leaf := newJob(t, keyPEM, ca.url("/directory"), []string{"edge.example.com"}, Challenge{Type: "dns-01", DNS01: &p})
			if p.Provider == "acme-dns" {
				job.Domains = []string{"example.com"}
				job.CSR, _ = NewCSR(leaf, job.Domains)
			}
			is := &Issuer{LookupTXT: dns.lookup, Poll: 10 * time.Millisecond,
				Endpoints: map[string]string{"cloudflare": api.URL + "/cf", "hetzner": api.URL + "/hz", "digitalocean": api.URL + "/do", "porkbun": api.URL + "/pb"}}
			checkIssued(t, is.Issue(context.Background(), job), leaf, job.Domains)
			if dns.deletes != c.deletes {
				t.Fatalf("deletes = %d, want %d", dns.deletes, c.deletes)
			}
			if c.header != "" && !slices.ContainsFunc(dns.headers, func(h http.Header) bool { return h.Get(c.header) == c.value }) {
				t.Fatalf("no request carried %s", c.header)
			}
		})
	}
}

func TestIssueDNS01Wildcard(t *testing.T) {
	key, keyPEM := accountKey(t)
	dns := &fakeDNS{}
	api := providerAPI(t, dns)
	ca := newMockCA(t, key)
	ca.txt = dns.txt
	p := DNS01{Provider: "cloudflare", APIToken: "cf"}
	job, leaf := newJob(t, keyPEM, ca.url("/directory"), []string{"*.example.com", "example.com"}, Challenge{Type: "dns-01", DNS01: &p})
	is := &Issuer{LookupTXT: dns.lookup, Poll: 10 * time.Millisecond, Endpoints: map[string]string{"cloudflare": api.URL + "/cf"}}
	checkIssued(t, is.Issue(context.Background(), job), leaf, job.Domains)
	if dns.deletes != 2 || len(dns.records["_acme-challenge.example.com"]) != 2 {
		t.Fatalf("deletes %d, records %v", dns.deletes, dns.records)
	}
}

func TestWebhookRefusesPrivateAddresses(t *testing.T) {
	key, keyPEM := accountKey(t)
	dns := &fakeDNS{}
	api := providerAPI(t, dns)
	ca := newMockCA(t, key)
	ca.txt = dns.txt
	p := DNS01{Provider: "webhook", URL: api.URL + "/hook", Authorization: "Bearer hook"}
	job, _ := newJob(t, keyPEM, ca.url("/directory"), []string{"edge.example.com"}, Challenge{Type: "dns-01", DNS01: &p})
	res := (&Issuer{LookupTXT: dns.lookup, Poll: 10 * time.Millisecond}).Issue(context.Background(), job)
	if res.OK || !strings.Contains(res.Error, "not a public address") {
		t.Fatalf("got %+v", res)
	}
	if len(dns.headers) != 0 {
		t.Fatal("the webhook was reached")
	}
	if strings.Contains(res.Error, "hook") && strings.Contains(res.Error, "Bearer") {
		t.Fatal("error leaks the authorization")
	}
}

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{"8.8.8.8": true, "2001:4860:4860::8888": true, "127.0.0.1": false, "::1": false, "10.1.2.3": false,
		"172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false, "100.64.0.1": false, "fd00::1": false, "fe80::1": false,
		"224.0.0.1": false, "ff02::1": false, "0.0.0.0": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %v", addr, got)
		}
	}
}

func TestPropagationTimeoutStillCleansUp(t *testing.T) {
	key, keyPEM := accountKey(t)
	dns := &fakeDNS{}
	api := providerAPI(t, dns)
	ca := newMockCA(t, key)
	ca.txt = dns.txt
	p := DNS01{Provider: "cloudflare", APIToken: "cf", PropagationTimeoutSeconds: 1}
	job, _ := newJob(t, keyPEM, ca.url("/directory"), []string{"edge.example.com"}, Challenge{Type: "dns-01", DNS01: &p})
	never := func(context.Context, string) ([]string, error) { return nil, nil }
	start := time.Now()
	res := (&Issuer{LookupTXT: never, Poll: 50 * time.Millisecond, Endpoints: map[string]string{"cloudflare": api.URL + "/cf"}}).Issue(context.Background(), job)
	if res.OK || !strings.Contains(res.Error, "not visible within 1s") {
		t.Fatalf("got %+v", res)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the propagation wait was not bounded")
	}
	if dns.deletes != 1 {
		t.Fatalf("deletes = %d", dns.deletes)
	}
}

func TestZones(t *testing.T) {
	if got := zones("_acme-challenge.a.b.example.com"); !slices.Equal(got, []string{"a.b.example.com", "b.example.com", "example.com"}) {
		t.Fatal(got)
	}
}

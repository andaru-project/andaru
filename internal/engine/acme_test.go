package engine

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/notify"
	"github.com/andaru-project/andaru/internal/pki"
	"github.com/andaru-project/andaru/internal/render"
)

type savedACMECert struct{ orderID, certID, certPEM, keyPEM string }

type fakeACMEStore struct {
	mu       sync.Mutex
	settings config.ACMESettings
	saved    []savedACMECert
}

func (f *fakeACMEStore) ACME(context.Context) (config.ACMESettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.settings
	out.Orders = slices.Clone(f.settings.Orders)
	return out, nil
}

func (f *fakeACMEStore) SaveACMECertificate(_ context.Context, orderID, certID, certPEM, _, keyPEM string) (config.SaveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, savedACMECert{orderID, certID, certPEM, keyPEM})
	for i := range f.settings.Orders {
		if f.settings.Orders[i].ID == orderID {
			f.settings.Orders[i].CertificateID = certID
		}
	}
	return config.SaveResult{}, nil
}

// acmeBench is the engine's bus with a stand-in for andaru-outbound that signs each order's CSR with a test CA.
type acmeBench struct {
	t        *testing.T
	events   *bus.Bus
	dir      string
	store    *fakeACMEStore
	c        *acmeCoordinator
	realized []string
	mu       sync.Mutex
	jobs     []acme.Job
	notices  []notify.Raise
	renewed  []string
	// answer turns a job into its result; nil signs the CSR.
	answer func(acme.Job) acme.Result
}

const testOrderID = "aco_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f"

func newACMEBench(t *testing.T, order config.ACMEOrder) *acmeBench {
	t.Helper()
	dir := t.TempDir()
	events, err := bus.Open(bus.Options{Path: filepath.Join(dir, "bus.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	b := &acmeBench{t: t, events: events, dir: dir, store: &fakeACMEStore{settings: config.ACMESettings{Orders: []config.ACMEOrder{order}, AccountIDs: []string{order.AccountID}}}}
	ca, _ := pki.NewCA(pki.CARequest{CommonName: "Test ACME CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, nil)
	issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
	sign := func(job acme.Job) acme.Result { return signTestJob(issuer, ca.CertPEM, job) }
	if _, err := events.Subscribe(acme.OrderFilter, func(topic string, payload []byte) {
		var e struct{ Data acme.Job }
		_ = json.Unmarshal(payload, &e)
		b.mu.Lock()
		b.jobs = append(b.jobs, e.Data)
		answer := b.answer
		b.mu.Unlock()
		go func() {
			if answer == nil {
				answer = sign
			}
			res := answer(e.Data)
			res.JobID, res.OrderID, res.AccountID, res.StagingTest = e.Data.JobID, e.Data.OrderID, e.Data.AccountID, e.Data.StagingTest
			raw, _ := bus.NewEvent(acme.ResultType, "andaru-outbound", res)
			_ = events.Publish(acme.ResultTopic(e.Data.OrderID), raw, false)
		}()
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := events.Subscribe("notification/acme", func(_ string, payload []byte) {
		var e struct {
			Type string
			Data notify.Raise
		}
		_ = json.Unmarshal(payload, &e)
		b.mu.Lock()
		b.notices = append(b.notices, e.Data)
		b.mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := events.Subscribe("certificate/renewed", func(_ string, payload []byte) {
		var e struct {
			Data map[string]string
		}
		_ = json.Unmarshal(payload, &e)
		b.mu.Lock()
		b.renewed = append(b.renewed, e.Data["certificateId"]+" "+e.Data["source"])
		b.mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	b.c = newACMECoordinator(b.store, filepath.Join(dir, "state"), filepath.Join(dir, "root"), events, nil, nil, func(_ context.Context, verb string, args ...string) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.realized = append(b.realized, strings.TrimSpace(verb+" "+strings.Join(args, " ")))
		return nil
	})
	if _, err := events.Subscribe(acme.ResultFilter, b.c.onResult); err != nil {
		t.Fatal(err)
	}
	return b
}

func dnsTestOrder() config.ACMEOrder {
	return config.ACMEOrder{ID: testOrderID, Name: "edge", AccountID: "aca_1", Domains: []string{"edge.example.com", "www.example.com"}, KeyAlgorithm: "ecdsa-p256",
		RenewBeforeDays: 30, Enabled: true, Challenge: "dns-01", DNS01: &acme.DNS01{Provider: "cloudflare", APIToken: "cf-secret"},
		Account: config.ACMEAccount{ID: "aca_1", DirectoryURL: "https://ca.example/dir", Email: "a@example.com", Key: "ACCOUNT KEY"}}
}

func (b *acmeBench) state(t *testing.T) acmeStatusItem {
	t.Helper()
	s, err := b.c.status(context.Background())
	if err != nil || len(s.Items) != 1 {
		t.Fatalf("status %+v %v", s, err)
	}
	return s.Items[0]
}

// The first issuance keeps the key in the engine, saves the certificate under a new ID bound to the order, and
// publishes certificate/renewed; the job crosses the bus non-durably.
func TestACMEInitialIssuanceBindsCertificate(t *testing.T) {
	b := newACMEBench(t, dnsTestOrder())
	if err := b.c.trigger(context.Background(), testOrderID, false); err != nil {
		t.Fatal(err)
	}
	b.c.wg.Wait()
	if len(b.store.saved) != 1 {
		t.Fatalf("saved %d", len(b.store.saved))
	}
	saved := b.store.saved[0]
	if !strings.HasPrefix(saved.certID, "crt_") || saved.orderID != testOrderID {
		t.Fatalf("saved %+v", saved)
	}
	if _, err := pki.Import([]byte(saved.certPEM), nil, []byte(saved.keyPEM)); err != nil {
		t.Fatalf("saved certificate and key: %v", err)
	}
	job := b.jobs[0]
	if job.DirectoryURL != "https://ca.example/dir" || job.StagingTest || job.Challenge.DNS01.APIToken != "cf-secret" || strings.Contains(string(mustJSON(t, job)), "PRIVATE KEY") {
		t.Fatalf("job %+v", job)
	}
	if !slices.Equal(b.renewed, []string{saved.certID + " acme"}) {
		t.Fatalf("renewed %v", b.renewed)
	}
	if st := b.state(t); st.State != "succeeded" || st.LastSucceededAt == nil || st.CertificateID != saved.certID {
		t.Fatalf("status %+v", st)
	}
	if n := b.notices[len(b.notices)-1]; n.Key != "acme/"+testOrderID || n.Severity != "" {
		t.Fatalf("notification not cleared: %+v", b.notices)
	}
	s, _ := b.c.status(context.Background())
	if s.Accounts[0].RegisteredURI != "https://ca.example/acct/1" || s.Accounts[0].Status != "valid" {
		t.Fatalf("accounts %+v", s.Accounts)
	}
	// The registered account is reused, and the state survives a restart.
	raw, err := os.ReadFile(filepath.Join(b.dir, "state", "acme", "state.json"))
	if err != nil || strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("state.json %s %v", raw, err)
	}
	if err := b.c.trigger(context.Background(), testOrderID, false); err != nil {
		t.Fatal(err)
	}
	b.c.wg.Wait()
	if b.jobs[1].AccountURI != "https://ca.example/acct/1" || len(b.store.saved) != 2 || b.store.saved[1].certID != saved.certID {
		t.Fatalf("renewal job %+v saved %+v", b.jobs[1], b.store.saved)
	}
	db, err := sql.Open("sqlite", filepath.Join(b.dir, "bus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE topic LIKE 'acme-%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d ACME events in bus.db (%v)", n, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A staging test orders from Let's Encrypt staging and changes no certificate.
func TestACMEStagingTestMutatesNothing(t *testing.T) {
	order := dnsTestOrder()
	order.Enabled = false
	order.Account.EABKeyID, order.Account.EABHMAC = "kid", "hmac"
	b := newACMEBench(t, order)
	if err := b.c.trigger(context.Background(), testOrderID, false); err != errACMEDisabled {
		t.Fatalf("disabled renew: %v", err)
	}
	if err := b.c.trigger(context.Background(), testOrderID, true); err != nil {
		t.Fatal(err)
	}
	b.c.wg.Wait()
	if job := b.jobs[0]; job.DirectoryURL != acme.StagingDirectory || !job.StagingTest || job.EABKeyID != "" {
		t.Fatalf("job %+v", job)
	}
	if len(b.store.saved) != 0 || len(b.renewed) != 0 || len(b.notices) != 0 {
		t.Fatalf("staging mutated: saved %d renewed %v notices %v", len(b.store.saved), b.renewed, b.notices)
	}
	if st := b.state(t); st.State != "succeeded" || st.LastSucceededAt != nil {
		t.Fatalf("status %+v", st)
	}
	s, _ := b.c.status(context.Background())
	if s.Accounts[0].Status != "unregistered" {
		t.Fatalf("staging registered the production account: %+v", s.Accounts)
	}
}

// A certificate for another key, or one missing a requested name, is refused and the order fails with a notification.
func TestACMERefusesMismatchedCertificates(t *testing.T) {
	for name, answer := range map[string]func(acme.Job) acme.Result{
		"other key": func(job acme.Job) acme.Result {
			leaf, _ := pki.NewCA(pki.CARequest{CommonName: "edge.example.com", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, nil)
			return acme.Result{OK: true, CertPEM: string(leaf.CertPEM)}
		},
		"missing name":   nil, // set below: signs only the first domain
		"outbound error": func(acme.Job) acme.Result { return acme.Result{Error: "urn:ietf:params:acme:error:dns: no TXT record"} },
	} {
		t.Run(name, func(t *testing.T) {
			b := newACMEBench(t, dnsTestOrder())
			if answer == nil {
				ca, _ := pki.NewCA(pki.CARequest{CommonName: "CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, nil)
				issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
				answer = func(job acme.Job) acme.Result {
					csr, _ := x509.ParseCertificateRequest(job.CSR)
					tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), DNSNames: csr.DNSNames[:1], NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
					der, _ := x509.CreateCertificate(rand.Reader, tmpl, issuer.Cert, csr.PublicKey, issuer.Key)
					cert, _ := x509.ParseCertificate(der)
					return acme.Result{OK: true, CertPEM: string(pki.Encode(cert)), ChainPEM: string(ca.CertPEM)}
				}
			}
			b.answer = answer
			if err := b.c.trigger(context.Background(), testOrderID, false); err != nil {
				t.Fatal(err)
			}
			b.c.wg.Wait()
			if len(b.store.saved) != 0 || len(b.renewed) != 0 {
				t.Fatal("a refused certificate was saved")
			}
			st := b.state(t)
			if st.State != "failed" || st.LastError == "" {
				t.Fatalf("status %+v", st)
			}
			if n := b.notices[len(b.notices)-1]; n.Severity != notify.Warning || n.Key != "acme/"+testOrderID || n.Requires != string(authz.CertificatesRead) {
				t.Fatalf("notices %+v", b.notices)
			}
		})
	}
}

// An HTTP-01 order opens the redirect on the live generation's interfaces for it, and always closes it again; one not
// yet applied does not start.
func TestACMEHTTP01OpensLiveInterfaces(t *testing.T) {
	order := dnsTestOrder()
	order.Challenge, order.DNS01, order.ListenSegments = "http-01", nil, []string{"seg_1"}
	b := newACMEBench(t, order)
	var notReady *acmeNotReady
	if err := b.c.trigger(context.Background(), testOrderID, false); !errors.As(err, &notReady) {
		t.Fatalf("unapplied order: %v", err)
	}
	dir := generation.Dir(b.c.stateDir)
	script := "# interface ens18 52:54:00:00:00:01\n" + render.ACMEHTTP01Prefix + testOrderID + " ens18 ens19\ntable inet andaru {}\n"
	id, err := generation.Create(dir, generation.Manifest{SavedVersion: "5", Files: []generation.FileEntry{{Path: generation.FilterEntry, Mode: "0644"}}},
		map[string][]byte{generation.FilterEntry: []byte(script)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"confirmed": id, "lastKnownGood": nil, "pending": nil, "live": id})
	if err := os.WriteFile(filepath.Join(dir, "pointers.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b.answer = func(acme.Job) acme.Result { return acme.Result{Error: "unauthorized"} }
	if err := b.c.trigger(context.Background(), testOrderID, false); err != nil {
		t.Fatal(err)
	}
	b.c.wg.Wait()
	if !slices.Equal(b.realized, []string{"acme-http01-open ens18 ens19", "acme-http01-close"}) {
		t.Fatalf("realized %q", b.realized)
	}
}

// The daily check renews what is due and warns of a certificate about to expire; safe mode holds it.
func TestACMESchedulerRenewsDueOrders(t *testing.T) {
	order := dnsTestOrder()
	order.CertificateID, order.CertificateNotAfter = "crt_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", time.Now().Add(10*24*time.Hour)
	fresh := dnsTestOrder()
	fresh.ID, fresh.CertificateNotAfter = "aco_00000000-0000-4000-8000-000000000002", time.Now().Add(80*24*time.Hour)
	b := newACMEBench(t, order)
	b.store.settings.Orders = append(b.store.settings.Orders, fresh)
	b.answer = func(acme.Job) acme.Result { return acme.Result{Error: "rate limited"} }
	safe := filepath.Join(b.c.systemRoot, generation.SafeModeBootFile)
	if err := os.MkdirAll(filepath.Dir(safe), 0o755); err != nil || os.WriteFile(safe, nil, 0o644) != nil {
		t.Fatal("safe mode marker")
	}
	b.c.check(context.Background())
	b.c.wg.Wait()
	if len(b.jobs) != 0 || len(b.notices) != 0 {
		t.Fatal("safe mode did not hold the scheduler")
	}
	_ = os.Remove(safe)
	b.c.check(context.Background())
	b.c.wg.Wait()
	if len(b.jobs) != 1 || b.jobs[0].OrderID != testOrderID {
		t.Fatalf("jobs %+v", b.jobs)
	}
	if len(b.notices) != 2 || !strings.Contains(b.notices[0].Subject, "expires") || !strings.Contains(b.notices[1].Detail, "rate limited") {
		t.Fatalf("notices %+v", b.notices)
	}
	// Unresolved secrets, left by a restore without secrets, fail the order instead of sending it.
	b.store.settings.Orders[0].Unresolved = []string{"/acme/orders/x/challenge/dns01/cloudflare/apiTokenRef"}
	b.c.check(context.Background())
	b.c.wg.Wait()
	if s, _ := b.c.status(context.Background()); len(b.jobs) != 1 || s.Items[0].State != "failed" || !strings.Contains(s.Items[0].LastError, "not restored") {
		t.Fatalf("unresolved order ran: %d jobs", len(b.jobs))
	}
}

// The routes answer 202, 404 and 409 as documented.
func TestACMEAPI(t *testing.T) {
	b := newACMEBench(t, dnsTestOrder())
	block := make(chan struct{})
	b.answer = func(acme.Job) acme.Result { <-block; return acme.Result{Error: "stopped"} }
	api := &acmeAPI{acme: b.c}
	post := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodPost, path, nil), authz.CertificatesManage))
		return rec
	}
	if rec := post(acmeOrdersPrefix + "aco_00000000-0000-4000-8000-000000000009/renew"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown order: %d %s", rec.Code, rec.Body)
	}
	if rec := post(acmeOrdersPrefix + testOrderID + "/renew"); rec.Code != http.StatusAccepted || rec.Body.String() != `{"orderId":"`+testOrderID+`","started":true}`+"\n" {
		t.Fatalf("renew: %d %q", rec.Code, rec.Body)
	}
	if rec := post(acmeOrdersPrefix + testOrderID + "/test-staging"); rec.Code != http.StatusConflict {
		t.Fatalf("running: %d %s", rec.Code, rec.Body)
	}
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, withActor(httptest.NewRequest(http.MethodGet, acmeStatusPath, nil), authz.CertificatesRead))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"running"`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	close(block)
	b.c.wg.Wait()
	if rec := post(acmeOrdersPrefix + testOrderID + "/test-staging"); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"stagingTest":true`) {
		t.Fatalf("staging: %d %s", rec.Code, rec.Body)
	}
	b.c.wg.Wait()
}

// ACME objects take certificates.manage, whichever route carries the change.
func TestACMEChangesNeedCertificatesManage(t *testing.T) {
	h := newConfigurationHandler(nil)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/config/changes", strings.NewReader(`{"operations":[{"op":"create","collection":"acme.accounts","value":{"name":"x","directoryUrl":"https://ca.example/d","email":"a@b.c"}}]}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/config/acme/accounts", strings.NewReader(`{"name":"x","directoryUrl":"https://ca.example/d","email":"a@b.c"}`)),
	} {
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, withActor(req, authz.ConfigurationRead, authz.ConfigurationWrite))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "certificates.manage") {
			t.Fatalf("%s: %d %s", req.URL.Path, rec.Code, rec.Body)
		}
	}
	routes := map[string]authz.Capability{}
	for _, r := range buildEngineRoutes() {
		routes[r.Method+" "+r.Path] = r.Capability
	}
	for route, want := range map[string]authz.Capability{
		"GET /api/v1/config/acme/orders": authz.ConfigurationRead, "POST /api/v1/config/acme/orders": authz.CertificatesManage,
		"PUT /api/v1/config/acme/accounts/{id}": authz.CertificatesManage, "DELETE /api/v1/config/acme/orders/{id}": authz.CertificatesManage,
		"GET /api/v1/status/acme": authz.CertificatesRead, "POST /api/v1/acme/orders/{id}/renew": authz.CertificatesManage,
		"POST /api/v1/acme/orders/{id}/test-staging": authz.CertificatesManage,
	} {
		if routes[route] != want {
			t.Errorf("%s needs %q, want %q", route, routes[route], want)
		}
	}
}

// signTestJob is a CA's answer to a job: a certificate for its CSR's key and names, with the CA as the chain.
func signTestJob(issuer pki.Issuer, caPEM []byte, job acme.Job) acme.Result {
	res := acme.Result{JobID: job.JobID, OrderID: job.OrderID, AccountID: job.AccountID, StagingTest: job.StagingTest}
	csr, err := x509.ParseCertificateRequest(job.CSR)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<60))
	tmpl := &x509.Certificate{SerialNumber: serial.Add(serial, big.NewInt(1)), Subject: csr.Subject, DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer.Cert, csr.PublicKey, issuer.Key)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	cert, _ := x509.ParseCertificate(der)
	res.OK, res.CertPEM, res.ChainPEM, res.AccountURI = true, string(pki.Encode(cert)), string(caPEM), "https://ca.example/acct/1"
	return res
}

package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/notify"
	"github.com/andaru-project/andaru/internal/pki"
	"github.com/andaru-project/andaru/internal/render"
)

const (
	acmeStatusPath    = "/api/v1/status/acme"
	acmeOrdersPrefix  = "/api/v1/acme/orders/"
	acmeNoticeSource  = "acme"
	acmeJobTimeout    = 15 * time.Minute
	acmeFirstCheck    = 2 * time.Minute
	acmeCheckEvery    = 24 * time.Hour
	acmeExpiryWarning = 14 * 24 * time.Hour
	acmeRealizeLimit  = 30 * time.Second
)

// acmeKinds maps the ACME collections' path names to their collections.
var acmeKinds = map[string]string{"accounts": "acme.accounts", "orders": "acme.orders"}

var (
	errACMENotFound = errors.New("the ACME order does not exist")
	errACMEDisabled = errors.New("the ACME order is disabled")
	errACMERunning  = errors.New("the ACME order is already running")
)

// acmeNotReady is an order that cannot start: its secrets are unresolved, or its HTTP-01 segments are not applied.
type acmeNotReady struct{ reason string }

func (e *acmeNotReady) Error() string { return e.reason }

// acmeStore is what the coordinator reads and saves of the configuration.
type acmeStore interface {
	ACME(ctx context.Context) (config.ACMESettings, error)
	SaveACMECertificate(ctx context.Context, orderID, certID, accountID, certPEM, chainPEM, keyPEM string) (config.SaveResult, error)
}

// acmeOrderState and acmeAccountState are what the orders' runs left: kept beside the store, in state.json, so
// renewals never churn the configuration's versions.
type acmeOrderState struct {
	State           string     `json:"state"`
	LastAttemptAt   *time.Time `json:"lastAttemptAt,omitempty"`
	LastSucceededAt *time.Time `json:"lastSucceededAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
}

type acmeAccountState struct {
	RegisteredURI string `json:"registeredUri,omitempty"`
	// Binding is the directory and key the URI was registered with, hashed: another of either registers again.
	Binding string `json:"binding,omitempty"`
}

type acmeState struct {
	Orders   map[string]*acmeOrderState   `json:"orders"`
	Accounts map[string]*acmeAccountState `json:"accounts"`
}

// acmeRun is an order in flight. Its private key stays in the engine's memory; only the CSR goes out.
type acmeRun struct {
	jobID   string
	keyPEM  string
	order   config.ACMEOrder
	staging bool
	binding string
	results chan acme.Result
}

// acmeCoordinator runs the ACME orders: on demand, and daily for the ones due a renewal. andaru-outbound does the
// talking; the engine keeps the key, checks what comes back and saves it as the order's certificate.
type acmeCoordinator struct {
	store      acmeStore
	stateDir   string
	systemRoot string
	certs      *certSync
	audits     *audit.Store
	// publish sends a job non-durably; notice raises or clears one of the source's notifications.
	publish func(topic string, payload []byte) error
	notice  func(notify.Raise) error
	// realize runs a realize verb on no generation.
	realize func(ctx context.Context, verb string, args ...string) error
	events  *bus.Bus
	now     func() time.Time
	timeout time.Duration

	mu       sync.Mutex
	http01Mu sync.Mutex
	state    acmeState
	runs     map[string]*acmeRun
	lifetime context.Context
	wg       sync.WaitGroup
}

func newACMECoordinator(store acmeStore, stateDir, systemRoot string, events *bus.Bus, certs *certSync, audits *audit.Store,
	realize func(ctx context.Context, verb string, args ...string) error) *acmeCoordinator {
	c := &acmeCoordinator{store: store, stateDir: stateDir, systemRoot: systemRoot, certs: certs, audits: audits, realize: realize, events: events,
		now: time.Now, timeout: acmeJobTimeout, runs: map[string]*acmeRun{}, lifetime: context.Background()}
	if events != nil {
		c.publish = func(topic string, payload []byte) error { return events.Publish(topic, payload, false) }
		c.notice = func(r notify.Raise) error { return notify.Publish(events, acmeNoticeSource, r) }
	}
	c.state = c.load()
	return c
}

func (c *acmeCoordinator) statePath() string { return filepath.Join(c.stateDir, "acme", "state.json") }

// load reads state.json; a run it says was running ended with the engine that ran it.
func (c *acmeCoordinator) load() acmeState {
	s := acmeState{Orders: map[string]*acmeOrderState{}, Accounts: map[string]*acmeAccountState{}}
	raw, err := os.ReadFile(c.statePath())
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("acme: state unreadable; starting afresh", "error", err)
	}
	if s.Orders == nil {
		s.Orders = map[string]*acmeOrderState{}
	}
	if s.Accounts == nil {
		s.Accounts = map[string]*acmeAccountState{}
	}
	for _, o := range s.Orders {
		if o.State == "running" {
			o.State, o.LastError = "failed", "The engine stopped while the order ran."
		}
	}
	return s
}

// saveLocked writes state.json atomically; it holds no secret.
func (c *acmeCoordinator) saveLocked() {
	raw, err := json.Marshal(c.state)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(c.statePath()), 0o700)
	}
	if err == nil {
		err = writeFileAtomic(c.statePath(), raw, 0o600)
	}
	if err != nil {
		slog.Warn("acme: state not saved", "error", err)
	}
}

// run closes any HTTP-01 redirect a stopped engine left open, then checks the orders shortly after startup and daily.
func (c *acmeCoordinator) run(ctx context.Context) {
	c.mu.Lock()
	c.lifetime = ctx
	c.mu.Unlock()
	c.http01Mu.Lock()
	c.closeHTTP01()
	c.http01Mu.Unlock()
	timer := time.NewTimer(acmeFirstCheck)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			c.wg.Wait()
			return
		case <-timer.C:
		}
		c.check(ctx)
		timer.Reset(acmeCheckEvery)
	}
}

func (c *acmeCoordinator) closeHTTP01() {
	if c.realize == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), acmeRealizeLimit)
	defer cancel()
	if err := c.realize(ctx, "acme-http01-close"); err != nil {
		slog.Warn("acme: close the HTTP-01 redirect", "error", err)
	}
}

// check renews the enabled orders that are due, and warns of certificates about to expire. Safe mode holds it.
func (c *acmeCoordinator) check(ctx context.Context) {
	if generation.ReadSafeMode(c.systemRoot, c.stateDir).Active {
		slog.Info("acme: safe mode is active; renewals wait")
		return
	}
	settings, err := c.store.ACME(ctx)
	if err != nil {
		slog.Warn("acme: read the orders", "error", err)
		return
	}
	now := c.now()
	for _, o := range settings.Orders {
		if !o.Enabled {
			continue
		}
		if !o.CertificateNotAfter.IsZero() && !now.Before(expiryWarnAt(o)) {
			c.raise(o, notify.Warning, fmt.Sprintf("The certificate of ACME order %s expires on %s", o.Name, o.CertificateNotAfter.UTC().Format(time.DateOnly)),
				"It has not been renewed yet. Check the order's last error under System, Certificates.")
		}
		if !o.CertificateNotAfter.IsZero() && now.Before(renewAt(o)) {
			continue
		}
		if err := c.start(o, false); err != nil && !errors.Is(err, errACMERunning) {
			slog.Warn("acme: renewal not started", "order", o.ID, "error", err)
		}
	}
}

func renewAt(o config.ACMEOrder) time.Time {
	target := o.CertificateNotAfter.Add(-time.Duration(o.RenewBeforeDays) * 24 * time.Hour)
	if !o.CertificateNotBefore.IsZero() && o.CertificateNotAfter.After(o.CertificateNotBefore) {
		// Clamp within the certificate's lifetime (at least two-thirds elapsed) so short-lived certificates
		// whose lifetime <= renewBeforeDays do not immediately renew on every daily pass.
		earliest := o.CertificateNotBefore.Add(o.CertificateNotAfter.Sub(o.CertificateNotBefore) * 2 / 3)
		if target.Before(earliest) {
			return earliest
		}
	}
	return target
}

func expiryWarnAt(o config.ACMEOrder) time.Time {
	warnWindow := acmeExpiryWarning
	if !o.CertificateNotBefore.IsZero() && o.CertificateNotAfter.After(o.CertificateNotBefore) {
		if third := o.CertificateNotAfter.Sub(o.CertificateNotBefore) / 3; third < warnWindow {
			warnWindow = third
		}
	}
	return o.CertificateNotAfter.Add(-warnWindow)
}

// trigger starts an order now, as a user asks.
func (c *acmeCoordinator) trigger(ctx context.Context, orderID string, staging bool) error {
	if !staging && generation.ReadSafeMode(c.systemRoot, c.stateDir).Active {
		return &acmeNotReady{"Safe mode is active; renewals wait until the system returns to normal mode."}
	}
	settings, err := c.store.ACME(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(settings.Orders, func(o config.ACMEOrder) bool { return o.ID == orderID })
	if i < 0 {
		return errACMENotFound
	}
	if !settings.Orders[i].Enabled && !staging {
		return errACMEDisabled
	}
	return c.start(settings.Orders[i], staging)
}

// start prepares the order's key, CSR and job, and runs it in the background. A failure to prepare is the order's.
func (c *acmeCoordinator) start(o config.ACMEOrder, staging bool) error {
	c.mu.Lock()
	if c.runs[o.ID] != nil {
		c.mu.Unlock()
		return errACMERunning
	}
	// Reserve the slot before releasing c.mu so key generation and disk reads never block onResult or status.
	c.runs[o.ID] = &acmeRun{}
	var cached *acmeAccountState
	if a := c.state.Accounts[o.AccountID]; a != nil {
		cp := *a
		cached = &cp
	}
	c.mu.Unlock()

	job, run, ifaces, err := c.prepare(o, staging, cached)
	if err != nil {
		res := acme.Result{Error: err.Error()}
		c.mu.Lock()
		delete(c.runs, o.ID)
		c.recordLocked(o, staging, res)
		c.mu.Unlock()
		c.notifyRun(o, staging, res)
		return err
	}
	c.mu.Lock()
	c.runs[o.ID] = run
	now := c.now()
	st := c.orderStateLocked(o.ID)
	st.State, st.LastAttemptAt = "running", &now
	c.saveLocked()
	life := c.lifetime
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		c.finish(life, run, c.execute(life, run, job, ifaces))
	}()
	return nil
}

func (c *acmeCoordinator) orderStateLocked(id string) *acmeOrderState {
	st := c.state.Orders[id]
	if st == nil {
		st = &acmeOrderState{State: "idle"}
		c.state.Orders[id] = st
	}
	return st
}

func (c *acmeCoordinator) prepare(o config.ACMEOrder, staging bool, cached *acmeAccountState) (acme.Job, *acmeRun, []string, error) {
	if len(o.Unresolved) > 0 {
		return acme.Job{}, nil, nil, &acmeNotReady{"The order's secrets were not restored; set them again: " + strings.Join(o.Unresolved, ", ") + "."}
	}
	if o.Account.Key == "" {
		return acme.Job{}, nil, nil, &acmeNotReady{"The order's account has no key."}
	}
	var ifaces []string
	if o.Challenge == "http-01" {
		var err error
		if ifaces, err = liveHTTP01Interfaces(c.stateDir, o.ID); err != nil {
			return acme.Job{}, nil, nil, err
		}
	}
	key, err := pki.GenerateKey(o.KeyAlgorithm)
	if err != nil {
		return acme.Job{}, nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return acme.Job{}, nil, nil, err
	}
	csr, err := acme.NewCSR(key, o.Domains)
	if err != nil {
		return acme.Job{}, nil, nil, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return acme.Job{}, nil, nil, err
	}
	job := acme.Job{JobID: hex.EncodeToString(id[:]), OrderID: o.ID, AccountID: o.AccountID, StagingTest: staging, DirectoryURL: o.Account.DirectoryURL,
		Email: o.Account.Email, AccountKey: o.Account.Key, EABKeyID: o.Account.EABKeyID, EABHMAC: o.Account.EABHMAC, Domains: o.Domains, CSR: csr,
		Challenge: acme.Challenge{Type: o.Challenge, DNS01: o.DNS01}}
	sum := sha256.Sum256([]byte(o.Account.DirectoryURL + "\x00" + o.Account.Key))
	binding := hex.EncodeToString(sum[:])
	if staging {
		// A staging test registers its own account at Let's Encrypt's staging directory, which knows no EAB.
		job.DirectoryURL, job.EABKeyID, job.EABHMAC, binding = acme.StagingDirectory, "", "", ""
	} else if cached != nil && cached.Binding == binding {
		job.AccountURI = cached.RegisteredURI
	}
	run := &acmeRun{jobID: job.JobID, keyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), order: o, staging: staging,
		binding: binding, results: make(chan acme.Result, 1)}
	return job, run, ifaces, nil
}

// liveHTTP01Interfaces are the interfaces the live generation's filter redirects port 80 on for orderID, as its
// comment names them: the order's segments as applied, never as saved.
func liveHTTP01Interfaces(stateDir, orderID string) ([]string, error) {
	notApplied := &acmeNotReady{"The HTTP-01 order and its segments are not applied yet; apply the configuration first."}
	dir := generation.Dir(stateDir)
	p, err := generation.ReadPointers(dir)
	if err != nil || p.Live == nil {
		return nil, notApplied
	}
	g, err := generation.Read(dir, *p.Live)
	if err != nil {
		return nil, notApplied
	}
	contents, err := generation.Contents(dir, g)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(contents[generation.FilterEntry]))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		rest, ok := strings.CutPrefix(scanner.Text(), render.ACMEHTTP01Prefix+orderID)
		if !ok || rest != "" && rest[0] != ' ' {
			continue
		}
		ifaces := strings.Fields(rest)
		if len(ifaces) == 0 || slices.ContainsFunc(ifaces, func(n string) bool { return !generation.ValidIfname(n) }) {
			return nil, &acmeNotReady{"None of the HTTP-01 order's applied segments has a usable interface."}
		}
		return ifaces, nil
	}
	return nil, notApplied
}

// execute opens the HTTP-01 redirect if the order needs it, sends the job and waits for its result.
func (c *acmeCoordinator) execute(ctx context.Context, run *acmeRun, job acme.Job, ifaces []string) acme.Result {
	failed := func(err error) acme.Result {
		return acme.Result{JobID: run.jobID, OrderID: job.OrderID, Error: err.Error()}
	}
	if len(ifaces) > 0 {
		if c.realize == nil {
			return failed(errors.New("no realizer"))
		}
		// Serialize HTTP-01 runs so concurrent orders never flush each other's active nftables redirect set.
		c.http01Mu.Lock()
		defer c.http01Mu.Unlock()
		rctx, cancel := context.WithTimeout(ctx, acmeRealizeLimit)
		err := c.realize(rctx, "acme-http01-open", ifaces...)
		cancel()
		defer c.closeHTTP01() // on success, failure, timeout and stop alike
		if err != nil {
			return failed(fmt.Errorf("open the HTTP-01 redirect: %w", err))
		}
	}
	deadline := c.now().Add(c.timeout).UTC()
	job.Deadline = &deadline
	payload, err := bus.NewEvent(acme.JobType, "engine", job)
	if err == nil {
		if c.publish == nil {
			err = errors.New("no event bus")
		} else {
			err = c.publish(acme.OrderTopic(job.OrderID), payload)
		}
	}
	if err != nil {
		return failed(fmt.Errorf("send the order to andaru-outbound: %w", err))
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case res := <-run.results:
		return res
	case <-timer.C:
		return failed(errors.New("andaru-outbound did not answer in time; is it running?"))
	case <-ctx.Done():
		return failed(errors.New("the engine stopped"))
	}
}

// onResult hands andaru-outbound's result to the run it answers. It runs in the bus's goroutine and never blocks.
func (c *acmeCoordinator) onResult(topic string, payload []byte) {
	var e struct {
		Type string      `json:"type"`
		Data acme.Result `json:"data"`
	}
	if json.Unmarshal(payload, &e) != nil || e.Type != acme.ResultType {
		return
	}
	orderID := strings.TrimPrefix(topic, "acme-result/")
	c.mu.Lock()
	run := c.runs[orderID]
	c.mu.Unlock()
	if run == nil || run.jobID == "" || run.jobID != e.Data.JobID || e.Data.OrderID != orderID {
		return // a late or foreign result
	}
	select {
	case run.results <- e.Data:
	default:
	}
}

// finish checks a successful result against the order and its key, saves the certificate unless the run was a
// staging test, and records how the run went.
func (c *acmeCoordinator) finish(life context.Context, run *acmeRun, res acme.Result) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(life), 30*time.Second)
	defer cancel()
	o := run.order
	var imported pki.Imported
	if res.OK {
		var err error
		if imported, err = pki.Import([]byte(res.CertPEM), []byte(res.ChainPEM), []byte(run.keyPEM)); err != nil {
			res.OK, res.Error = false, "The issued certificate was refused: "+err.Error()+"."
		} else if missing := slices.DeleteFunc(slices.Clone(o.Domains), func(d string) bool { return slices.Contains(imported.Info.SANs, d) }); len(missing) > 0 {
			res.OK, res.Error = false, "The issued certificate does not cover "+strings.Join(missing, ", ")+"."
		}
	}
	certID := o.CertificateID
	if res.OK && !run.staging {
		if err := c.save(ctx, o, &certID, imported); err != nil {
			res.OK, res.Error = false, "The issued certificate was not saved: "+err.Error()
		}
	}
	c.mu.Lock()
	delete(c.runs, o.ID)
	if res.OK && !run.staging && validACMEAccountURI(res.AccountURI) {
		c.state.Accounts[o.AccountID] = &acmeAccountState{RegisteredURI: res.AccountURI, Binding: run.binding}
	}
	c.recordLocked(o, run.staging, res)
	c.mu.Unlock()
	c.notifyRun(o, run.staging, res)
	if res.OK {
		slog.Info("acme: order done", "order", o.ID, "certificate", certID, "staging", run.staging)
	} else {
		slog.Warn("acme: order failed", "order", o.ID, "staging", run.staging, "error", res.Error)
	}
}

func validACMEAccountURI(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != ""
}

// save stores the certificate and key, audited as certificate.renewed, then places it and publishes
// certificate/renewed once it is in place.
func (c *acmeCoordinator) save(ctx context.Context, o config.ACMEOrder, certID *string, imported pki.Imported) error {
	if *certID == "" {
		id, err := config.NewID("pki.certificates")
		if err != nil {
			return err
		}
		*certID = id
	}
	finish, err := beginAudit(ctx, c.audits, requestActor{Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "local"}, audit.ActionCertificateRenewed, *certID)
	if err != nil {
		return errors.New("security audit storage is unavailable")
	}
	var saved config.SaveResult
	var fault *config.Fault
	for attempt := 0; ; attempt++ {
		saved, err = c.store.SaveACMECertificate(ctx, o.ID, *certID, o.AccountID, string(imported.CertPEM), string(imported.ChainPEM), string(imported.KeyPEM))
		// A save that moved the head in between is read again: the batch is guarded by the head it was built on.
		if err == nil || attempt == 2 || !errors.As(err, &fault) || fault.Code != "PRECONDITION_FAILED" {
			break
		}
	}
	if err != nil {
		finish(audit.ResultFailure)
		if errors.As(err, &fault) {
			return errors.New(fault.Code)
		}
		return err
	}
	finish(audit.ResultSuccess)
	certName := o.Name
	for _, item := range saved.Items {
		if item.Collection == "pki.certificates" && item.ID == *certID && len(item.Value) > 0 {
			var meta struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(item.Value, &meta) == nil && meta.Name != "" {
				certName = meta.Name
			}
			break
		}
	}
	renewed := renewal{name: certName, info: imported.Info, source: "acme"}
	if c.certs == nil {
		publishCertificateRenewed(c.events, *certID, renewed)
		return nil
	}
	c.certs.enqueueRenewal(*certID, renewed)
	if err := c.certs.sync(ctx); err != nil {
		slog.Warn("acme: cert-sync after renewal; renewal event deferred until a retry succeeds", "error", err)
		c.certs.nudge()
	}
	return nil
}

// recordLocked records a run's end in state.json.
func (c *acmeCoordinator) recordLocked(o config.ACMEOrder, staging bool, res acme.Result) {
	now := c.now()
	st := c.orderStateLocked(o.ID)
	st.LastAttemptAt = &now
	if res.OK {
		st.State, st.LastError = "succeeded", ""
		if !staging {
			st.LastSucceededAt = &now
		}
	} else {
		st.State, st.LastError = "failed", notify.Detail(res.Error)
	}
	c.saveLocked()
}

// notifyRun raises or clears the order's notification outside c.mu. A staging test notifies nothing.
func (c *acmeCoordinator) notifyRun(o config.ACMEOrder, staging bool, res acme.Result) {
	switch {
	case staging:
	case res.OK:
		c.clear(o)
	default:
		c.raise(o, notify.Warning, "ACME order "+o.Name+" failed", res.Error)
	}
}

func (c *acmeCoordinator) raise(o config.ACMEOrder, severity, subject, detail string) {
	if c.notice == nil {
		return
	}
	r := notify.Raise{Key: "acme/" + o.ID, Severity: severity, Subject: notify.Subject(subject), Detail: notify.Detail(detail),
		Link: "/system/certificates", Requires: string(authz.CertificatesRead)}
	if err := c.notice(r); err != nil {
		slog.Warn("acme: raise notification", "order", o.ID, "error", err)
	}
}

func (c *acmeCoordinator) clear(o config.ACMEOrder) {
	if c.notice != nil {
		if err := c.notice(notify.Raise{Key: "acme/" + o.ID}); err != nil {
			slog.Warn("acme: clear notification", "order", o.ID, "error", err)
		}
	}
}

type acmeStatusItem struct {
	OrderID         string     `json:"orderId"`
	AccountID       string     `json:"accountId"`
	CertificateID   string     `json:"certificateId,omitempty"`
	State           string     `json:"state"`
	LastAttemptAt   *time.Time `json:"lastAttemptAt,omitempty"`
	LastSucceededAt *time.Time `json:"lastSucceededAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	NextRenewAt     *time.Time `json:"nextRenewAt,omitempty"`
}

type acmeAccountStatus struct {
	AccountID     string `json:"accountId"`
	RegisteredURI string `json:"registeredUri,omitempty"`
	Status        string `json:"status"`
}

type acmeStatus struct {
	Items    []acmeStatusItem    `json:"items"`
	Accounts []acmeAccountStatus `json:"accounts"`
}

// status is every saved order and account with what its runs left.
func (c *acmeCoordinator) status(ctx context.Context) (acmeStatus, error) {
	settings, err := c.store.ACME(ctx)
	if err != nil {
		return acmeStatus{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := acmeStatus{Items: []acmeStatusItem{}, Accounts: []acmeAccountStatus{}}
	for _, o := range settings.Orders {
		item := acmeStatusItem{OrderID: o.ID, AccountID: o.AccountID, CertificateID: o.CertificateID, State: "idle"}
		if st := c.state.Orders[o.ID]; st != nil {
			item.State, item.LastAttemptAt, item.LastSucceededAt, item.LastError = st.State, st.LastAttemptAt, st.LastSucceededAt, st.LastError
		}
		if o.Enabled && !o.CertificateNotAfter.IsZero() {
			next := renewAt(o).UTC()
			item.NextRenewAt = &next
		}
		out.Items = append(out.Items, item)
	}
	for _, id := range settings.AccountIDs {
		a := acmeAccountStatus{AccountID: id, Status: "unregistered"}
		if st := c.state.Accounts[id]; st != nil && st.RegisteredURI != "" {
			a.RegisteredURI, a.Status = st.RegisteredURI, "valid"
		}
		out.Accounts = append(out.Accounts, a)
	}
	return out, nil
}

// acmeAPI serves the ACME status and the routes that start an order.
type acmeAPI struct {
	acme   *acmeCoordinator
	audits *audit.Store
}

func (a *acmeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == acmeStatusPath {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		s, err := a.acme.status(r.Context())
		if err != nil {
			writeConfigurationError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, s)
		return
	}
	id, verb, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, acmeOrdersPrefix), "/")
	if !ok || id == "" || verb != "renew" && verb != "test-staging" {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return
	}
	if raw, err := readRequestBody(w, r, 1<<10); err != nil || len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "{}" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The request takes no body.")
		return
	}
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	if !audit.ValidTarget(audit.ActionACMEOrderTriggered, id) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "The ACME order does not exist.")
		return
	}
	finish, err := beginAudit(r.Context(), a.audits, actor, audit.ActionACMEOrderTriggered, id)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Security audit storage is unavailable.")
		return
	}
	staging := verb == "test-staging"
	err = a.acme.trigger(r.Context(), id, staging)
	if err == nil {
		finish(audit.ResultSuccess)
		if staging {
			writeJSON(w, r, http.StatusAccepted, map[string]any{"orderId": id, "stagingTest": true})
		} else {
			writeJSON(w, r, http.StatusAccepted, map[string]any{"orderId": id, "started": true})
		}
		return
	}
	finish(audit.ResultFailure)
	var notReady *acmeNotReady
	switch {
	case errors.Is(err, errACMENotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "The ACME order does not exist.")
	case errors.Is(err, errACMEDisabled):
		writeError(w, r, http.StatusConflict, "ACME_ORDER_DISABLED", "The ACME order is disabled; enable it, or run a staging test.")
	case errors.Is(err, errACMERunning):
		writeError(w, r, http.StatusConflict, "ACME_ORDER_RUNNING", "The ACME order is already running.")
	case errors.As(err, &notReady):
		writeError(w, r, http.StatusConflict, "ACME_ORDER_NOT_READY", notReady.reason)
	default:
		writeConfigurationError(w, r, err)
	}
}

// beginAudit records the intent durably before anything changes; it fails closed when audit storage cannot. A nil
// store records nothing.
func beginAudit(ctx context.Context, audits *audit.Store, actor requestActor, action audit.Action, target string) (func(audit.Result), error) {
	if audits == nil {
		return func(audit.Result) {}, nil
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	opID := "pki-" + hex.EncodeToString(rnd[:])
	sum := sha256.Sum256([]byte(string(action) + "\x00" + target))
	if _, err := audits.Begin(ctx, audit.Intent{OperationID: opID, Actor: actor.Actor, Source: actor.Source,
		Action: action, Target: target, RequestHash: hex.EncodeToString(sum[:]), Category: audit.CategoryOf(action)}); err != nil {
		slog.WarnContext(ctx, "begin audit intent", "action", action, "target", target, "error", err)
		return nil, err
	}
	return func(result audit.Result) {
		ctx, cancel := context.WithTimeout(context.Background(), pkiAuditTimeout)
		defer cancel()
		if err := audits.Finish(ctx, audit.Outcome{OperationID: opID, Result: result}); err != nil {
			slog.Warn("finish audit outcome", "action", action, "target", target, "operationId", opID, "error", err)
		}
	}, nil
}

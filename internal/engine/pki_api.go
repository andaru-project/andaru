package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/pki"
	"github.com/andaru-project/andaru/internal/render"
)

const (
	pkiAPIPrefix       = "/api/v1/pki"
	pkiConfigPrefix    = "/api/v1/config/pki/"
	certSyncTempGlob   = "cert-sync-*.json"
	certSyncRetryDelay = 5 * time.Second
	certSyncTimeout    = 30 * time.Second
	pkiAuditTimeout    = 5 * time.Second
)

// pkiKinds maps the PKI collections' path names to their collections.
var pkiKinds = map[string]string{"cas": "pki.cas", "certificates": "pki.certificates", "tls-crypt-keys": "pki.tlsCryptKeys"}

type pendingRenewal struct {
	id string
	r  renewal
}

// certSync keeps the certificates the live generation's services name in place, as the saved head holds them now:
// on startup, after each activation or rollback, and when a certificate is renewed or replaced. It stages them for
// andaru realize cert-sync, which opens no database.
type certSync struct {
	stateDir   string
	runtimeDir string
	catalog    *addon.Catalog
	bindings   func(context.Context, config.Version) ([]config.CertificateConsumer, map[string]config.CertificateMaterial, error)
	realize    func(ctx context.Context, file string) error
	events     *bus.Bus

	mu              sync.Mutex
	last            string // the live generation and payload last placed, hashed, so an unchanged sync spawns nothing
	pendingRenewals []pendingRenewal
	wake            chan struct{}
}

func newCertSync(stateDir, runtimeDir string, catalog *addon.Catalog,
	bindings func(context.Context, config.Version) ([]config.CertificateConsumer, map[string]config.CertificateMaterial, error),
	realize func(ctx context.Context, file string) error) *certSync {
	if matches, err := filepath.Glob(filepath.Join(runtimeDir, certSyncTempGlob)); err == nil {
		for _, m := range matches {
			_ = os.Remove(m) // left by a killed engine; each holds private keys
		}
	}
	return &certSync{stateDir: stateDir, runtimeDir: runtimeDir, catalog: catalog, bindings: bindings, realize: realize, wake: make(chan struct{}, 1)}
}

func (c *certSync) nudge() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// invalidate clears the cached placement hash before nudging, so even a failed activation that restored the
// previous generation's stale certificate files without changing Pointers.Live still re-syncs renewed certs.
func (c *certSync) invalidate() {
	c.mu.Lock()
	c.last = ""
	c.mu.Unlock()
	c.nudge()
}

// enqueueRenewal queues a certificate/renewed bus event to be published only after cert-sync succeeds on disk.
func (c *certSync) enqueueRenewal(id string, r renewal) {
	c.mu.Lock()
	c.pendingRenewals = append(c.pendingRenewals, pendingRenewal{id: id, r: r})
	c.mu.Unlock()
}

// run syncs on each nudge, and again after a failure.
func (c *certSync) run(ctx context.Context, retry bool) {
	timer := time.NewTimer(certSyncRetryDelay)
	if !retry {
		timer.Stop()
	}
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-timer.C:
		}
		timer.Stop()
		if err := c.sync(ctx); err != nil {
			slog.Warn("cert-sync: certificates not placed; retrying", "error", err)
			timer.Reset(certSyncRetryDelay)
		}
	}
}

// sync places the live generation's certificates unless they are already in place.
func (c *certSync) sync(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, certSyncTimeout)
	defer cancel()
	live, payload, err := c.payload(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(raw) > generation.MaxCertSyncBytes {
		return fmt.Errorf("cert-sync payload exceeds maximum size (%d bytes)", generation.MaxCertSyncBytes)
	}
	// The generation is part of the key: an activation or rollback places its own copies, which must be replaced.
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("%d\x00", live)), raw...))
	if hex.EncodeToString(sum[:]) == c.last {
		c.drainRenewalsLocked()
		return nil
	}
	f, err := os.CreateTemp(c.runtimeDir, certSyncTempGlob)
	if err != nil {
		return fmt.Errorf("stage cert-sync: %w", err)
	}
	staged := f.Name()
	defer os.Remove(staged) // realize removes it once read; this covers its failing first
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("stage cert-sync: %w", err)
	}
	if err = c.realize(ctx, staged); err != nil {
		return err
	}
	c.last = hex.EncodeToString(sum[:])
	c.drainRenewalsLocked()
	return nil
}

func (c *certSync) drainRenewalsLocked() {
	if len(c.pendingRenewals) == 0 {
		return
	}
	queued := c.pendingRenewals
	c.pendingRenewals = nil
	for _, item := range queued {
		publishCertificateRenewed(c.events, item.id, item.r)
	}
}

// payload is what the live generation's services name: management.certificate and the certificate-reference
// settings of the add-ons it realized, each with the certificate the saved head holds now.
func (c *certSync) payload(ctx context.Context) (int, generation.CertSync, error) {
	payload := generation.CertSync{Addons: map[string]map[string]string{}}
	dir := generation.Dir(c.stateDir)
	p, err := generation.ReadPointers(dir)
	if err != nil {
		return 0, payload, err
	}
	if p.Live == nil {
		zero := 0
		payload.ExpectedLive = &zero
		return 0, payload, nil
	}
	g, err := generation.Read(dir, *p.Live)
	if err != nil {
		return 0, payload, err
	}
	liveID := g.ID
	payload.ExpectedLive = &liveID
	if g.SavedVersion == "" {
		return g.ID, payload, nil
	}
	consumers, material, err := c.bindings(ctx, config.Version(g.SavedVersion))
	if fault := (*config.Fault)(nil); errors.As(err, &fault) && fault.Code == "NOT_FOUND" {
		slog.Warn("cert-sync: the live generation's saved version is unavailable; serving no managed certificate", "generation", g.ID, "version", g.SavedVersion)
		return g.ID, payload, nil
	}
	if err != nil {
		return 0, payload, err
	}
	realized := map[string]bool{}
	for _, f := range g.Files {
		if rest, ok := strings.CutPrefix(f.Path, addonQuadletDir+"andaru-addon-"); ok {
			realized[strings.TrimSuffix(rest, ".container")] = true
		}
	}
	for _, consumer := range consumers {
		m, ok := material[consumer.CertificateID]
		if !ok {
			if consumer.Kind == "management" {
				// Certificate deleted in saved head after this live generation was applied: keep the live web cert until Apply.
				payload.KeepWeb = true
			}
			continue
		}
		if m.Unresolved || m.Key == "" {
			continue // with no key there is nothing to serve: management falls back to the bootstrap certificate
		}
		switch consumer.Kind {
		case "management":
			payload.Web = &generation.CertPair{Cert: joinPEM(m.CertPEM, m.ChainPEM), Key: m.Key}
		case "addon":
			if c.catalog == nil {
				continue
			}
			manifest, ok := c.catalog.ByID(consumer.AddonID)
			if !ok || !realized[manifest.Slug] {
				continue
			}
			i := slices.IndexFunc(manifest.Properties, func(p addon.Property) bool { return p.Name == consumer.Setting })
			if i < 0 || manifest.Properties[i].Type != "certificate-reference" || manifest.Properties[i].Binding == nil {
				continue
			}
			files, _ := render.CertificateFiles(manifest.Properties[i].Binding, m.CertPEM, m.ChainPEM, m.Key)
			if payload.Addons[manifest.Slug] == nil {
				payload.Addons[manifest.Slug] = map[string]string{}
			}
			for name, content := range files {
				payload.Addons[manifest.Slug][name] = content
			}
		}
	}
	return g.ID, payload, nil
}

func joinPEM(certPEM, chainPEM string) string {
	out := strings.TrimRight(certPEM, "\n") + "\n"
	if strings.TrimSpace(chainPEM) != "" {
		out += strings.TrimRight(chainPEM, "\n") + "\n"
	}
	return out
}

// pkiAPI serves the imperative PKI routes and supplies PKI audit/sync hooks to config routes.
type pkiAPI struct {
	store       *config.Store
	coordinator *securityCoordinator
	events      *bus.Bus
	certs       *certSync
}

type apiFailure struct {
	status        int
	code, message string
	details       []errorDetail
}

func (f *apiFailure) write(w http.ResponseWriter, r *http.Request) {
	details := f.details
	if details == nil {
		details = []errorDetail{}
	}
	writeJSON(w, r, f.status, errorResponse{Error: errorBody{Code: f.code, Message: f.message, Details: details}})
}

func invalidField(path, message string) *apiFailure {
	return &apiFailure{status: http.StatusUnprocessableEntity, code: "VALIDATION_FAILED", message: "The request is not valid.", details: []errorDetail{{Path: path, Message: message}}}
}

func (a *pkiAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, pkiAPIPrefix+"/"), "/")
	switch {
	case len(parts) == 2 && parts[1] == "generate" && (parts[0] == "cas" || parts[0] == "certificates" || parts[0] == "tls-crypt-keys"),
		len(parts) == 2 && parts[0] == "certificates" && parts[1] == "import":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		a.create(w, r, parts[0], parts[1])
	case len(parts) == 3 && (parts[0] == "cas" || parts[0] == "certificates") && parts[1] != "" && parts[2] == "pem":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		a.pem(w, r, pkiKinds[parts[0]], parts[1])
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
	}
}

// pem sends a CA's or certificate's public PEM, a certificate's chain appended; never a private key.
func (a *pkiAPI) pem(w http.ResponseWriter, r *http.Request, collection, id string) {
	object, err := a.store.PKIObject(r.Context(), collection, id)
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="`+object.ID+`.pem"`)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(joinPEM(object.CertPEM, object.ChainPEM)))
	}
}

// renewal is the certificate a create made, for its certificate/renewed event.
type renewal struct {
	name   string
	info   pki.Info
	source string
}

func (a *pkiAPI) create(w http.ResponseWriter, r *http.Request, kind, verb string) {
	raw, err := readRequestBody(w, r, 1<<20)
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Request body exceeds its size limit.")
		return
	}
	collection := pkiKinds[kind]
	action := audit.ActionCertificateCreated
	if verb == "import" {
		action = audit.ActionCertificateImported
	}
	// Each builder checks its request before anything is recorded, then makes the object under its new ID.
	var build func(ctx context.Context, id string) (map[string]any, *renewal, *apiFailure)
	switch {
	case kind == "cas":
		build = a.generateCA(raw)
	case kind == "certificates" && verb == "generate":
		build = a.generateCertificate(raw)
	case kind == "certificates":
		build = a.importCertificate(raw)
	default:
		build = generateTLSCryptKey(raw)
	}
	if build == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON or unknown request fields.")
		return
	}
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	id, err := config.NewID(collection)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Identity allocation failed.")
		return
	}
	finish, ok := a.beginAudit(w, r, actor, action, id)
	if !ok {
		return
	}
	value, renewed, failure := build(r.Context(), id)
	if failure != nil {
		finish(audit.ResultFailure)
		failure.write(w, r)
		return
	}
	body, err := json.Marshal(value)
	if err != nil {
		finish(audit.ResultFailure)
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "The object could not be encoded.")
		return
	}
	changes, err := config.ObjectCreateWithID(collection, id, body)
	var result config.SaveResult
	if err == nil {
		result, err = a.store.Save(r.Context(), actor.Actor, changes)
	}
	if err != nil {
		finish(audit.ResultFailure)
		writeConfigurationError(w, r, err)
		return
	}
	finish(audit.ResultSuccess)
	if renewed != nil {
		publishCertificateRenewed(a.events, id, *renewed)
	}
	w.Header().Set("Andaru-Saved-Version", string(result.SavedVersion))
	w.Header().Set("Location", pkiConfigPrefix+kind+"/"+id)
	for _, item := range result.Items {
		if item.Collection == collection && item.ID == id {
			writeJSON(w, r, http.StatusCreated, config.ObjectView{Value: item.Value, Version: item.Version})
			return
		}
	}
	writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Saved response is unavailable; read current state before retrying.")
}

// certFields are the recorded fields of a certificate's PEM, as the store checks them.
func certFields(name string, certPEM []byte, info pki.Info) map[string]any {
	return map[string]any{"name": name, "subject": info.Subject, "serial": info.Serial, "fingerprintSha256": info.FingerprintSHA256,
		"notBefore": info.NotBefore, "notAfter": info.NotAfter, "certPem": string(certPEM)}
}

func keyWrite(id string, key []byte) map[string]any {
	return map[string]any{"secret": config.PKISecretName(id), "set": string(key), "expectedVersion": "0"}
}

func validName(name string) *apiFailure {
	if name == "" || len(name) > 128 {
		return invalidField("/name", "Expected a name of 1 to 128 characters.")
	}
	return nil
}

// issuer loads a stored CA able to sign: its certificate and resolved key.
func (a *pkiAPI) issuer(ctx context.Context, path, id string) (pki.Issuer, *apiFailure) {
	ca, err := a.store.PKIObject(ctx, "pki.cas", id)
	var fault *config.Fault
	if errors.As(err, &fault) && fault.Code == "NOT_FOUND" {
		return pki.Issuer{}, invalidField(path, "Expected a certificate authority of this configuration.")
	}
	if err != nil {
		return pki.Issuer{}, &apiFailure{status: http.StatusServiceUnavailable, code: "STORE_UNAVAILABLE", message: "Configuration storage is unavailable."}
	}
	if ca.Key == "" {
		return pki.Issuer{}, &apiFailure{status: http.StatusConflict, code: "CA_KEY_UNAVAILABLE", message: "The certificate authority has no private key to sign with; set it again after a restore without secrets.",
			details: []errorDetail{{Path: path, Message: "The CA's private key is unavailable."}}}
	}
	issuer, err := pki.ParseIssuer([]byte(ca.CertPEM), []byte(ca.Key))
	if err != nil {
		return pki.Issuer{}, invalidField(path, "The certificate authority cannot sign: "+err.Error()+".")
	}
	return issuer, nil
}

func (a *pkiAPI) generateCA(raw []byte) func(context.Context, string) (map[string]any, *renewal, *apiFailure) {
	var req struct {
		Name         string `json:"name"`
		CommonName   string `json:"commonName"`
		Organization string `json:"organization"`
		KeyAlgorithm string `json:"keyAlgorithm"`
		ValidDays    int    `json:"validDays"`
		ParentCAID   string `json:"parentCaId"`
	}
	if !strictJSON(raw, &req, "name", "commonName", "organization", "keyAlgorithm", "validDays", "parentCaId") {
		return nil
	}
	return func(ctx context.Context, id string) (map[string]any, *renewal, *apiFailure) {
		if f := validName(req.Name); f != nil {
			return nil, nil, f
		}
		if !slices.Contains(pki.Algorithms, req.KeyAlgorithm) {
			return nil, nil, invalidField("/keyAlgorithm", `Expected "ecdsa-p256", "ecdsa-p384", "rsa-2048" or "rsa-4096".`)
		}
		var parent *pki.Issuer
		if req.ParentCAID != "" {
			p, f := a.issuer(ctx, "/parentCaId", req.ParentCAID)
			if f != nil {
				return nil, nil, f
			}
			parent = &p
		}
		issued, err := pki.NewCA(pki.CARequest{CommonName: req.CommonName, Organization: req.Organization, KeyAlgorithm: req.KeyAlgorithm, ValidDays: req.ValidDays}, parent)
		if err != nil {
			return nil, nil, invalidField("", err.Error()+".")
		}
		value := certFields(req.Name, issued.CertPEM, issued.Info)
		if req.ParentCAID != "" {
			value["parentCaId"] = req.ParentCAID
		}
		value["keyRef"] = keyWrite(id, issued.KeyPEM)
		return value, nil, nil
	}
}

func (a *pkiAPI) generateCertificate(raw []byte) func(context.Context, string) (map[string]any, *renewal, *apiFailure) {
	var req struct {
		Name         string   `json:"name"`
		CAID         string   `json:"caId"`
		CommonName   string   `json:"commonName"`
		SANs         []string `json:"sans"`
		Usage        string   `json:"usage"`
		KeyAlgorithm string   `json:"keyAlgorithm"`
		ValidDays    int      `json:"validDays"`
	}
	if !strictJSON(raw, &req, "name", "caId", "commonName", "sans", "usage", "keyAlgorithm", "validDays") {
		return nil
	}
	return func(ctx context.Context, id string) (map[string]any, *renewal, *apiFailure) {
		if f := validName(req.Name); f != nil {
			return nil, nil, f
		}
		if !slices.Contains(pki.Algorithms, req.KeyAlgorithm) {
			return nil, nil, invalidField("/keyAlgorithm", `Expected "ecdsa-p256", "ecdsa-p384", "rsa-2048" or "rsa-4096".`)
		}
		issuer, f := a.issuer(ctx, "/caId", req.CAID)
		if f != nil {
			return nil, nil, f
		}
		issued, err := pki.NewLeaf(pki.LeafRequest{CommonName: req.CommonName, SANs: req.SANs, Usage: req.Usage, KeyAlgorithm: req.KeyAlgorithm, ValidDays: req.ValidDays}, issuer)
		if err != nil {
			return nil, nil, invalidField("", err.Error()+".")
		}
		value := certFields(req.Name, issued.CertPEM, issued.Info)
		value["caId"], value["sans"], value["keyAlgorithm"], value["keyRef"] = req.CAID, issued.Info.SANs, issued.Info.KeyAlgorithm, keyWrite(id, issued.KeyPEM)
		chain, err := a.chain(ctx, issuer.Cert)
		if err != nil {
			return nil, nil, &apiFailure{status: http.StatusServiceUnavailable, code: "STORE_UNAVAILABLE", message: "Configuration storage is unavailable."}
		}
		if len(chain) > 0 {
			value["chainPem"] = string(chain)
		}
		return value, &renewal{name: req.Name, info: issued.Info, source: "manual"}, nil
	}
}

// chain is the intermediates from issuer up to, not including, a self-signed root, as far as the stored CAs reach.
func (a *pkiAPI) chain(ctx context.Context, issuer *x509.Certificate) ([]byte, error) {
	stored, err := a.store.PKICertificateAuthorities(ctx)
	if err != nil {
		return nil, err
	}
	var cas []*x509.Certificate
	for _, ca := range stored {
		if _, cert, err := pki.Inspect([]byte(ca.CertPEM)); err == nil {
			cas = append(cas, cert)
		}
	}
	var out []*x509.Certificate
	for current := issuer; current != nil && len(out) < 10; {
		if bytes.Equal(current.RawIssuer, current.RawSubject) && current.CheckSignatureFrom(current) == nil {
			break
		}
		out = append(out, current)
		next := current
		current = nil
		for _, ca := range cas {
			if !ca.Equal(next) && next.CheckSignatureFrom(ca) == nil {
				current = ca
				break
			}
		}
	}
	return pki.Encode(out...), nil
}

func (a *pkiAPI) importCertificate(raw []byte) func(context.Context, string) (map[string]any, *renewal, *apiFailure) {
	var req struct {
		Name          string `json:"name"`
		CertPEM       string `json:"certPem"`
		ChainPEM      string `json:"chainPem"`
		PrivateKeyPEM string `json:"privateKeyPem"`
	}
	if !strictJSON(raw, &req, "name", "certPem", "chainPem", "privateKeyPem") {
		return nil
	}
	return func(_ context.Context, id string) (map[string]any, *renewal, *apiFailure) {
		if f := validName(req.Name); f != nil {
			return nil, nil, f
		}
		imported, err := pki.Import([]byte(req.CertPEM), []byte(req.ChainPEM), []byte(req.PrivateKeyPEM))
		if err != nil {
			return nil, nil, invalidField("", err.Error()+".") // never the key itself
		}
		value := certFields(req.Name, imported.CertPEM, imported.Info)
		value["sans"], value["keyAlgorithm"], value["keyRef"] = imported.Info.SANs, imported.Info.KeyAlgorithm, keyWrite(id, imported.KeyPEM)
		if len(imported.ChainPEM) > 0 {
			value["chainPem"] = string(imported.ChainPEM)
		}
		return value, &renewal{name: req.Name, info: imported.Info, source: "import"}, nil
	}
}

func generateTLSCryptKey(raw []byte) func(context.Context, string) (map[string]any, *renewal, *apiFailure) {
	var req struct {
		Name string `json:"name"`
	}
	if !strictJSON(raw, &req, "name") {
		return nil
	}
	return func(_ context.Context, id string) (map[string]any, *renewal, *apiFailure) {
		if f := validName(req.Name); f != nil {
			return nil, nil, f
		}
		key, err := pki.TLSCryptKey()
		if err != nil {
			return nil, nil, &apiFailure{status: http.StatusServiceUnavailable, code: "STORE_UNAVAILABLE", message: "No key could be generated."}
		}
		return map[string]any{"name": req.Name, "keyRef": keyWrite(id, key)}, nil, nil
	}
}

// publishCertificateRenewed tells the bus, and the add-ons subscribed to it, that a certificate changed. The event
// carries no key.
func publishCertificateRenewed(events *bus.Bus, id string, r renewal) {
	if events == nil {
		return
	}
	payload, err := bus.NewEvent("certificate.renewed.v1", "engine", map[string]string{
		"certificateId": id, "name": r.name, "fingerprintSha256": r.info.FingerprintSHA256, "serial": r.info.Serial,
		"notBefore": r.info.NotBefore, "notAfter": r.info.NotAfter, "source": r.source,
	})
	if err == nil {
		err = events.Publish("certificate/renewed", payload, true)
	}
	if err != nil {
		slog.Warn("publish certificate/renewed", "certificate", id, "error", err)
	}
}

// beginAudit records the intent durably before anything changes; it fails closed when audit storage cannot.
func (a *pkiAPI) beginAudit(w http.ResponseWriter, r *http.Request, actor requestActor, action audit.Action, target string) (func(audit.Result), bool) {
	if a.coordinator == nil || a.coordinator.audits == nil {
		return func(audit.Result) {}, true
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Security audit storage is unavailable.")
		return nil, false
	}
	opID := "pki-" + hex.EncodeToString(rnd[:])
	sum := sha256.Sum256([]byte(string(action) + "\x00" + target))
	if _, err := a.coordinator.audits.Begin(r.Context(), audit.Intent{OperationID: opID, Actor: actor.Actor, Source: actor.Source,
		Action: action, Target: target, RequestHash: hex.EncodeToString(sum[:]), Category: audit.CategoryOf(action)}); err != nil {
		slog.WarnContext(r.Context(), "pki: begin audit intent", "action", action, "target", target, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Security audit storage is unavailable.")
		return nil, false
	}
	return func(result audit.Result) {
		ctx, cancel := context.WithTimeout(context.Background(), pkiAuditTimeout)
		defer cancel()
		if err := a.coordinator.audits.Finish(ctx, audit.Outcome{OperationID: opID, Result: result}); err != nil {
			slog.Warn("pki: finish audit outcome", "action", action, "target", target, "operationId", opID, "error", err)
		}
	}, true
}

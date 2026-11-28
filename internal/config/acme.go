package config

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/pki"
)

// acmeSettings are the ACME accounts and the orders that keep certificates issued from them. They take effect when
// saved, except for the HTTP-01 orders' port-80 redirect, which the packet filter realizes. What changes as orders
// run (registered accounts, attempts, errors) is kept by the engine beside the store, not in it.
type acmeSettings struct {
	Accounts []acmeAccount `json:"accounts,omitempty"`
	Orders   []acmeOrder   `json:"orders,omitempty"`
}

// acmeAccount is an account at an ACME directory; AccountKeyRef is generated when a create leaves it out.
type acmeAccount struct {
	identity
	DirectoryURL  string           `json:"directoryUrl"`
	Email         string           `json:"email"`
	EABKeyID      string           `json:"eabKeyId,omitempty"`
	EABHMACRef    *secretReference `json:"eabHmacRef,omitempty"`
	AccountKeyRef *secretReference `json:"accountKeyRef,omitempty"`
}

// acmeOrder keeps a certificate for domains issued from an account. CertificateID names the certificate it renews;
// the first issuance creates it.
type acmeOrder struct {
	identity
	AccountID       string        `json:"accountId"`
	CertificateID   string        `json:"certificateId,omitempty"`
	Domains         []string      `json:"domains"`
	KeyAlgorithm    string        `json:"keyAlgorithm"`
	RenewBeforeDays int           `json:"renewBeforeDays,omitempty"`
	Enabled         bool          `json:"enabled"`
	Challenge       acmeChallenge `json:"challenge"`
}

type acmeChallenge struct {
	Type   string      `json:"type"`
	HTTP01 *acmeHTTP01 `json:"http01,omitempty"`
	DNS01  *acmeDNS01  `json:"dns01,omitempty"`
}

type acmeHTTP01 struct {
	ListenSegments []string `json:"listenSegments"`
}

type acmeDNS01 struct {
	Provider                  string         `json:"provider"`
	PropagationTimeoutSeconds int            `json:"propagationTimeoutSeconds,omitempty"`
	Cloudflare                *acmeAPIToken  `json:"cloudflare,omitempty"`
	Hetzner                   *acmeAPIToken  `json:"hetzner,omitempty"`
	DigitalOcean              *acmeAPIToken  `json:"digitalocean,omitempty"`
	Porkbun                   *acmePorkbun   `json:"porkbun,omitempty"`
	ACMEDNS                   *acmeDNSServer `json:"acmeDns,omitempty"`
	Webhook                   *acmeWebhook   `json:"webhook,omitempty"`
}

type acmeAPIToken struct {
	APITokenRef *secretReference `json:"apiTokenRef,omitempty"`
}

type acmePorkbun struct {
	APIKeyRef    *secretReference `json:"apiKeyRef,omitempty"`
	SecretKeyRef *secretReference `json:"secretKeyRef,omitempty"`
}

type acmeDNSServer struct {
	ServerURL   string           `json:"serverUrl"`
	Username    string           `json:"username"`
	PasswordRef *secretReference `json:"passwordRef,omitempty"`
	Subdomain   string           `json:"subdomain"`
	FullDomain  string           `json:"fullDomain,omitempty"`
}

type acmeWebhook struct {
	URL              string           `json:"url"`
	AuthorizationRef *secretReference `json:"authorizationRef,omitempty"`
	AllowPrivateIP   bool             `json:"allowPrivateIp,omitempty"`
}

const (
	defaultRenewBeforeDays = 30
	defaultPropagation     = 120
)

var acmeCollections = []string{"acme.accounts", "acme.orders"}

// acmeDNSFields are each provider's object and secret fields, as they are written.
var acmeDNSFields = map[string]struct {
	object string
	refs   []string
}{
	"cloudflare": {"cloudflare", []string{"apiTokenRef"}}, "hetzner": {"hetzner", []string{"apiTokenRef"}}, "digitalocean": {"digitalocean", []string{"apiTokenRef"}},
	"porkbun": {"porkbun", []string{"apiKeyRef", "secretKeyRef"}}, "acme-dns": {"acmeDns", []string{"passwordRef"}}, "webhook": {"webhook", []string{"authorizationRef"}},
}

func (a *acmeAccount) secrets() []channelSecret {
	return []channelSecret{{"/eabHmacRef", a.EABHMACRef}, {"/accountKeyRef", a.AccountKeyRef}}
}

func (o *acmeOrder) secrets() []channelSecret {
	d := o.Challenge.DNS01
	if d == nil {
		return nil
	}
	const p = "/challenge/dns01/"
	var out []channelSecret
	for name, t := range map[string]*acmeAPIToken{"cloudflare": d.Cloudflare, "hetzner": d.Hetzner, "digitalocean": d.DigitalOcean} {
		if t != nil {
			out = append(out, channelSecret{p + name + "/apiTokenRef", t.APITokenRef})
		}
	}
	if d.Porkbun != nil {
		out = append(out, channelSecret{p + "porkbun/apiKeyRef", d.Porkbun.APIKeyRef}, channelSecret{p + "porkbun/secretKeyRef", d.Porkbun.SecretKeyRef})
	}
	if d.ACMEDNS != nil {
		out = append(out, channelSecret{p + "acmeDns/passwordRef", d.ACMEDNS.PasswordRef})
	}
	if d.Webhook != nil {
		out = append(out, channelSecret{p + "webhook/authorizationRef", d.Webhook.AuthorizationRef})
	}
	slices.SortFunc(out, func(a, b channelSecret) int { return strings.Compare(a.path, b.path) })
	return out
}

// acmeSecrets are an ACME object's secret references, nil for any other object.
func acmeSecrets(v any) []channelSecret {
	switch x := v.(type) {
	case *acmeAccount:
		return x.secrets()
	case *acmeOrder:
		return x.secrets()
	}
	return nil
}

// acmeSecretFields are the places in a written ACME object that may hold a secret reference.
func acmeSecretFields(collection string, value map[string]any) []secretField {
	if collection == "acme.accounts" {
		return []secretField{{"/eabHmacRef", value, "eabHmacRef"}, {"/accountKeyRef", value, "accountKeyRef"}}
	}
	challenge, _ := value["challenge"].(map[string]any)
	dns01, _ := challenge["dns01"].(map[string]any)
	var out []secretField
	for _, provider := range slices.Sorted(maps.Keys(acmeDNSFields)) {
		f := acmeDNSFields[provider]
		if obj, ok := dns01[f.object].(map[string]any); ok {
			for _, ref := range f.refs {
				out = append(out, secretField{"/challenge/dns01/" + f.object + "/" + ref, obj, ref})
			}
		}
	}
	return out
}

// acmeRefAt is the reference old holds at path, so a replace that leaves it out keeps it.
func acmeRefAt(old any, path string) *secretReference {
	for _, s := range acmeSecrets(old) {
		if s.path == path {
			return s.ref
		}
	}
	return nil
}

// ACMESecretName is the name of a secret the engine generates for an ACME object, such as an account's key.
func ACMESecretName(id, field string) string {
	prefix, uuid, _ := strings.Cut(id, "_")
	return "acme_" + prefix + "_" + strings.ReplaceAll(uuid, "-", "") + "_" + field
}

// newACMEAccountKey is a new ECDSA P-256 account key, PKCS #8 PEM.
func newACMEAccountKey() (string, error) {
	k, err := pki.GenerateKey("ecdsa-p256")
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

var acmeDomain = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

// validateACME checks the accounts and orders and what they name: an order's account, its certificate, which no
// other order may name, and its HTTP-01 segments. A wildcard domain is proved only by DNS-01.
func validateACME(d *declaration) []FieldError {
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{Path: path, Message: msg})
		}
	}
	unique := func(p, id, name string, seen map[string]bool) {
		if seen[id] {
			bad(p+"/id", "Duplicate object identity.")
		}
		if invalidText(name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if seen["name:"+name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		seen[id], seen["name:"+name] = true, true
	}
	accounts := map[string]bool{}
	for i, a := range d.ACME.Accounts {
		p := fmt.Sprintf("/acme/accounts/%d", i)
		unique(p, a.ID, a.Name, accounts)
		if !httpsURL(a.DirectoryURL) {
			bad(p+"/directoryUrl", "Expected an https URL with a host.")
		}
		if (a.EABKeyID == "") != (a.EABHMACRef == nil) {
			bad(p+"/eabKeyId", "An external account binding needs both its key ID and its HMAC key.")
		}
		if a.AccountKeyRef == nil {
			bad(p+"/accountKeyRef", "An account key is required.")
		}
	}
	certs := certificateIndex(d)
	segments := map[string]bool{}
	for _, s := range d.Segments {
		segments[s.ID] = true
	}
	orders, bound := map[string]bool{}, map[string]string{}
	for i, o := range d.ACME.Orders {
		p := fmt.Sprintf("/acme/orders/%d", i)
		unique(p, o.ID, o.Name, orders)
		if !accounts[o.AccountID] {
			bad(p+"/accountId", "Expected an ACME account of this configuration.")
		}
		if o.CertificateID != "" {
			if certs[o.CertificateID] == nil {
				bad(p+"/certificateId", "Expected a certificate of this configuration.")
			}
			if other, taken := bound[o.CertificateID]; taken {
				bad(p+"/certificateId", "The certificate is already renewed by order "+other+".")
			}
			bound[o.CertificateID] = o.ID
		}
		seen := map[string]bool{}
		for j, name := range o.Domains {
			dp := fmt.Sprintf("%s/domains/%d", p, j)
			switch {
			case len(name) > 253 || !acmeDomain.MatchString(name):
				bad(dp, "Expected a lowercase DNS name, or a wildcard *. and one.")
			case seen[name]:
				bad(dp, "Duplicate domain.")
			case strings.HasPrefix(name, "*.") && o.Challenge.Type != "dns-01":
				bad(dp, "A wildcard domain can only be proved with the dns-01 challenge.")
			}
			seen[name] = true
		}
		c := o.Challenge
		switch c.Type {
		case "http-01":
			if c.HTTP01 == nil || c.DNS01 != nil {
				bad(p+"/challenge", "The http-01 challenge needs http01, and no dns01.")
				break
			}
			for j, s := range c.HTTP01.ListenSegments {
				if !segments[s] {
					bad(fmt.Sprintf("%s/challenge/http01/listenSegments/%d", p, j), "Expected a segment of this configuration.")
				}
			}
		case "dns-01":
			if c.DNS01 == nil || c.HTTP01 != nil {
				bad(p+"/challenge", "The dns-01 challenge needs dns01, and no http01.")
				break
			}
			validateDNS01(p+"/challenge/dns01", c.DNS01, bad)
		}
	}
	return out
}

func validateDNS01(p string, d *acmeDNS01, bad func(path, msg string)) {
	present := map[string]bool{"cloudflare": d.Cloudflare != nil, "hetzner": d.Hetzner != nil, "digitalocean": d.DigitalOcean != nil,
		"porkbun": d.Porkbun != nil, "acme-dns": d.ACMEDNS != nil, "webhook": d.Webhook != nil}
	for provider, has := range present {
		if has != (provider == d.Provider) {
			bad(p, fmt.Sprintf("Expected the settings of provider %s, and of no other.", d.Provider))
			return
		}
	}
	missing := func(field string, ref *secretReference) {
		if ref == nil {
			bad(p+"/"+acmeDNSFields[d.Provider].object+"/"+field, "This secret is required.")
		}
	}
	switch d.Provider {
	case "cloudflare", "hetzner", "digitalocean":
		t := map[string]*acmeAPIToken{"cloudflare": d.Cloudflare, "hetzner": d.Hetzner, "digitalocean": d.DigitalOcean}[d.Provider]
		missing("apiTokenRef", t.APITokenRef)
	case "porkbun":
		missing("apiKeyRef", d.Porkbun.APIKeyRef)
		missing("secretKeyRef", d.Porkbun.SecretKeyRef)
	case "acme-dns":
		missing("passwordRef", d.ACMEDNS.PasswordRef)
		if !httpsURL(d.ACMEDNS.ServerURL) {
			bad(p+"/acmeDns/serverUrl", "Expected an https URL with a host.")
		}
		if f := d.ACMEDNS.FullDomain; f != "" && (strings.HasPrefix(f, "*.") || !acmeDomain.MatchString(f)) {
			bad(p+"/acmeDns/fullDomain", "Expected a lowercase DNS name.")
		}
	case "webhook":
		if !httpsURL(d.Webhook.URL) {
			bad(p+"/webhook/url", "Expected an https URL with a host.")
		}
	}
}

// ACMEOrder is one saved order with its account, credentials resolved, for an issuance or its status. Its
// credentials must never be logged or sent anywhere but andaru-outbound.
type ACMEOrder struct {
	ID, Name, AccountID, CertificateID string
	Domains                            []string
	KeyAlgorithm                       string
	RenewBeforeDays                    int
	Enabled                            bool
	// Challenge is "http-01" or "dns-01": ListenSegments are the first's, DNS01 the second's.
	Challenge      string
	ListenSegments []string
	DNS01          *acme.DNS01
	Account        ACMEAccount
	// CertificateNotAfter is when the bound certificate expires; zero when the order has none yet.
	CertificateNotAfter time.Time
	// Unresolved are the paths of secrets the order needs that a restore left unresolved.
	Unresolved []string
}

// ACMEAccount is an account with its key and EAB HMAC key.
type ACMEAccount struct {
	ID, Name, DirectoryURL, Email, EABKeyID string
	EABHMAC, Key                            string
}

// ACMESettings are the saved head's orders, and the IDs of all its accounts.
type ACMESettings struct {
	Orders     []ACMEOrder
	AccountIDs []string
}

// ACME returns the saved head's ACME orders and accounts.
func (s *Store) ACME(ctx context.Context) (result ACMESettings, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		result = acmeSettingsOf(&state)
		return nil
	})
	return
}

func acmeSettingsOf(state *savedState) (result ACMESettings) {
	d := &state.Config
	for _, a := range d.ACME.Accounts {
		result.AccountIDs = append(result.AccountIDs, a.ID)
	}
	core := state.Secrets.Scopes["core"]
	accounts := map[string]*acmeAccount{}
	for i := range d.ACME.Accounts {
		accounts[d.ACME.Accounts[i].ID] = &d.ACME.Accounts[i]
	}
	certs := certificateIndex(d)
	for i := range d.ACME.Orders {
		o := &d.ACME.Orders[i]
		out := ACMEOrder{ID: o.ID, Name: o.Name, AccountID: o.AccountID, CertificateID: o.CertificateID, Domains: slices.Clone(o.Domains),
			KeyAlgorithm: o.KeyAlgorithm, RenewBeforeDays: o.RenewBeforeDays, Enabled: o.Enabled, Challenge: o.Challenge.Type}
		value := func(path string, ref *secretReference) string {
			switch {
			case ref == nil:
				return ""
			case ref.Unresolved:
				out.Unresolved = append(out.Unresolved, path)
				return ""
			}
			return core[ref.Secret].Value
		}
		if a := accounts[o.AccountID]; a != nil {
			out.Account = ACMEAccount{ID: a.ID, Name: a.Name, DirectoryURL: a.DirectoryURL, Email: a.Email, EABKeyID: a.EABKeyID,
				EABHMAC: value("/acme/accounts/"+a.ID+"/eabHmacRef", a.EABHMACRef), Key: value("/acme/accounts/"+a.ID+"/accountKeyRef", a.AccountKeyRef)}
		}
		if h := o.Challenge.HTTP01; h != nil {
			out.ListenSegments = slices.Clone(h.ListenSegments)
		}
		if x := o.Challenge.DNS01; x != nil {
			p := "/acme/orders/" + o.ID + "/challenge/dns01/"
			j := &acme.DNS01{Provider: x.Provider, PropagationTimeoutSeconds: x.PropagationTimeoutSeconds}
			for _, t := range []*acmeAPIToken{x.Cloudflare, x.Hetzner, x.DigitalOcean} {
				if t != nil {
					j.APIToken = value(p+acmeDNSFields[x.Provider].object+"/apiTokenRef", t.APITokenRef)
				}
			}
			if x.Porkbun != nil {
				j.APIKey, j.SecretKey = value(p+"porkbun/apiKeyRef", x.Porkbun.APIKeyRef), value(p+"porkbun/secretKeyRef", x.Porkbun.SecretKeyRef)
			}
			if x.ACMEDNS != nil {
				j.ServerURL, j.Username, j.Subdomain, j.FullDomain = x.ACMEDNS.ServerURL, x.ACMEDNS.Username, x.ACMEDNS.Subdomain, x.ACMEDNS.FullDomain
				j.Password = value(p+"acmeDns/passwordRef", x.ACMEDNS.PasswordRef)
			}
			if x.Webhook != nil {
				j.URL, j.AllowPrivateIP = x.Webhook.URL, x.Webhook.AllowPrivateIP
				j.Authorization = value(p+"webhook/authorizationRef", x.Webhook.AuthorizationRef)
			}
			out.DNS01 = j
		}
		if c := certs[o.CertificateID]; c != nil {
			out.CertificateNotAfter, _ = time.Parse(time.RFC3339, c.NotAfter)
		}
		result.Orders = append(result.Orders, out)
	}
	return
}

// SaveACMECertificate saves what an order was issued: into the certificate it renews, or, on its first issuance,
// into a new certificate certID named after the order, which the order is then bound to. The certificate and key
// must have been checked against the order by the caller.
func (s *Store) SaveACMECertificate(ctx context.Context, orderID, certID, certPEM, chainPEM, keyPEM string) (SaveResult, error) {
	var changes Changes
	err := s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		var err error
		changes, err = acmeCertificateChanges(&state, orderID, certID, certPEM, chainPEM, keyPEM)
		return err
	})
	if err != nil {
		return SaveResult{}, err
	}
	return s.Save(ctx, systemActor, changes)
}

// acmeCertificateChanges is the batch SaveACMECertificate saves over state, guarded by its version.
func acmeCertificateChanges(state *savedState, orderID, certID, certPEM, chainPEM, keyPEM string) (Changes, error) {
	var changes Changes
	info, _, err := pki.Inspect([]byte(certPEM))
	if err != nil {
		return changes, fault("VALIDATION_FAILED", "/certPem", "Expected one PEM certificate.")
	}
	d := &state.Config
	i := slices.IndexFunc(d.ACME.Orders, func(o acmeOrder) bool { return o.ID == orderID })
	if i < 0 {
		return changes, fault("NOT_FOUND", "", "Object does not exist.")
	}
	order := d.ACME.Orders[i]
	head := state.Version
	changes.ExpectedSavedVersion = &head
	value := certFields(info, certPEM, chainPEM)
	keyName, keyVersion := PKISecretName(certID), Version("0")
	create := order.CertificateID == ""
	if create {
		name := order.Name
		if slices.ContainsFunc(d.PKI.Certificates, func(c pkiCertificate) bool { return c.Name == name }) {
			r := []rune(name)
			name = strings.TrimSpace(string(r[:min(len(r), 100)])) + " (" + certID[:12] + ")"
		}
		value["name"] = name
	} else {
		c := certificateIndex(d)[certID]
		if order.CertificateID != certID || c == nil {
			return changes, fault("PRECONDITION_FAILED", "/certificateId", "The order renews another certificate.")
		}
		value["name"] = c.Name
		if c.Description != "" {
			value["description"] = c.Description
		}
		keyName = c.KeyRef.Secret // the renewed key replaces the old under its name
		if old, ok := state.Secrets.Scopes["core"][keyName]; ok {
			keyVersion = old.Version
		}
	}
	value["keyRef"] = map[string]any{"secret": keyName, "set": keyPEM, "expectedVersion": keyVersion}
	body, err := json.Marshal(value)
	if err != nil {
		return changes, err
	}
	if !create {
		v := state.Versions[objectKey{"pki.certificates", certID}].Version
		changes.Operations = []Operation{{Op: "replace", Collection: "pki.certificates", ID: certID, ExpectedVersion: &v, Value: body}}
		return changes, nil
	}
	changes.Operations = []Operation{{Op: "create", Collection: "pki.certificates", Value: body}}
	if err := changes.AssignID(0, certID); err != nil {
		return changes, err
	}
	order.CertificateID = certID
	raw, err := json.Marshal(order)
	if err != nil {
		return changes, err
	}
	v := state.Versions[objectKey{"acme.orders", orderID}].Version
	changes.Operations = append(changes.Operations, Operation{Op: "replace", Collection: "acme.orders", ID: orderID, ExpectedVersion: &v, Value: raw})
	return changes, nil
}

// certFields are a certificate's recorded fields, as validatePKI checks them.
func certFields(info pki.Info, certPEM, chainPEM string) map[string]any {
	v := map[string]any{"subject": info.Subject, "serial": info.Serial, "fingerprintSha256": info.FingerprintSHA256, "notBefore": info.NotBefore,
		"notAfter": info.NotAfter, "certPem": certPEM, "sans": info.SANs, "keyAlgorithm": info.KeyAlgorithm}
	if strings.TrimSpace(chainPEM) != "" {
		v["chainPem"] = chainPEM
	}
	return v
}

// Package acme issues certificates from an RFC 8555 (ACME) certificate authority for andaru-outbound, which has the
// network: it registers the account, answers the HTTP-01 or DNS-01 challenges and finalizes the engine's CSR. The
// engine, which has no network, keeps the certificate's private key; only the CSR travels over the bus.
package acme

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	xacme "golang.org/x/crypto/acme"

	"github.com/andaru-project/andaru/internal/pki"
)

// The bus topics and event types of an order and its result. Orders carry credentials, so both are published
// non-durable and never reach the bus's log.
const (
	OrderFilter  = "acme-order/#"
	ResultFilter = "acme-result/+"
	JobType      = "acme.order.v1"
	ReadyType    = "acme.http01.ready.v1"
	ResultType   = "acme.result.v1"
	// StagingDirectory is Let's Encrypt's staging directory, which a staging test orders from.
	StagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"
	// HTTP01Addr is where the HTTP-01 responder listens while a challenge is open; the packet filter redirects port 80
	// of the order's segments to it.
	HTTP01Addr = "[::]:8480"
)

// OrderTopic and ResultTopic are the topics of one order's job and result.
func OrderTopic(orderID string) string  { return "acme-order/" + orderID }
func ResultTopic(orderID string) string { return "acme-result/" + orderID }

// Job is one issuance: the account and its credentials, the challenge's provider credentials and the CSR. It must
// never be logged or published durably.
type Job struct {
	JobID        string    `json:"jobId"`
	OrderID      string    `json:"orderId"`
	AccountID    string    `json:"accountId"`
	StagingTest  bool      `json:"stagingTest"`
	DirectoryURL string    `json:"directoryUrl"`
	Email        string    `json:"email,omitempty"`
	AccountKey   string    `json:"accountKey"`
	AccountURI   string    `json:"accountUri,omitempty"`
	EABKeyID     string    `json:"eabKeyId,omitempty"`
	EABHMAC      string    `json:"eabHmac,omitempty"`
	Domains      []string   `json:"domains"`
	CSR          []byte     `json:"csr"`
	Challenge    Challenge  `json:"challenge"`
	Deadline     *time.Time `json:"deadline,omitempty"`
}

// Challenge is how the order proves control of its domains: "http-01" or "dns-01" with a DNS provider.
type Challenge struct {
	Type  string `json:"type"`
	DNS01 *DNS01 `json:"dns01,omitempty"`
}

// DNS01 is a DNS provider and its credentials. The fields a provider does not use are empty.
type DNS01 struct {
	Provider                  string `json:"provider"`
	PropagationTimeoutSeconds int    `json:"propagationTimeoutSeconds,omitempty"`
	// APIToken is cloudflare's, hetzner's and digitalocean's.
	APIToken string `json:"apiToken,omitempty"`
	// APIKey and SecretKey are porkbun's.
	APIKey    string `json:"apiKey,omitempty"`
	SecretKey string `json:"secretKey,omitempty"`
	// ServerURL, Username, Password, Subdomain and FullDomain are an acme-dns registration.
	ServerURL  string `json:"serverUrl,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	Subdomain  string `json:"subdomain,omitempty"`
	FullDomain string `json:"fullDomain,omitempty"`
	// URL, Authorization and AllowPrivateIP are a webhook's.
	URL            string `json:"url,omitempty"`
	Authorization  string `json:"authorization,omitempty"`
	AllowPrivateIP bool   `json:"allowPrivateIp,omitempty"`
}

// HTTP01Ready notifies the engine that andaru-outbound has bound the HTTP-01 responder and is ready for port 80
// redirection to open.
type HTTP01Ready struct {
	JobID   string `json:"jobId"`
	OrderID string `json:"orderId"`
}

// Result is what an issuance returned: the certificate and its chain, or why it failed. It holds no secret.
type Result struct {
	JobID       string `json:"jobId"`
	OrderID     string `json:"orderId"`
	AccountID   string `json:"accountId"`
	StagingTest bool   `json:"stagingTest"`
	OK          bool   `json:"ok"`
	CertPEM     string `json:"certPem,omitempty"`
	ChainPEM    string `json:"chainPem,omitempty"`
	AccountURI  string `json:"accountUri,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Issuer runs jobs. Its zero value talks to the real CA and DNS providers.
type Issuer struct {
	// HTTP talks to the CA; nil is a client that follows no redirect.
	HTTP *http.Client
	// Transport is the base transport of the DNS providers' clients; nil is http.DefaultTransport.
	Transport *http.Transport
	// HTTP01Addr is where the HTTP-01 responder listens; empty is HTTP01Addr.
	HTTP01Addr string
	// LookupTXT resolves TXT records; nil is Go's resolver.
	LookupTXT func(ctx context.Context, name string) ([]string, error)
	// Poll is the wait between propagation checks; zero is two seconds.
	Poll time.Duration
	// Endpoints overrides the providers' API base URLs, by provider name, for tests.
	Endpoints map[string]string
	// OnHTTP01Ready, when set, is called right after the HTTP-01 responder starts listening.
	OnHTTP01Ready func()
}

// Issue runs job to its end and says how it went.
func (is *Issuer) Issue(ctx context.Context, job Job) Result {
	res := Result{JobID: job.JobID, OrderID: job.OrderID, AccountID: job.AccountID, StagingTest: job.StagingTest}
	certPEM, chainPEM, uri, err := is.issue(ctx, job)
	res.AccountURI = uri
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK, res.CertPEM, res.ChainPEM = true, certPEM, chainPEM
	return res
}

func (is *Issuer) issue(ctx context.Context, job Job) (certPEM, chainPEM, accountURI string, err error) {
	key, err := pki.ParseKey([]byte(job.AccountKey))
	if err != nil {
		return "", "", "", errors.New("the account key is not a PEM private key")
	}
	if len(job.Domains) == 0 || len(job.CSR) == 0 {
		return "", "", "", errors.New("the order has no domains or CSR")
	}
	var cleanups []func()
	defer func() {
		for _, c := range slices.Backward(cleanups) {
			c()
		}
	}()
	// Start the HTTP-01 responder before contacting the CA so port 8480 is listening before port 80 redirects.
	var responder *responder
	if job.Challenge.Type == "http-01" {
		addr := is.HTTP01Addr
		if addr == "" {
			addr = HTTP01Addr
		}
		if responder, err = startResponder(addr); err != nil {
			return "", "", "", err
		}
		cleanups = append(cleanups, responder.close)
		if is.OnHTTP01Ready != nil {
			is.OnHTTP01Ready()
		}
	}
	httpClient := is.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	client := &xacme.Client{Key: key, DirectoryURL: job.DirectoryURL, HTTPClient: httpClient, UserAgent: "andaru-outbound", KID: xacme.KeyID(job.AccountURI)}
	if job.AccountURI == "" {
		if err := register(ctx, client, job); err != nil {
			return "", "", "", err
		}
	}
	accountURI = string(client.KID)
	order, err := client.AuthorizeOrder(ctx, xacme.DomainIDs(job.Domains...))
	if err != nil {
		return "", "", accountURI, fmt.Errorf("create the order: %w", acmeError(err))
	}
	if len(order.AuthzURLs) > len(job.Domains) || len(order.AuthzURLs) > 100 {
		return "", "", accountURI, errors.New("the CA returned more authorizations than requested domains")
	}
	allowed := make(map[string]bool, len(job.Domains))
	for _, d := range job.Domains {
		allowed[strings.ToLower(d)] = true
	}
	type pendingAuthz struct {
		authz  *xacme.Authorization
		chal   *xacme.Challenge
		domain string
	}
	var pending []pendingAuthz
	for _, u := range order.AuthzURLs {
		z, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return "", "", accountURI, fmt.Errorf("read an authorization: %w", acmeError(err))
		}
		if z.Identifier.Type != "dns" {
			return "", "", accountURI, fmt.Errorf("the CA requested a non-DNS authorization %q", z.Identifier.Type)
		}
		ident := strings.ToLower(z.Identifier.Value)
		if z.Wildcard && !strings.HasPrefix(ident, "*.") {
			ident = "*." + ident
		}
		if !allowed[ident] {
			return "", "", accountURI, fmt.Errorf("the CA requested an authorization for an unrequested domain %s", z.Identifier.Value)
		}
		if z.Status == xacme.StatusValid {
			continue
		}
		i := slices.IndexFunc(z.Challenges, func(c *xacme.Challenge) bool { return c.Type == job.Challenge.Type })
		if i < 0 {
			return "", "", accountURI, fmt.Errorf("the CA offers no %s challenge for %s", job.Challenge.Type, z.Identifier.Value)
		}
		pending = append(pending, pendingAuthz{
			authz:  z,
			chal:   z.Challenges[i],
			domain: strings.TrimPrefix(strings.ToLower(z.Identifier.Value), "*."),
		})
	}
	switch job.Challenge.Type {
	case "http-01":
		for _, p := range pending {
			answer, err := client.HTTP01ChallengeResponse(p.chal.Token)
			if err != nil {
				return "", "", accountURI, err
			}
			if !responder.add(p.chal.Token, answer) {
				return "", "", accountURI, errors.New("the CA sent an invalid HTTP-01 token")
			}
		}
		for _, p := range pending {
			if _, err := client.Accept(ctx, p.chal); err != nil {
				return "", "", accountURI, fmt.Errorf("accept the challenge for %s: %w", p.authz.Identifier.Value, acmeError(err))
			}
			if _, err := client.WaitAuthorization(ctx, p.authz.URI); err != nil {
				return "", "", accountURI, fmt.Errorf("validate %s: %w", p.authz.Identifier.Value, acmeError(err))
			}
		}
	case "dns-01":
		if job.Challenge.DNS01 == nil {
			return "", "", accountURI, errors.New("the order has no DNS provider")
		}
		// Group authorizations sharing the same _acme-challenge.<domain> owner (e.g. example.com and *.example.com)
		// so all TXT values are published before propagation polling caches the RRset.
		var orderDomains []string
		byDomain := map[string][]pendingAuthz{}
		for _, p := range pending {
			if _, seen := byDomain[p.domain]; !seen {
				orderDomains = append(orderDomains, p.domain)
			}
			byDomain[p.domain] = append(byDomain[p.domain], p)
		}
		dnsCfg := *job.Challenge.DNS01
		for _, domain := range orderDomains {
			group := byDomain[domain]
			// acme-dns retains the last two TXT values per subdomain; batch by 2 if a domain somehow has more.
			batchSize := len(group)
			if dnsCfg.Provider == "acme-dns" && batchSize > 2 {
				batchSize = 2
			}
			for start := 0; start < len(group); start += batchSize {
				batch := group[start:min(start+batchSize, len(group))]
				values := make([]string, 0, len(batch))
				for _, item := range batch {
					value, err := client.DNS01ChallengeRecord(item.chal.Token)
					if err != nil {
						return "", "", accountURI, err
					}
					cleanup, err := is.publishDNS(ctx, dnsCfg, domain, value)
					if cleanup != nil {
						cleanups = append(cleanups, cleanup)
					}
					if err != nil {
						return "", "", accountURI, err
					}
					values = append(values, value)
				}
				fqdn := "_acme-challenge." + domain
				if dnsCfg.Provider == "acme-dns" && dnsCfg.FullDomain != "" {
					fqdn = dnsCfg.FullDomain
				}
				if err := is.propagated(ctx, dnsCfg, fqdn, values); err != nil {
					return "", "", accountURI, err
				}
				for _, item := range batch {
					if _, err := client.Accept(ctx, item.chal); err != nil {
						return "", "", accountURI, fmt.Errorf("accept the challenge for %s: %w", item.authz.Identifier.Value, acmeError(err))
					}
					if _, err := client.WaitAuthorization(ctx, item.authz.URI); err != nil {
						return "", "", accountURI, fmt.Errorf("validate %s: %w", item.authz.Identifier.Value, acmeError(err))
					}
				}
			}
		}
	default:
		if len(pending) > 0 {
			return "", "", accountURI, fmt.Errorf("unsupported challenge %q", job.Challenge.Type)
		}
	}
	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return "", "", accountURI, fmt.Errorf("wait for the order: %w", acmeError(err))
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, job.CSR, true)
	if err != nil {
		return "", "", accountURI, fmt.Errorf("finalize the order: %w", acmeError(err))
	}
	if len(der) == 0 {
		return "", "", accountURI, errors.New("the CA returned no certificate")
	}
	if len(der) > 10 {
		return "", "", accountURI, errors.New("the CA returned too many certificates in the chain")
	}
	certOut, chainOut := string(encodeDER(der[:1])), string(encodeDER(der[1:]))
	if len(certOut) > 65536 || len(chainOut) > 65536 {
		return "", "", accountURI, errors.New("the issued certificate or chain exceeds the size limit")
	}
	return certOut, chainOut, accountURI, nil
}

// register creates the account, or finds the one the key already has.
func register(ctx context.Context, client *xacme.Client, job Job) error {
	acct := &xacme.Account{}
	if job.Email != "" {
		acct.Contact = []string{"mailto:" + job.Email}
	}
	if job.EABKeyID != "" {
		hmac, err := decodeEAB(job.EABHMAC)
		if err != nil {
			return err
		}
		acct.ExternalAccountBinding = &xacme.ExternalAccountBinding{KID: job.EABKeyID, Key: hmac}
	}
	_, err := client.Register(ctx, acct, xacme.AcceptTOS)
	if err != nil && !errors.Is(err, xacme.ErrAccountAlreadyExists) {
		return fmt.Errorf("register the account: %w", acmeError(err))
	}
	if errors.Is(err, xacme.ErrAccountAlreadyExists) || client.KID == "" {
		existing, regErr := client.GetReg(ctx, "")
		if regErr != nil {
			return fmt.Errorf("look up existing account: %w", acmeError(regErr))
		}
		client.KID = xacme.KeyID(existing.URI)
	}
	if client.KID == "" {
		return errors.New("the CA returned no account URL")
	}
	return nil
}

// decodeEAB reads an EAB HMAC key, base64url as CAs hand it out, padded or not.
func decodeEAB(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, errors.New("the EAB HMAC key is not base64url")
	}
	return b, nil
}

// acmeError keeps a CA's problem document's type and detail, never a request URL or oversized body.
func acmeError(err error) error {
	var msg string
	var e *xacme.Error
	var oe *xacme.OrderError
	var ae *xacme.AuthorizationError
	var ue *url.Error
	switch {
	case errors.As(err, &e):
		msg = fmt.Sprintf("%s: %s", e.ProblemType, e.Detail)
	case errors.As(err, &oe):
		msg = fmt.Sprintf("order %s", oe.Status)
	case errors.As(err, &ae):
		msg = fmt.Sprintf("authorization of %s failed", ae.Identifier)
	case errors.As(err, &ue) && ue.Err != nil:
		msg = ue.Err.Error()
	default:
		msg = err.Error()
	}
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return errors.New(msg)
}

func encodeDER(der [][]byte) []byte {
	var out []byte
	for _, b := range der {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})...)
	}
	return out
}

// NewCSR is a CSR for domains signed by key, the first domain its common name.
func NewCSR(key crypto.Signer, domains []string) ([]byte, error) {
	if len(domains) == 0 {
		return nil, errors.New("no domains")
	}
	tmpl := &x509.CertificateRequest{DNSNames: domains}
	tmpl.Subject.CommonName = domains[0]
	return x509.CreateCertificateRequest(rand.Reader, tmpl, key)
}

// lookupTXT is Go's resolver, never the C library's.
func lookupTXT(ctx context.Context, name string) ([]string, error) {
	return (&net.Resolver{PreferGo: true}).LookupTXT(ctx, name)
}

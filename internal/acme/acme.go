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
	Domains      []string  `json:"domains"`
	CSR          []byte    `json:"csr"`
	Challenge    Challenge `json:"challenge"`
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
	var cleanups []func()
	defer func() {
		for _, c := range slices.Backward(cleanups) {
			c()
		}
	}()
	var responder *responder
	for _, u := range order.AuthzURLs {
		z, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return "", "", accountURI, fmt.Errorf("read an authorization: %w", acmeError(err))
		}
		if z.Status == xacme.StatusValid {
			continue
		}
		i := slices.IndexFunc(z.Challenges, func(c *xacme.Challenge) bool { return c.Type == job.Challenge.Type })
		if i < 0 {
			return "", "", accountURI, fmt.Errorf("the CA offers no %s challenge for %s", job.Challenge.Type, z.Identifier.Value)
		}
		chal := z.Challenges[i]
		switch job.Challenge.Type {
		case "http-01":
			if responder == nil {
				addr := is.HTTP01Addr
				if addr == "" {
					addr = HTTP01Addr
				}
				if responder, err = startResponder(addr); err != nil {
					return "", "", accountURI, err
				}
				cleanups = append(cleanups, responder.close)
			}
			answer, err := client.HTTP01ChallengeResponse(chal.Token)
			if err != nil {
				return "", "", accountURI, err
			}
			if !responder.add(chal.Token, answer) {
				return "", "", accountURI, errors.New("the CA sent an invalid HTTP-01 token")
			}
		case "dns-01":
			if job.Challenge.DNS01 == nil {
				return "", "", accountURI, errors.New("the order has no DNS provider")
			}
			value, err := client.DNS01ChallengeRecord(chal.Token)
			if err != nil {
				return "", "", accountURI, err
			}
			domain := strings.TrimPrefix(z.Identifier.Value, "*.")
			cleanup, err := is.present(ctx, *job.Challenge.DNS01, domain, value)
			if cleanup != nil {
				cleanups = append(cleanups, cleanup)
			}
			if err != nil {
				return "", "", accountURI, err
			}
		default:
			return "", "", accountURI, fmt.Errorf("unsupported challenge %q", job.Challenge.Type)
		}
		if _, err := client.Accept(ctx, chal); err != nil {
			return "", "", accountURI, fmt.Errorf("accept the challenge for %s: %w", z.Identifier.Value, acmeError(err))
		}
		if _, err := client.WaitAuthorization(ctx, z.URI); err != nil {
			return "", "", accountURI, fmt.Errorf("validate %s: %w", z.Identifier.Value, acmeError(err))
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
	return string(encodeDER(der[:1])), string(encodeDER(der[1:])), accountURI, nil
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

// acmeError keeps a CA's problem document's type and detail, never a request.
func acmeError(err error) error {
	var e *xacme.Error
	if errors.As(err, &e) {
		return fmt.Errorf("%s: %s", e.ProblemType, e.Detail)
	}
	var oe *xacme.OrderError
	if errors.As(err, &oe) {
		return fmt.Errorf("order %s", oe.Status)
	}
	var ae *xacme.AuthorizationError
	if errors.As(err, &ae) {
		return fmt.Errorf("authorization of %s failed", ae.Identifier)
	}
	return err
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

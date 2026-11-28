// Package pki issues and inspects the X.509 certificates and OpenVPN static keys Andaru manages. It uses the
// standard library alone and performs no I/O: callers store what it returns.
package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Algorithms are the key algorithms Andaru generates and accepts.
var Algorithms = []string{"ecdsa-p256", "ecdsa-p384", "rsa-2048", "rsa-4096"}

// Issuer is a CA certificate with its private key, able to sign.
type Issuer struct {
	Cert *x509.Certificate
	Key  crypto.Signer
}

// Info is what Andaru records of a certificate beside its PEM.
type Info struct {
	Subject           string
	SANs              []string
	Serial            string
	FingerprintSHA256 string
	NotBefore         string
	NotAfter          string
	KeyAlgorithm      string
	IsCA              bool
}

// Issued is a newly signed certificate and its new private key, both PEM.
type Issued struct {
	CertPEM, KeyPEM []byte
	Info            Info
}

// CARequest describes a root or intermediate CA.
type CARequest struct {
	CommonName, Organization, KeyAlgorithm string
	ValidDays                              int
}

// LeafRequest describes a certificate signed by a CA. Usage is server, client or both.
type LeafRequest struct {
	CommonName, Usage, KeyAlgorithm string
	SANs                            []string
	ValidDays                       int
}

var dnsName = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// now is the clock certificates are dated by; tests replace it.
var now = time.Now

// GenerateKey makes a private key of one of Algorithms.
func GenerateKey(algorithm string) (crypto.Signer, error) {
	switch algorithm {
	case "ecdsa-p256":
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "ecdsa-p384":
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "rsa-2048":
		return rsa.GenerateKey(rand.Reader, 2048)
	case "rsa-4096":
		return rsa.GenerateKey(rand.Reader, 4096)
	}
	return nil, fmt.Errorf("unsupported key algorithm %q", algorithm)
}

// NewCA issues a CA: self-signed when parent is nil, otherwise an intermediate signed by parent.
func NewCA(req CARequest, parent *Issuer) (Issued, error) {
	if req.ValidDays < 1 || req.ValidDays > 36500 {
		return Issued{}, errors.New("validDays must be 1 to 36500")
	}
	if err := checkName(req.CommonName); err != nil {
		return Issued{}, err
	}
	if strings.ContainsFunc(req.Organization, isControl) || len(req.Organization) > 64 {
		return Issued{}, errors.New("organization must be at most 64 characters without control characters")
	}
	subject := pkix.Name{CommonName: req.CommonName}
	if req.Organization != "" {
		subject.Organization = []string{req.Organization}
	}
	template := &x509.Certificate{
		Subject:               subject,
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		MaxPathLen:            -1,
	}
	if parent != nil {
		if !parent.Cert.IsCA {
			return Issued{}, errors.New("the parent is not a CA")
		}
		template.MaxPathLen, template.MaxPathLenZero = 0, true
	}
	return issue(template, req.KeyAlgorithm, req.ValidDays, parent)
}

// NewLeaf issues a server, client or dual-use certificate signed by issuer.
func NewLeaf(req LeafRequest, issuer Issuer) (Issued, error) {
	if req.ValidDays < 1 || req.ValidDays > 3650 {
		return Issued{}, errors.New("validDays must be 1 to 3650")
	}
	if err := checkName(req.CommonName); err != nil {
		return Issued{}, err
	}
	template := &x509.Certificate{Subject: pkix.Name{CommonName: req.CommonName}, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature}
	switch req.Usage {
	case "server":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	case "client":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	case "both":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	default:
		return Issued{}, errors.New(`usage must be "server", "client" or "both"`)
	}
	if strings.HasPrefix(req.KeyAlgorithm, "rsa-") {
		template.KeyUsage |= x509.KeyUsageKeyEncipherment
	}
	sans := req.SANs
	if len(sans) == 0 && req.Usage != "client" && validSAN(req.CommonName) {
		sans = []string{req.CommonName} // a TLS client checks a server's SANs alone
	}
	if len(sans) > 100 {
		return Issued{}, errors.New("at most 100 subject alternative names")
	}
	for _, san := range sans {
		if a, err := netip.ParseAddr(san); err == nil && a.Zone() == "" {
			template.IPAddresses = append(template.IPAddresses, net.IP(a.AsSlice()))
		} else if validSAN(san) {
			template.DNSNames = append(template.DNSNames, strings.ToLower(san))
		} else {
			return Issued{}, fmt.Errorf("subject alternative name %q is neither a DNS name nor an IP address", san)
		}
	}
	if !issuer.Cert.IsCA {
		return Issued{}, errors.New("the issuer is not a CA")
	}
	return issue(template, req.KeyAlgorithm, req.ValidDays, &issuer)
}

func issue(template *x509.Certificate, algorithm string, days int, parent *Issuer) (Issued, error) {
	key, err := GenerateKey(algorithm)
	if err != nil {
		return Issued{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return Issued{}, err
	}
	start := now().UTC().Truncate(time.Second)
	template.SerialNumber = new(big.Int).Add(serial, big.NewInt(1))
	template.NotBefore = start.Add(-5 * time.Minute) // tolerate a peer's clock running behind
	template.NotAfter = start.AddDate(0, 0, days)
	signerCert, signerKey := template, crypto.Signer(key)
	if parent != nil {
		signerCert, signerKey = parent.Cert, parent.Key
		// A certificate never outlives the CA that signs it.
		template.NotAfter = minTime(template.NotAfter, parent.Cert.NotAfter)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, signerCert, key.Public(), signerKey)
	if err != nil {
		return Issued{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Issued{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	info, _, err := Inspect(certPEM)
	if err != nil {
		return Issued{}, err
	}
	return Issued{CertPEM: certPEM, KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), Info: info}, nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func checkName(cn string) error {
	if cn == "" || len(cn) > 64 || strings.ContainsFunc(cn, isControl) {
		return errors.New("commonName must be 1 to 64 characters without control characters")
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func validSAN(s string) bool { return len(s) <= 253 && dnsName.MatchString(s) }

// Certificates decodes PEM holding only CERTIFICATE blocks, at least one. Text around the blocks is not allowed.
func Certificates(data []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := bytes.TrimSpace(data)
	for len(rest) > 0 {
		block, next := pem.Decode(rest)
		// pem.Decode skips text before a block; none is allowed.
		if block == nil || !bytes.HasPrefix(rest, []byte("-----BEGIN ")) || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("expected PEM CERTIFICATE blocks only")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		out = append(out, cert)
		rest = bytes.TrimSpace(next)
	}
	if len(out) == 0 {
		return nil, errors.New("expected a PEM certificate")
	}
	return out, nil
}

// Inspect reads PEM holding exactly one certificate.
func Inspect(certPEM []byte) (Info, *x509.Certificate, error) {
	certs, err := Certificates(certPEM)
	if err != nil {
		return Info{}, nil, err
	}
	if len(certs) != 1 {
		return Info{}, nil, errors.New("expected exactly one certificate")
	}
	c := certs[0]
	algorithm, err := keyAlgorithm(c.PublicKey)
	if err != nil {
		return Info{}, nil, err
	}
	sum := sha256.Sum256(c.Raw)
	sans := slices.Clone(c.DNSNames)
	for _, ip := range c.IPAddresses {
		sans = append(sans, ip.String())
	}
	if sans == nil {
		sans = []string{}
	}
	return Info{
		Subject:           c.Subject.String(),
		SANs:              sans,
		Serial:            fmt.Sprintf("%x", c.SerialNumber),
		FingerprintSHA256: hex.EncodeToString(sum[:]),
		NotBefore:         c.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:          c.NotAfter.UTC().Format(time.RFC3339),
		KeyAlgorithm:      algorithm,
		IsCA:              c.IsCA,
	}, c, nil
}

func keyAlgorithm(public any) (string, error) {
	switch k := public.(type) {
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256():
			return "ecdsa-p256", nil
		case elliptic.P384():
			return "ecdsa-p384", nil
		}
	case *rsa.PublicKey:
		switch k.N.BitLen() {
		case 2048:
			return "rsa-2048", nil
		case 4096:
			return "rsa-4096", nil
		}
	}
	return "", errors.New("unsupported key: expected ECDSA P-256 or P-384, or RSA 2048 or 4096")
}

// ParseKey decodes one unencrypted PEM private key: PKCS #8, SEC 1 or PKCS #1.
func ParseKey(keyPEM []byte) (crypto.Signer, error) {
	trimmed := bytes.TrimSpace(keyPEM)
	block, rest := pem.Decode(trimmed)
	if block == nil || !bytes.HasPrefix(trimmed, []byte("-----BEGIN ")) || len(bytes.TrimSpace(rest)) != 0 || len(block.Headers) != 0 {
		return nil, errors.New("expected exactly one unencrypted PEM private key")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, errors.New("expected an unencrypted PEM private key")
	}
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("unsupported private key")
	}
	if _, err := keyAlgorithm(signer.Public()); err != nil {
		return nil, err
	}
	return signer, nil
}

// Matches reports whether key is the private key of cert.
func Matches(cert *x509.Certificate, key crypto.Signer) bool {
	public, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	return ok && public.Equal(cert.PublicKey)
}

// ParseIssuer reads a CA certificate and its private key.
func ParseIssuer(certPEM, keyPEM []byte) (Issuer, error) {
	_, cert, err := Inspect(certPEM)
	if err != nil {
		return Issuer{}, err
	}
	key, err := ParseKey(keyPEM)
	if err != nil {
		return Issuer{}, err
	}
	if !cert.IsCA || !Matches(cert, key) {
		return Issuer{}, errors.New("the CA certificate and key do not form a signing CA")
	}
	return Issuer{Cert: cert, Key: key}, nil
}

// Imported is an external certificate checked and split into its leaf and chain.
type Imported struct {
	CertPEM, ChainPEM []byte
	Info              Info
}

// Import checks an external leaf certificate, its optional chain and its private key: every block parses, the key is
// the leaf's, the leaf is current, and each certificate is signed by the next. Certificates after the first in
// certPEM are taken as the start of the chain.
func Import(certPEM, chainPEM, keyPEM []byte) (Imported, error) {
	certs, err := Certificates(certPEM)
	if err != nil {
		return Imported{}, err
	}
	if len(bytes.TrimSpace(chainPEM)) > 0 {
		chain, err := Certificates(chainPEM)
		if err != nil {
			return Imported{}, fmt.Errorf("chain: %w", err)
		}
		certs = append(certs, chain...)
	}
	if len(certs) > 10 {
		return Imported{}, errors.New("at most 9 chain certificates")
	}
	key, err := ParseKey(keyPEM)
	if err != nil {
		return Imported{}, err
	}
	leaf := certs[0]
	if !Matches(leaf, key) {
		return Imported{}, errors.New("the private key does not match the certificate")
	}
	if now().After(leaf.NotAfter) {
		return Imported{}, errors.New("the certificate has expired")
	}
	for i := 1; i < len(certs); i++ {
		if !certs[i].IsCA {
			return Imported{}, fmt.Errorf("chain certificate %d is not a CA", i)
		}
		if err := certs[i-1].CheckSignatureFrom(certs[i]); err != nil {
			return Imported{}, fmt.Errorf("chain certificate %d did not sign the certificate before it", i)
		}
	}
	out := Imported{CertPEM: encode(certs[:1])}
	if len(certs) > 1 {
		out.ChainPEM = encode(certs[1:])
	}
	if out.Info, _, err = Inspect(out.CertPEM); err != nil {
		return Imported{}, err
	}
	return out, nil
}

// Encode is certs as PEM.
func Encode(certs ...*x509.Certificate) []byte { return encode(certs) }

func encode(certs []*x509.Certificate) []byte {
	var b bytes.Buffer
	for _, c := range certs {
		_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	return b.Bytes()
}

const (
	staticKeyBegin = "-----BEGIN OpenVPN Static key V1-----"
	staticKeyEnd   = "-----END OpenVPN Static key V1-----"
)

// TLSCryptKey generates an OpenVPN 2048-bit static key, as openvpn --genkey writes it but without its comments.
func TLSCryptKey() ([]byte, error) {
	var raw [256]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(staticKeyBegin + "\n")
	for i := 0; i < len(raw); i += 16 {
		b.WriteString(hex.EncodeToString(raw[i:i+16]) + "\n")
	}
	b.WriteString(staticKeyEnd + "\n")
	return []byte(b.String()), nil
}

// ValidTLSCryptKey reports whether key is an OpenVPN 2048-bit static key: 16 lines of 32 hex digits between its markers.
func ValidTLSCryptKey(key []byte) bool {
	lines := strings.Split(strings.TrimSpace(string(key)), "\n")
	if len(lines) != 18 || lines[0] != staticKeyBegin || lines[17] != staticKeyEnd {
		return false
	}
	for _, line := range lines[1:17] {
		if _, err := hex.DecodeString(line); err != nil || len(line) != 32 {
			return false
		}
	}
	return true
}

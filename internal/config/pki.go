package config

import (
	"context"
	"crypto/x509"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/pki"
)

// pkiSettings are the certificate authorities, certificates and OpenVPN static keys Andaru manages. They take effect
// when saved: the services that use one name it (management.certificate, an add-on's certificate-reference) and
// those references are what an apply realizes.
type pkiSettings struct {
	CAs          []pkiCA          `json:"cas,omitempty"`
	Certificates []pkiCertificate `json:"certificates,omitempty"`
	TLSCryptKeys []tlsCryptKey    `json:"tlsCryptKeys,omitempty"`
}

// pkiCertInfo is a certificate's PEM and what is read from it, recorded so lists need not parse it.
type pkiCertInfo struct {
	Subject           string `json:"subject"`
	Serial            string `json:"serial"`
	FingerprintSHA256 string `json:"fingerprintSha256"`
	NotBefore         string `json:"notBefore"`
	NotAfter          string `json:"notAfter"`
	CertPEM           string `json:"certPem"`
}

// pkiCA is a CA; one without a key is a trust anchor only, which signs nothing.
type pkiCA struct {
	identity
	ParentCAID string `json:"parentCaId,omitempty"`
	pkiCertInfo
	KeyRef *secretReference `json:"keyRef,omitempty"`
}

// pkiCertificate is a certificate and its private key; CAID names the managed CA that signed it, if any.
type pkiCertificate struct {
	identity
	CAID string `json:"caId,omitempty"`
	pkiCertInfo
	SANs         []string        `json:"sans"`
	ChainPEM     string          `json:"chainPem,omitempty"`
	KeyRef       secretReference `json:"keyRef"`
	KeyAlgorithm string          `json:"keyAlgorithm"`
}

// tlsCryptKey is an OpenVPN tls-crypt static key.
type tlsCryptKey struct {
	identity
	KeyRef secretReference `json:"keyRef"`
}

var pkiCollections = []string{"pki.cas", "pki.certificates", "pki.tlsCryptKeys"}

var certificateIDPattern = regexp.MustCompile(`^crt_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// keyRefOf is a PKI object's key reference, nil for any other object or a CA without a key.
func keyRefOf(v any) *secretReference {
	switch x := v.(type) {
	case *pkiCA:
		return x.KeyRef
	case *pkiCertificate:
		return &x.KeyRef
	case *tlsCryptKey:
		return &x.KeyRef
	}
	return nil
}

// addonCertificateRefs are the certificates an add-on's settings name: a certificate-reference setting's value is
// the certificate's ID, and no other setting holds one.
func addonCertificateRefs(a *addonConfig) []string {
	var out []string
	for _, v := range a.Settings {
		if s, ok := v.(string); ok && certificateIDPattern.MatchString(s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

func certificateIndex(d *declaration) map[string]*pkiCertificate {
	out := make(map[string]*pkiCertificate, len(d.PKI.Certificates))
	for i := range d.PKI.Certificates {
		out[d.PKI.Certificates[i].ID] = &d.PKI.Certificates[i]
	}
	return out
}

// validatePKI checks each PKI object's names and that its recorded fields are those of its PEM, and the references
// to them: a certificate's CA, management.certificate and add-on certificate settings, enabled or not.
func validatePKI(d *declaration) []FieldError {
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
	matches := func(p string, info pki.Info, c pkiCertInfo) {
		if info.Subject != c.Subject || info.Serial != c.Serial || info.FingerprintSHA256 != c.FingerprintSHA256 || info.NotBefore != c.NotBefore || info.NotAfter != c.NotAfter {
			bad(p+"/certPem", "The subject, serial, fingerprint and validity must be those of the certificate.")
		}
	}
	cas := map[string]bool{}
	caCerts := map[string]*x509.Certificate{}
	for i, ca := range d.PKI.CAs {
		p := fmt.Sprintf("/pki/cas/%d", i)
		unique(p, ca.ID, ca.Name, cas)
		info, cert, err := pki.Inspect([]byte(ca.CertPEM))
		switch {
		case err != nil:
			bad(p+"/certPem", "Expected one PEM certificate: "+err.Error()+".")
		case !info.IsCA:
			bad(p+"/certPem", "The certificate is not a CA certificate.")
		default:
			matches(p, info, ca.pkiCertInfo)
			caCerts[ca.ID] = cert
		}
	}
	for i, ca := range d.PKI.CAs {
		if ca.ParentCAID != "" && (!cas[ca.ParentCAID] || ca.ParentCAID == ca.ID) {
			bad(fmt.Sprintf("/pki/cas/%d/parentCaId", i), "Expected another certificate authority of this configuration.")
		}
	}
	seen := map[string]bool{}
	for i, c := range d.PKI.Certificates {
		p := fmt.Sprintf("/pki/certificates/%d", i)
		unique(p, c.ID, c.Name, seen)
		if c.CAID != "" && !cas[c.CAID] {
			bad(p+"/caId", "Expected a certificate authority of this configuration.")
		}
		info, cert, err := pki.Inspect([]byte(c.CertPEM))
		if err != nil {
			bad(p+"/certPem", "Expected one PEM certificate: "+err.Error()+".")
			continue
		}
		if info.IsCA {
			bad(p+"/certPem", "Expected a leaf certificate, not a CA certificate.")
		}
		if c.CAID != "" {
			if issuerCert := caCerts[c.CAID]; issuerCert != nil {
				if err := cert.CheckSignatureFrom(issuerCert); err != nil {
					bad(p+"/caId", "The certificate was not signed by the named certificate authority.")
				}
			}
		}
		matches(p, info, c.pkiCertInfo)
		if !slices.Equal(info.SANs, c.SANs) {
			bad(p+"/sans", "The subject alternative names must be those of the certificate.")
		}
		if info.KeyAlgorithm != c.KeyAlgorithm {
			bad(p+"/keyAlgorithm", "The key algorithm must be that of the certificate.")
		}
		if c.ChainPEM != "" {
			if _, err := pki.Certificates([]byte(c.ChainPEM)); err != nil {
				bad(p+"/chainPem", "Expected PEM certificates: "+err.Error()+".")
			}
		}
	}
	keys := map[string]bool{}
	for i, k := range d.PKI.TLSCryptKeys {
		unique(fmt.Sprintf("/pki/tlsCryptKeys/%d", i), k.ID, k.Name, keys)
	}
	if d.Management != nil && d.Management.Certificate != "" && !seen[d.Management.Certificate] {
		bad("/management/certificate", "Expected a certificate of this configuration.")
	}
	for i := range d.Addons {
		for _, k := range slices.Sorted(maps.Keys(d.Addons[i].Settings)) {
			if id, ok := d.Addons[i].Settings[k].(string); ok && certificateIDPattern.MatchString(id) && !seen[id] {
				bad(fmt.Sprintf("/addons/%d/settings/%s", i, escapeJSONPointer(k)), "Expected a certificate of this configuration.")
			}
		}
	}
	return out
}

// checkAllPKIKeys checks every resolved PKI secret in secrets against its PKI object in d (used on backup restore).
func checkAllPKIKeys(d *declaration, secrets secretDocument) []FieldError {
	written := map[objectKey][]byte{}
	for _, ca := range d.PKI.CAs {
		written[objectKey{"pki.cas", ca.ID}] = nil
	}
	for _, c := range d.PKI.Certificates {
		written[objectKey{"pki.certificates", c.ID}] = nil
	}
	for _, k := range d.PKI.TLSCryptKeys {
		written[objectKey{"pki.tlsCryptKeys", k.ID}] = nil
	}
	return checkPKIKeys(d, secrets, written, nil)
}

// checkPKIKeys checks each PKI key a batch writes or sets: a CA's or certificate's key is the private key of its
// certificate, and a tls-crypt key is an OpenVPN static key. Unresolved keys, left by a restore, are not known.
func checkPKIKeys(d *declaration, secrets secretDocument, written map[objectKey][]byte, changed map[string]bool) []FieldError {
	value := func(collection, id string, ref *secretReference) (string, bool) {
		if ref == nil || ref.Unresolved {
			return "", false
		}
		_, w := written[objectKey{collection, id}]
		if !w && !changed["core\x00"+ref.Secret] {
			return "", false
		}
		return secrets.Scopes["core"][ref.Secret].Value, true
	}
	var out []FieldError
	pair := func(p, certPEM, keyPEM string) {
		_, cert, err := pki.Inspect([]byte(certPEM))
		if err != nil {
			return // validatePKI reports it
		}
		key, err := pki.ParseKey([]byte(keyPEM))
		if err != nil {
			out = append(out, FieldError{p + "/keyRef", "Expected the secret to hold a PEM private key: " + err.Error() + "."})
		} else if !pki.Matches(cert, key) {
			out = append(out, FieldError{p + "/keyRef", "The private key does not match the certificate."})
		}
	}
	for i, ca := range d.PKI.CAs {
		if v, ok := value("pki.cas", ca.ID, ca.KeyRef); ok {
			pair(fmt.Sprintf("/pki/cas/%d", i), ca.CertPEM, v)
		}
	}
	for i, c := range d.PKI.Certificates {
		if v, ok := value("pki.certificates", c.ID, &c.KeyRef); ok {
			pair(fmt.Sprintf("/pki/certificates/%d", i), c.CertPEM, v)
		}
	}
	for i, k := range d.PKI.TLSCryptKeys {
		if v, ok := value("pki.tlsCryptKeys", k.ID, &k.KeyRef); ok && !pki.ValidTLSCryptKey([]byte(v)) {
			out = append(out, FieldError{fmt.Sprintf("/pki/tlsCryptKeys/%d/keyRef", i), "Expected the secret to hold an OpenVPN static key."})
		}
	}
	return out
}

// PKIObject is one saved CA, certificate or tls-crypt key with its PEM and private key, for signing, download and
// deployment. Key is empty when the object has none or it is unresolved; it must never be logged or sent.
type PKIObject struct {
	ID, Name, CAID    string
	CertPEM, ChainPEM string
	Key               string
	Unresolved        bool
}

// PKIObject returns one object of a PKI collection of the saved head.
func (s *Store) PKIObject(ctx context.Context, collection, id string) (result PKIObject, err error) {
	if !slices.Contains(pkiCollections, collection) {
		return result, fault("NOT_FOUND", "", "Object does not exist.")
	}
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		v, ok := objects(&state.Config)[objectKey{collection, id}]
		if !ok {
			return fault("NOT_FOUND", "", "Object does not exist.")
		}
		switch x := v.(type) {
		case *pkiCA:
			result = PKIObject{ID: x.ID, Name: x.Name, CertPEM: x.CertPEM}
		case *pkiCertificate:
			result = PKIObject{ID: x.ID, Name: x.Name, CAID: x.CAID, CertPEM: x.CertPEM, ChainPEM: x.ChainPEM}
		case *tlsCryptKey:
			result = PKIObject{ID: x.ID, Name: x.Name}
		}
		if ref := keyRefOf(v); ref != nil {
			result.Unresolved = ref.Unresolved
			if !ref.Unresolved {
				result.Key = state.Secrets.Scopes["core"][ref.Secret].Value
			}
		}
		return nil
	})
	return
}

// PKICertificateAuthorities returns every CA certificate of the saved head, keys left out, for building chains.
func (s *Store) PKICertificateAuthorities(ctx context.Context) (result []PKIObject, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		for _, ca := range state.Config.PKI.CAs {
			result = append(result, PKIObject{ID: ca.ID, Name: ca.Name, CertPEM: ca.CertPEM})
		}
		return nil
	})
	return
}

// CertificateConsumer is one service's binding to a certificate: the management web server, or an add-on's
// certificate-reference setting.
type CertificateConsumer struct {
	// Kind is "management", "addon" or "ipsec" (an enabled connection's local certificate).
	Kind string
	// AddonID is the add-on's catalog ID, and Setting the name of its certificate-reference setting.
	AddonID, Setting string
	CertificateID    string
}

// CertificateMaterial is a certificate as the saved head holds it now, with its private key.
type CertificateMaterial struct {
	Name, CertPEM, ChainPEM, Key string
	Unresolved                   bool
}

// CertificateBindings returns the consumers of certificates in the saved version live (the applied one), and the
// head's current certificate and key for each certificate they name: a renewed or replaced certificate is served at
// once by what is applied, while a binding saved but not applied is not.
func (s *Store) CertificateBindings(ctx context.Context, live Version) (consumers []CertificateConsumer, material map[string]CertificateMaterial, err error) {
	if live != "" {
		err = s.read(ctx, live, nil, func(state savedState, _ VersionInfo) error {
			if m := state.Config.Management; m != nil && m.Certificate != "" {
				consumers = append(consumers, CertificateConsumer{Kind: "management", CertificateID: m.Certificate})
			}
			for i := range state.Config.Addons {
				a := &state.Config.Addons[i]
				if !a.Enabled {
					continue
				}
				for _, k := range slices.Sorted(maps.Keys(a.Settings)) {
					if v, ok := a.Settings[k].(string); ok && certificateIDPattern.MatchString(v) {
						consumers = append(consumers, CertificateConsumer{Kind: "addon", AddonID: a.ID, Setting: k, CertificateID: v})
					}
				}
			}
			for _, c := range state.Config.VPN.IPsec {
				if !c.Enabled {
					continue
				}
				if c.Auth.LocalCertID != "" {
					consumers = append(consumers, CertificateConsumer{Kind: "ipsec", CertificateID: c.Auth.LocalCertID})
				}
				if c.Auth.CAID != "" {
					consumers = append(consumers, CertificateConsumer{Kind: "ipsec-ca", CertificateID: c.Auth.CAID})
				}
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	material = map[string]CertificateMaterial{}
	if len(consumers) == 0 {
		return consumers, material, nil
	}
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		certs := certificateIndex(&state.Config)
		cas := map[string]*pkiCA{}
		for i := range state.Config.PKI.CAs {
			cas[state.Config.PKI.CAs[i].ID] = &state.Config.PKI.CAs[i]
		}
		for _, c := range consumers {
			if c.Kind == "ipsec-ca" {
				if ca, ok := cas[c.CertificateID]; ok {
					material[c.CertificateID] = CertificateMaterial{Name: ca.Name, CertPEM: ca.CertPEM}
				}
				continue
			}
			x, ok := certs[c.CertificateID]
			if !ok {
				continue // deleted since: the consumer keeps what it has until an apply
			}
			m := CertificateMaterial{Name: x.Name, CertPEM: x.CertPEM, ChainPEM: x.ChainPEM, Unresolved: x.KeyRef.Unresolved}
			if !x.KeyRef.Unresolved {
				m.Key = state.Secrets.Scopes["core"][x.KeyRef.Secret].Value
			}
			material[c.CertificateID] = m
		}
		return nil
	})
	return consumers, material, err
}

// PKISecretName is the name a PKI object's key secret is created under, derived from the object's ID.
func PKISecretName(id string) string {
	prefix, uuid, _ := strings.Cut(id, "_")
	return "pki_" + prefix + "_" + strings.ReplaceAll(uuid, "-", "")
}

package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/pki"
)

func pkiBody(t *testing.T, name string, issued pki.Issued, extra map[string]any) []byte {
	t.Helper()
	v := map[string]any{"name": name, "subject": issued.Info.Subject, "serial": issued.Info.Serial,
		"fingerprintSha256": issued.Info.FingerprintSHA256, "notBefore": issued.Info.NotBefore, "notAfter": issued.Info.NotAfter,
		"certPem": string(issued.CertPEM)}
	for k, x := range extra {
		v[k] = x
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func keyWrite(name string, key []byte) map[string]any {
	return map[string]any{"secret": name, "set": string(key), "expectedVersion": "0"}
}

func pkiCreate(t *testing.T, s *savedState, collection string, body []byte, next Version) (string, preparedChange, error) {
	t.Helper()
	id, err := NewID(collection)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ObjectCreateWithID(collection, id, body)
	if err != nil {
		t.Fatal(err)
	}
	p, err := prepareChanges(s, c, next, fixedTime)
	return id, p, err
}

func mustSave(t *testing.T, s savedState, c Changes, next Version) savedState {
	t.Helper()
	p, err := prepareChanges(&s, c, next, fixedTime)
	if err != nil {
		t.Fatalf("save %s: %+v", next, err)
	}
	return p.State
}

func faultText(err error) string {
	if f, ok := err.(*Fault); ok {
		raw, _ := json.Marshal(f.Details)
		return f.Code + " " + string(raw)
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// A CA and a certificate it signed save with their keys as pki secrets, show neither key, and are protected from
// deletion while referenced.
func TestPKILifecycle(t *testing.T) {
	s := seededState(t)
	ca, err := pki.NewCA(pki.CARequest{CommonName: "Andaru CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 365}, nil)
	if err != nil {
		t.Fatal(err)
	}
	caID, p, err := pkiCreate(t, &s, "pki.cas", pkiBody(t, "root", ca, map[string]any{"keyRef": keyWrite(PKISecretName("ca_x"), ca.KeyPEM)}), "8")
	if err != nil {
		t.Fatalf("create CA: %s", faultText(err))
	}
	if !strings.HasPrefix(caID, "ca_") || p.State.Secrets.Scopes["core"][PKISecretName("ca_x")].Kind != "pki" {
		t.Fatalf("CA %s secrets %+v", caID, p.State.Secrets.Scopes["core"])
	}
	if bytes.Contains(p.ConfigBody, []byte("PRIVATE KEY")) {
		t.Fatal("private key in the declaration")
	}
	snap, err := publicSnapshot(p.State)
	if err != nil || bytes.Contains(snap.Value, []byte("PRIVATE KEY")) || !bytes.Contains(snap.Value, []byte(`"keyRef":{"secret":"pki_ca_x","isSet":true`)) {
		t.Fatalf("snapshot %s %v", snap.Value, err)
	}
	s = p.State

	issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
	leaf, err := pki.NewLeaf(pki.LeafRequest{CommonName: "fw.example.com", Usage: "server", KeyAlgorithm: "rsa-4096", ValidDays: 30}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := pki.NewLeaf(pki.LeafRequest{CommonName: "other.example.com", Usage: "server", KeyAlgorithm: "rsa-4096", ValidDays: 30}, issuer)
	// A refused save leaves a secret it set in the state it was given, as a store transaction never would.
	attempt := 0
	certExtra := func(key []byte) map[string]any {
		attempt++
		name := "pki_web"
		if attempt < 3 {
			name = "pki_try" + string(rune('0'+attempt))
		}
		return map[string]any{"caId": caID, "sans": leaf.Info.SANs, "keyAlgorithm": "rsa-4096", "keyRef": keyWrite(name, key)}
	}
	if _, _, err := pkiCreate(t, &s, "pki.certificates", pkiBody(t, "web", leaf, certExtra(other.KeyPEM)), "9"); !strings.Contains(faultText(err), "does not match") {
		t.Fatalf("mismatched key: %s", faultText(err))
	}
	wrongSerial := certExtra(leaf.KeyPEM)
	body := pkiBody(t, "web", leaf, wrongSerial)
	body = bytes.Replace(body, []byte(leaf.Info.Serial), []byte("abc"), 1)
	if _, _, err := pkiCreate(t, &s, "pki.certificates", body, "9"); !strings.Contains(faultText(err), "/certPem") {
		t.Fatalf("wrong serial: %s", faultText(err))
	}
	// An RSA-4096 key is over the old 4096-byte secret limit.
	if len(leaf.KeyPEM) <= 3000 {
		t.Fatalf("RSA-4096 key is %d bytes", len(leaf.KeyPEM))
	}
	crtID, p, err := pkiCreate(t, &s, "pki.certificates", pkiBody(t, "web", leaf, certExtra(leaf.KeyPEM)), "9")
	if err != nil {
		t.Fatalf("create certificate: %s", faultText(err))
	}
	s = p.State

	// Bound to management, the certificate cannot be deleted; neither can its CA.
	mgmt, _ := json.Marshal(map[string]any{"segment": s.Config.Segments[0].ID, "certificate": crtID})
	version := Version("0")
	c, _ := ObjectChange("replace", "management", "", &version, mgmt)
	s = mustSave(t, s, c, "10")
	if s.Config.Management.Certificate != crtID {
		t.Fatalf("management %+v", s.Config.Management)
	}
	del := func(collection, id string) error {
		v := s.Versions[objectKey{collection, id}].Version
		c, _ := ObjectChange("delete", collection, id, &v, nil)
		_, err := prepareChanges(&s, c, "11", fixedTime)
		return err
	}
	if err := del("pki.certificates", crtID); !strings.Contains(faultText(err), "remains referenced") {
		t.Fatalf("delete bound certificate: %s", faultText(err))
	}
	if err := del("pki.cas", caID); !strings.Contains(faultText(err), "remains referenced") {
		t.Fatalf("delete CA of a certificate: %s", faultText(err))
	}

	// Unbound, the certificate deletes with its key.
	mv := s.Versions[listKey("management")].Version
	mgmt, _ = json.Marshal(map[string]any{"segment": s.Config.Segments[0].ID})
	c, _ = ObjectChange("replace", "management", "", &mv, mgmt)
	s = mustSave(t, s, c, "11")
	v := s.Versions[objectKey{"pki.certificates", crtID}].Version
	c, _ = ObjectChange("delete", "pki.certificates", crtID, &v, nil)
	s = mustSave(t, s, c, "12")
	if _, ok := s.Secrets.Scopes["core"]["pki_web"]; ok || len(s.Config.PKI.Certificates) != 0 {
		t.Fatal("deleted certificate's key kept")
	}
}

// A rename that leaves the key out keeps it, and a client may not invent an unresolved key.
func TestPKIReplaceKeepsKey(t *testing.T) {
	s := seededState(t)
	ca, _ := pki.NewCA(pki.CARequest{CommonName: "Andaru CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 365}, nil)
	caID, p, err := pkiCreate(t, &s, "pki.cas", pkiBody(t, "root", ca, map[string]any{"keyRef": keyWrite("pki_root", ca.KeyPEM)}), "8")
	if err != nil {
		t.Fatal(faultText(err))
	}
	s = p.State
	version := s.Versions[objectKey{"pki.cas", caID}].Version
	c, _ := ObjectChange("replace", "pki.cas", caID, &version, pkiBody(t, "renamed", ca, nil))
	s = mustSave(t, s, c, "9")
	if s.Config.PKI.CAs[0].Name != "renamed" || s.Config.PKI.CAs[0].KeyRef == nil || s.Config.PKI.CAs[0].KeyRef.Secret != "pki_root" {
		t.Fatalf("CA %+v", s.Config.PKI.CAs[0])
	}
	version = s.Versions[objectKey{"pki.cas", caID}].Version
	c, _ = ObjectChange("replace", "pki.cas", caID, &version, pkiBody(t, "renamed", ca, map[string]any{"keyRef": map[string]any{"secret": "pki_other", "unresolved": true}}))
	if _, err := prepareChanges(&s, c, "10", fixedTime); !strings.Contains(faultText(err), "must be set or exist") {
		t.Fatalf("invented unresolved key: %s", faultText(err))
	}
	key, _ := pki.TLSCryptKey()
	if _, _, err := pkiCreate(t, &s, "pki.tlsCryptKeys", []byte(`{"name":"bad","keyRef":{"secret":"pki_bad","set":"not a key","expectedVersion":"0"}}`), "10"); !strings.Contains(faultText(err), "static key") {
		t.Fatalf("bad tls-crypt key: %s", faultText(err))
	}
	raw, _ := json.Marshal(map[string]any{"name": "tc", "keyRef": keyWrite("pki_tc", key)})
	if _, _, err := pkiCreate(t, &s, "pki.tlsCryptKeys", raw, "10"); err != nil {
		t.Fatalf("tls-crypt key: %s", faultText(err))
	}
}

// An unresolved key blocks an apply only where it is served: management's certificate, not a CA.
func TestPKIUnresolvedKeysGateApply(t *testing.T) {
	s := seededState(t)
	d := s.Config
	d.PKI.CAs = []pkiCA{{identity: identity{ID: "ca_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", Name: "root"}, KeyRef: &secretReference{Secret: "pki_root", Unresolved: true}}}
	d.PKI.Certificates = []pkiCertificate{{identity: identity{ID: "crt_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", Name: "web"}, KeyRef: secretReference{Secret: "pki_web", Unresolved: true}}}
	if err := checkResolvedReferences(&d); err != nil {
		t.Fatalf("unbound unresolved keys blocked an apply: %v", faultText(err))
	}
	d.Management = &management{Segment: d.Segments[0].ID, Certificate: d.PKI.Certificates[0].ID}
	if err := checkResolvedReferences(&d); !strings.Contains(faultText(err), "/management/certificate") {
		t.Fatalf("unresolved management key: %s", faultText(err))
	}
	d.Management.Certificate = ""
	d.Addons = []addonConfig{{ID: "org.andaru.sample", Enabled: false, Settings: map[string]any{"tls": d.PKI.Certificates[0].ID}}}
	if err := checkResolvedReferences(&d); err != nil {
		t.Fatalf("disabled add-on blocked an apply: %s", faultText(err))
	}
	d.Addons[0].Enabled = true
	if err := checkResolvedReferences(&d); !strings.Contains(faultText(err), "/addons/0/settings") {
		t.Fatalf("enabled add-on's unresolved certificate: %s", faultText(err))
	}
}

// Saving or renewing a certificate needs no apply, but binding one to management does.
func TestPKINeedsNoApply(t *testing.T) {
	a := []byte(`{"links":[],"pki":{"certificates":[{"id":"crt_1"}]}}`)
	b := []byte(`{"links":[],"pki":{"certificates":[{"id":"crt_2"}]}}`)
	if !SameRealization(RealizationInput{Value: a}, RealizationInput{Value: b}) {
		t.Fatal("a PKI change needs an apply")
	}
	c := []byte(`{"links":[],"management":{"segment":"s","certificate":"crt_1"}}`)
	if SameRealization(RealizationInput{Value: a}, RealizationInput{Value: c}) {
		t.Fatal("binding management's certificate needs no apply")
	}
}

// A batch creates PKI objects like any collection.
func TestPKIBatch(t *testing.T) {
	s := seededState(t)
	key, _ := pki.TLSCryptKey()
	op, _ := json.Marshal(map[string]any{"operations": []any{map[string]any{"op": "create", "collection": "pki.tlsCryptKeys", "localId": "tc",
		"value": map[string]any{"name": "tc", "keyRef": keyWrite("pki_batch", key)}}}})
	c, err := DecodeChanges(op)
	if err != nil {
		t.Fatal(err)
	}
	p, err := prepareChanges(&s, c, "8", fixedTime)
	if err != nil || !strings.HasPrefix(p.Created["tc"], "tck_") || len(p.State.Config.PKI.TLSCryptKeys) != 1 {
		t.Fatalf("batch: %s %+v", faultText(err), p.Created)
	}
}

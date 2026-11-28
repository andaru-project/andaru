package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/pki"
)

func acmeCreate(t *testing.T, s *savedState, collection string, value map[string]any, next Version) (string, savedState, error) {
	t.Helper()
	raw, _ := json.Marshal(value)
	id, p, err := pkiCreate(t, s, collection, raw, next)
	return id, p.State, err
}

func acmeReplace(t *testing.T, s savedState, collection, id string, value map[string]any, next Version) (savedState, error) {
	t.Helper()
	raw, _ := json.Marshal(value)
	v := s.Versions[objectKey{collection, id}].Version
	c, err := ObjectChange("replace", collection, id, &v, raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := prepareChanges(&s, c, next, fixedTime)
	return p.State, err
}

func acmeDelete(t *testing.T, s savedState, collection, id string, next Version) (savedState, error) {
	t.Helper()
	v := s.Versions[objectKey{collection, id}].Version
	c, _ := ObjectChange("delete", collection, id, &v, nil)
	p, err := prepareChanges(&s, c, next, fixedTime)
	return p.State, err
}

func secretSet(name, value string) map[string]any {
	return map[string]any{"secret": name, "set": value, "expectedVersion": "0"}
}

func dnsOrder(accountID string, domains ...string) map[string]any {
	return map[string]any{"name": "edge", "accountId": accountID, "domains": domains, "keyAlgorithm": "ecdsa-p256", "enabled": true,
		"challenge": map[string]any{"type": "dns-01", "dns01": map[string]any{"provider": "cloudflare", "cloudflare": map[string]any{"apiTokenRef": secretSet("acme_cf", "cf-token")}}}}
}

func httpOrder(accountID, segment string, domains ...string) map[string]any {
	return map[string]any{"name": "web", "accountId": accountID, "domains": domains, "keyAlgorithm": "ecdsa-p256", "enabled": true,
		"challenge": map[string]any{"type": "http-01", "http01": map[string]any{"listenSegments": []string{segment}}}}
}

func withAccount(t *testing.T) (string, savedState) {
	t.Helper()
	s := seededState(t)
	id, s, err := acmeCreate(t, &s, "acme.accounts", map[string]any{"name": "le", "directoryUrl": "https://acme-v02.api.letsencrypt.org/directory", "email": "admin@example.com"}, "8")
	if err != nil {
		t.Fatalf("create account: %s", faultText(err))
	}
	return id, s
}

// An account created without a key is given one, kept as an acme secret and never shown; replacing the account
// without its secrets keeps them.
func TestACMEAccountKeyGeneratedAndKept(t *testing.T) {
	id, s := withAccount(t)
	if !strings.HasPrefix(id, "aca_") {
		t.Fatalf("id %s", id)
	}
	a := s.Config.ACME.Accounts[0]
	if a.AccountKeyRef == nil || a.AccountKeyRef.Secret != ACMESecretName(id, "key") {
		t.Fatalf("account %+v", a)
	}
	entry := s.Secrets.Scopes["core"][a.AccountKeyRef.Secret]
	if entry.Kind != "acme" {
		t.Fatalf("kind %q", entry.Kind)
	}
	if _, err := pki.ParseKey([]byte(entry.Value)); err != nil {
		t.Fatalf("generated key: %v", err)
	}
	snap, err := publicSnapshot(s)
	if err != nil || bytes.Contains(snap.Value, []byte("PRIVATE KEY")) || !bytes.Contains(snap.Value, []byte(`"accountKeyRef":{"secret":"`+a.AccountKeyRef.Secret+`","isSet":true`)) {
		t.Fatalf("snapshot %s %v", snap.Value, err)
	}
	s, err = acmeReplace(t, s, "acme.accounts", id, map[string]any{"name": "le-renamed", "directoryUrl": "https://acme.example.com/dir", "email": "ops@example.com",
		"eabKeyId": "kid", "eabHmacRef": secretSet("acme_eab", "aGVsbG8")}, "9")
	if err != nil {
		t.Fatalf("replace with EAB: %s", faultText(err))
	}
	s, err = acmeReplace(t, s, "acme.accounts", id, map[string]any{"name": "le-again", "directoryUrl": "https://acme.example.com/dir", "email": "ops@example.com", "eabKeyId": "kid"}, "10")
	if err != nil {
		t.Fatalf("replace without secrets: %s", faultText(err))
	}
	a = s.Config.ACME.Accounts[0]
	if a.AccountKeyRef.Secret != ACMESecretName(id, "key") || a.EABHMACRef == nil || a.EABHMACRef.Secret != "acme_eab" || s.Secrets.Scopes["core"]["acme_eab"].Value != "aGVsbG8" {
		t.Fatalf("account after replace %+v", a)
	}
	// Clearing EAB drops eabHmacRef and prunes the orphaned EAB secret from secrets.db.
	s, err = acmeReplace(t, s, "acme.accounts", id, map[string]any{"name": "le-no-eab", "directoryUrl": "https://acme.example.com/dir", "email": "ops@example.com"}, "11")
	if err != nil {
		t.Fatalf("clear EAB: %s", faultText(err))
	}
	a = s.Config.ACME.Accounts[0]
	if a.EABKeyID != "" || a.EABHMACRef != nil {
		t.Fatalf("EAB not cleared: %+v", a)
	}
	if _, exists := s.Secrets.Scopes["core"]["acme_eab"]; exists {
		t.Fatal("orphaned EAB secret was not pruned")
	}
	// A key ID without its HMAC key, or a plain-http directory, is refused.
	if _, _, err := acmeCreate(t, &s, "acme.accounts", map[string]any{"name": "x", "directoryUrl": "https://ca.example/dir", "email": "a@b.c", "eabKeyId": "kid"}, "11"); !strings.Contains(faultText(err), "eabKeyId") {
		t.Fatalf("EAB without HMAC: %s", faultText(err))
	}
	if _, _, err := acmeCreate(t, &s, "acme.accounts", map[string]any{"name": "x", "directoryUrl": "http://ca.example/dir", "email": "a@b.c"}, "11"); err == nil {
		t.Fatal("http directory accepted")
	}
}

// An order's DNS provider secrets are kept when a replace leaves them out; a wildcard needs DNS-01.
func TestACMEOrderSecretsAndWildcards(t *testing.T) {
	accountID, s := withAccount(t)
	orderID, s, err := acmeCreate(t, &s, "acme.orders", dnsOrder(accountID, "*.example.com", "example.com"), "9")
	if err != nil {
		t.Fatalf("create order: %s", faultText(err))
	}
	o := s.Config.ACME.Orders[0]
	if !strings.HasPrefix(orderID, "aco_") || o.RenewBeforeDays != 30 || o.Challenge.DNS01.PropagationTimeoutSeconds != 120 || s.Secrets.Scopes["core"]["acme_cf"].Kind != "acme" {
		t.Fatalf("order %+v", o)
	}
	snap, _ := publicSnapshot(s)
	if bytes.Contains(snap.Value, []byte("cf-token")) || !bytes.Contains(snap.Value, []byte(`"apiTokenRef":{"secret":"acme_cf","isSet":true`)) {
		t.Fatalf("snapshot %s", snap.Value)
	}
	replaced := dnsOrder(accountID, "*.example.com")
	replaced["challenge"].(map[string]any)["dns01"].(map[string]any)["cloudflare"] = map[string]any{}
	replaced["renewBeforeDays"] = 20
	if s, err = acmeReplace(t, s, "acme.orders", orderID, replaced, "10"); err != nil {
		t.Fatalf("replace without token: %s", faultText(err))
	}
	if o := s.Config.ACME.Orders[0]; o.Challenge.DNS01.Cloudflare.APITokenRef.Secret != "acme_cf" || o.RenewBeforeDays != 20 || s.Secrets.Scopes["core"]["acme_cf"].Value != "cf-token" {
		t.Fatalf("order after replace %+v", o)
	}
	// A new provider needs its own secret.
	hz := dnsOrder(accountID, "example.com")
	hz["challenge"] = map[string]any{"type": "dns-01", "dns01": map[string]any{"provider": "hetzner", "hetzner": map[string]any{}}}
	if _, err := acmeReplace(t, s, "acme.orders", orderID, hz, "11"); !strings.Contains(faultText(err), "required") {
		t.Fatalf("provider without secret: %s", faultText(err))
	}
	// Switching DNS provider prunes the old provider's orphaned secret.
	hzOK := dnsOrder(accountID, "example.com")
	hzOK["challenge"] = map[string]any{"type": "dns-01", "dns01": map[string]any{"provider": "hetzner", "hetzner": map[string]any{"apiTokenRef": secretSet("acme_hz_ok", "hz-token")}}}
	if s2, err := acmeReplace(t, s, "acme.orders", orderID, hzOK, "11"); err != nil {
		t.Fatalf("switch provider: %s", faultText(err))
	} else if _, exists := s2.Secrets.Scopes["core"]["acme_cf"]; exists {
		t.Fatal("old provider secret was not pruned on switch")
	}
	// A wildcard cannot be proved over HTTP-01.
	if _, _, err := acmeCreate(t, &s, "acme.orders", httpOrder(accountID, s.Config.Segments[0].ID, "*.example.com"), "11"); !strings.Contains(faultText(err), "wildcard") {
		t.Fatalf("wildcard over http-01: %s", faultText(err))
	}
	// Nor may an order name a provider block other than its own.
	mixed := dnsOrder(accountID, "example.com")
	mixed["name"] = "mixed"
	mixed["challenge"].(map[string]any)["dns01"].(map[string]any)["cloudflare"] = map[string]any{"apiTokenRef": map[string]any{"secret": "acme_cf"}}
	mixed["challenge"].(map[string]any)["dns01"].(map[string]any)["hetzner"] = map[string]any{"apiTokenRef": secretSet("acme_hz", "x")}
	if _, _, err := acmeCreate(t, &s, "acme.orders", mixed, "11"); !strings.Contains(faultText(err), "no other") {
		t.Fatalf("two providers: %s", faultText(err))
	}
	// A client may not invent an unresolved secret.
	invented := dnsOrder(accountID, "example.com")
	invented["challenge"].(map[string]any)["dns01"].(map[string]any)["cloudflare"] = map[string]any{"apiTokenRef": map[string]any{"secret": "acme_new", "unresolved": true}}
	if _, err := acmeReplace(t, s, "acme.orders", orderID, invented, "11"); !strings.Contains(faultText(err), "must be set or exist") {
		t.Fatalf("invented unresolved: %s", faultText(err))
	}
}

// What an order names cannot be deleted from under it, and no two orders, enabled or not, renew one certificate.
func TestACMEReferences(t *testing.T) {
	accountID, s := withAccount(t)
	ca, _ := pki.NewCA(pki.CARequest{CommonName: "CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, nil)
	issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
	leaf, _ := pki.NewLeaf(pki.LeafRequest{CommonName: "edge.example.com", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, issuer)
	crtID, p, err := pkiCreate(t, &s, "pki.certificates", pkiBody(t, "edge", leaf, map[string]any{"sans": leaf.Info.SANs, "keyAlgorithm": "ecdsa-p256", "keyRef": keyWrite("pki_edge", leaf.KeyPEM)}), "9")
	if err != nil {
		t.Fatal(faultText(err))
	}
	s = p.State
	segment := s.Config.Segments[0].ID
	first := httpOrder(accountID, segment, "edge.example.com")
	first["certificateId"], first["enabled"] = crtID, false
	orderID, s, err := acmeCreate(t, &s, "acme.orders", first, "10")
	if err != nil {
		t.Fatalf("create order: %s", faultText(err))
	}
	second := dnsOrder(accountID, "edge.example.com")
	second["certificateId"] = crtID
	if _, _, err := acmeCreate(t, &s, "acme.orders", second, "11"); !strings.Contains(faultText(err), "already renewed") {
		t.Fatalf("duplicate certificateId: %s", faultText(err))
	}
	for _, c := range []struct{ collection, id string }{{"acme.accounts", accountID}, {"pki.certificates", crtID}, {"segments", segment}} {
		if _, err := acmeDelete(t, s, c.collection, c.id, "11"); !strings.Contains(faultText(err), "remains referenced") {
			t.Fatalf("delete %s: %s", c.collection, faultText(err))
		}
	}
	// Deleted, the order and then its account go, the account's generated key with it.
	keyName := s.Config.ACME.Accounts[0].AccountKeyRef.Secret
	if s, err = acmeDelete(t, s, "acme.orders", orderID, "11"); err != nil {
		t.Fatal(faultText(err))
	}
	if s, err = acmeDelete(t, s, "acme.accounts", accountID, "12"); err != nil {
		t.Fatal(faultText(err))
	}
	if _, ok := s.Secrets.Scopes["core"][keyName]; ok {
		t.Fatal("deleted account's key kept")
	}
}

// Only an enabled HTTP-01 order's segments need an apply.
func TestACMESameRealization(t *testing.T) {
	order := func(typ string, enabled bool, segs string, cert string) RealizationInput {
		ch := `{"type":"dns-01","dns01":{"provider":"cloudflare"}}`
		if typ == "http-01" {
			ch = `{"type":"http-01","http01":{"listenSegments":[` + segs + `]}}`
		}
		v := `{"links":[],"acme":{"accounts":[{"id":"aca_1","name":"a"}],"orders":[{"id":"aco_1","enabled":` + map[bool]string{true: "true", false: "false"}[enabled] +
			`,"certificateId":"` + cert + `","challenge":` + ch + `}]}}`
		return RealizationInput{Value: json.RawMessage(v)}
	}
	none := RealizationInput{Value: json.RawMessage(`{"links":[]}`)}
	if !SameRealization(none, order("dns-01", true, "", "")) || !SameRealization(order("dns-01", true, "", ""), order("dns-01", false, "", "crt_1")) {
		t.Fatal("a DNS-01 order needs an apply")
	}
	if !SameRealization(none, order("http-01", false, `"seg_1"`, "")) {
		t.Fatal("a disabled HTTP-01 order needs an apply")
	}
	if SameRealization(none, order("http-01", true, `"seg_1"`, "")) {
		t.Fatal("enabling an HTTP-01 order needs no apply")
	}
	if SameRealization(order("http-01", true, `"seg_1"`, ""), order("http-01", true, `"seg_2"`, "")) {
		t.Fatal("moving an HTTP-01 order's segments needs no apply")
	}
	if !SameRealization(order("http-01", true, `"seg_1"`, ""), order("http-01", true, `"seg_1"`, "crt_1")) {
		t.Fatal("binding the issued certificate needs an apply")
	}
}

// Unresolved ACME secrets, as a restore without secrets leaves them, never block an apply.
func TestACMEUnresolvedSecretsDoNotGateApply(t *testing.T) {
	d := seededState(t).Config
	d.ACME.Accounts = []acmeAccount{{identity: identity{ID: "aca_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", Name: "le"}, AccountKeyRef: &secretReference{Secret: "acme_key", Unresolved: true}}}
	d.ACME.Orders = []acmeOrder{{identity: identity{ID: "aco_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", Name: "o"}, Enabled: true, AccountID: d.ACME.Accounts[0].ID,
		Challenge: acmeChallenge{Type: "dns-01", DNS01: &acmeDNS01{Provider: "cloudflare", Cloudflare: &acmeAPIToken{APITokenRef: &secretReference{Secret: "acme_cf", Unresolved: true}}}}}}
	if err := checkResolvedReferences(&d); err != nil {
		t.Fatalf("unresolved ACME secrets blocked an apply: %s", faultText(err))
	}
	held := 0
	for _, h := range heldSecrets(&d) {
		if strings.HasPrefix(h.path, "/acme/") {
			held++
		}
	}
	if held != 2 {
		t.Fatalf("held ACME secrets: %d", held)
	}
}

// A kept recovery copy holds no ACME section.
func TestKeptDeclarationDropsACME(t *testing.T) {
	body := []byte(`{"declaration":{"schemaVersion":4,"links":[],"segments":[],"zones":[],"roles":[],"acme":{"accounts":[{"id":"aca_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f","name":"le","directoryUrl":"https://ca.example/dir","email":"a@b.c","accountKeyRef":{"secret":"acme_key","isSet":true,"updatedAt":"2026-11-28T00:00:00Z","version":"3"}}]}},"secrets":{}}`)
	docs, err := KeptDeclaration(body)
	if err != nil {
		t.Fatal(faultText(err))
	}
	if strings.Contains(string(docs.Configuration), "acme") {
		t.Fatalf("kept copy holds acme: %s", docs.Configuration)
	}
	if err = ValidateBackupDocuments(docs); err != nil {
		t.Fatal(err)
	}
}

// The first issuance creates the certificate and binds the order to it; a renewal replaces it and its key in place.
func TestACMECertificateChanges(t *testing.T) {
	accountID, s := withAccount(t)
	orderID, s, err := acmeCreate(t, &s, "acme.orders", dnsOrder(accountID, "edge.example.com"), "9")
	if err != nil {
		t.Fatal(faultText(err))
	}
	issue := func() pki.Issued {
		ca, _ := pki.NewCA(pki.CARequest{CommonName: "LE", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, nil)
		issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
		leaf, err := pki.NewLeaf(pki.LeafRequest{CommonName: "edge.example.com", SANs: []string{"edge.example.com"}, Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, issuer)
		if err != nil {
			t.Fatal(err)
		}
		return leaf
	}
	certID, _ := NewID("pki.certificates")
	first := issue()
	c, err := acmeCertificateChanges(&s, orderID, certID, string(first.CertPEM), "", string(first.KeyPEM))
	if err != nil {
		t.Fatal(faultText(err))
	}
	s = mustSave(t, s, c, "10")
	if s.Config.ACME.Orders[0].CertificateID != certID || len(s.Config.PKI.Certificates) != 1 || s.Config.PKI.Certificates[0].Name != "edge" ||
		s.Secrets.Scopes["core"][PKISecretName(certID)].Value != string(first.KeyPEM) {
		t.Fatalf("first issuance: %+v %+v", s.Config.ACME.Orders[0], s.Config.PKI.Certificates)
	}
	second := issue()
	if c, err = acmeCertificateChanges(&s, orderID, certID, string(second.CertPEM), "", string(second.KeyPEM)); err != nil {
		t.Fatal(faultText(err))
	}
	s = mustSave(t, s, c, "11")
	if s.Config.PKI.Certificates[0].Serial != second.Info.Serial || s.Secrets.Scopes["core"][PKISecretName(certID)].Value != string(second.KeyPEM) {
		t.Fatal("renewal not saved in place")
	}
	other, _ := NewID("pki.certificates")
	if _, err := acmeCertificateChanges(&s, orderID, other, string(second.CertPEM), "", string(second.KeyPEM)); err == nil {
		t.Fatal("renewed into another certificate")
	}
}

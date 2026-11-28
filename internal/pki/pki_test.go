package pki

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"
)

func mustCA(t *testing.T, algorithm string, parent *Issuer) (Issued, Issuer) {
	t.Helper()
	ca, err := NewCA(CARequest{CommonName: "Andaru Test CA", Organization: "Andaru", KeyAlgorithm: algorithm, ValidDays: 365}, parent)
	if err != nil {
		t.Fatalf("NewCA(%s): %v", algorithm, err)
	}
	issuer, err := ParseIssuer(ca.CertPEM, ca.KeyPEM)
	if err != nil {
		t.Fatalf("ParseIssuer: %v", err)
	}
	return ca, issuer
}

func TestCAAndLeafEveryAlgorithm(t *testing.T) {
	for _, algorithm := range Algorithms {
		t.Run(algorithm, func(t *testing.T) {
			ca, root := mustCA(t, algorithm, nil)
			if !ca.Info.IsCA || ca.Info.KeyAlgorithm != algorithm || ca.Info.Subject != "CN=Andaru Test CA,O=Andaru" {
				t.Fatalf("CA info = %+v", ca.Info)
			}
			leaf, err := NewLeaf(LeafRequest{CommonName: "fw.example.com", SANs: []string{"fw.example.com", "192.0.2.1", "2001:db8::1"}, Usage: "server", KeyAlgorithm: algorithm, ValidDays: 30}, root)
			if err != nil {
				t.Fatalf("NewLeaf: %v", err)
			}
			info, cert, err := Inspect(leaf.CertPEM)
			if err != nil {
				t.Fatal(err)
			}
			if info.IsCA || info.KeyAlgorithm != algorithm || strings.Join(info.SANs, ",") != "fw.example.com,192.0.2.1,2001:db8::1" {
				t.Fatalf("leaf info = %+v", info)
			}
			if len(info.FingerprintSHA256) != 64 || info.FingerprintSHA256 != strings.ToLower(info.FingerprintSHA256) || info.Serial == "" {
				t.Fatalf("fingerprint/serial = %q/%q", info.FingerprintSHA256, info.Serial)
			}
			if _, err := time.Parse(time.RFC3339, info.NotAfter); err != nil || !strings.HasSuffix(info.NotAfter, "Z") {
				t.Fatalf("notAfter %q is not RFC 3339 UTC", info.NotAfter)
			}
			pool := x509.NewCertPool()
			pool.AddCert(root.Cert)
			if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "fw.example.com"}); err != nil {
				t.Fatalf("leaf does not verify: %v", err)
			}
			key, err := ParseKey(leaf.KeyPEM)
			if err != nil || !Matches(cert, key) {
				t.Fatalf("leaf key: %v", err)
			}
		})
	}
}

func TestIntermediateAndUsage(t *testing.T) {
	_, root := mustCA(t, "ecdsa-p256", nil)
	inter, issuer := mustCA(t, "ecdsa-p384", &root)
	if err := issuer.Cert.CheckSignatureFrom(root.Cert); err != nil || !issuer.Cert.MaxPathLenZero {
		t.Fatalf("intermediate not signed by root as a path-length-zero CA: %v", err)
	}
	client, err := NewLeaf(LeafRequest{CommonName: "alice", Usage: "client", KeyAlgorithm: "ecdsa-p256", ValidDays: 3650}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	_, cert, _ := Inspect(client.CertPEM)
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(cert.DNSNames) != 0 {
		t.Fatalf("client EKU/SANs = %v/%v", cert.ExtKeyUsage, cert.DNSNames)
	}
	if cert.NotAfter.After(issuer.Cert.NotAfter) {
		t.Fatal("leaf outlives its CA")
	}
	// The leaf imports with the intermediate as its chain.
	imported, err := Import(client.CertPEM, inter.CertPEM, client.KeyPEM)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if string(imported.ChainPEM) != string(inter.CertPEM) {
		t.Fatal("chain not kept")
	}
}

func TestLeafRejections(t *testing.T) {
	_, root := mustCA(t, "ecdsa-p256", nil)
	for name, req := range map[string]LeafRequest{
		"bad usage":    {CommonName: "x", Usage: "signing", KeyAlgorithm: "ecdsa-p256", ValidDays: 1},
		"bad days":     {CommonName: "x", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 3651},
		"bad SAN":      {CommonName: "x", SANs: []string{"not a name"}, Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 1},
		"bad key":      {CommonName: "x", Usage: "server", KeyAlgorithm: "ed25519", ValidDays: 1},
		"control char": {CommonName: "x\n", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 1},
	} {
		if _, err := NewLeaf(req, root); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewCA(CARequest{CommonName: "x", KeyAlgorithm: "ecdsa-p256", ValidDays: 36501}, nil); err == nil {
		t.Error("CA validDays above 36500 accepted")
	}
}

func TestImportRejections(t *testing.T) {
	_, root := mustCA(t, "ecdsa-p256", nil)
	other, _ := mustCA(t, "ecdsa-p256", nil)
	leaf, err := NewLeaf(LeafRequest{CommonName: "a.example", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 10}, root)
	if err != nil {
		t.Fatal(err)
	}
	stranger, _ := NewLeaf(LeafRequest{CommonName: "b.example", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 10}, root)
	if _, err := Import(leaf.CertPEM, nil, stranger.KeyPEM); err == nil {
		t.Error("mismatched key accepted")
	}
	if _, err := Import(leaf.CertPEM, other.CertPEM, leaf.KeyPEM); err == nil {
		t.Error("chain that did not sign the leaf accepted")
	}
	if _, err := Import(append([]byte("junk\n"), leaf.CertPEM...), nil, leaf.KeyPEM); err == nil {
		t.Error("text before the PEM accepted")
	}
	if _, err := Import(leaf.CertPEM, nil, leaf.CertPEM); err == nil {
		t.Error("certificate as key accepted")
	}
	// A full chain pasted as the certificate splits into leaf and chain.
	imported, err := Import(leaf.CertPEM, nil, leaf.KeyPEM)
	if err != nil || imported.ChainPEM != nil {
		t.Fatalf("plain import = %v, chain %q", err, imported.ChainPEM)
	}
	full := append(append([]byte{}, leaf.CertPEM...), Encode(root.Cert)...)
	if imported, err = Import(full, nil, leaf.KeyPEM); err != nil || string(imported.ChainPEM) != string(Encode(root.Cert)) {
		t.Fatalf("full chain import = %v", err)
	}
	expired := now
	now = func() time.Time { return time.Now().AddDate(0, 0, 20) }
	defer func() { now = expired }()
	if _, err := Import(leaf.CertPEM, nil, leaf.KeyPEM); err == nil {
		t.Error("expired certificate accepted")
	}
}

func TestTLSCryptKey(t *testing.T) {
	key, err := TLSCryptKey()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(key), "\n"), "\n")
	if len(lines) != 18 || lines[0] != "-----BEGIN OpenVPN Static key V1-----" || lines[17] != "-----END OpenVPN Static key V1-----" {
		t.Fatalf("key = %q", key)
	}
	for _, line := range lines[1:17] {
		if len(line) != 32 || strings.ToLower(line) != line {
			t.Fatalf("line %q", line)
		}
	}
	if !ValidTLSCryptKey(key) {
		t.Fatal("generated key not valid")
	}
	if ValidTLSCryptKey([]byte(strings.Replace(string(key), lines[3], "zz"+lines[3][2:], 1))) {
		t.Fatal("non-hex line accepted")
	}
}

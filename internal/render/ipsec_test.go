package render

import (
	"strconv"
	"strings"
	"testing"
)

// Secrets are only in Real: swanctl.conf and keys are redacted in the preview, and no value reaches any other file.
func TestIPsecSecretsRedacted(t *testing.T) {
	in := fixture(t, "ipsec")
	result := render(t, in)
	for _, f := range result.Files {
		for _, name := range []string{"IPSEC_SITE_A_PSK", "IPSEC_ALICE", "IPSEC_BOB"} {
			if strings.Contains(f.Content, in.Secrets[name]) {
				t.Fatalf("%s shows %s:\n%s", f.Path, name, f.Content)
			}
		}
		if strings.Contains(f.Content, "PRIVATE KEY") {
			t.Fatalf("%s shows a private key", f.Path)
		}
	}
	conf := fileEnding(t, result, "/run/andaru-ipsec/swanctl.conf")
	if !conf.Secret || conf.Mode != "0600" || strings.Count(conf.Content, "secret = "+redacted) != 3 || strings.Contains(conf.Real(), redacted) {
		t.Fatalf("swanctl.conf: %+v", conf)
	}
	// A secret that holds quotes and braces is base64, so it cannot end its value.
	if !strings.Contains(conf.Real(), "secret = 0sc2l0ZS1hICJzZWNyZXQiIH0=") {
		t.Fatalf("encoded secret:\n%s", conf.Real())
	}
	key := fileEnding(t, result, "/run/andaru-ipsec/private/crt_00000001-0000-4000-8000-000000000001.key")
	if !key.Secret || key.Mode != "0400" || key.Content != redacted+"\n" || !strings.Contains(key.Real(), "PRIVATE KEY") {
		t.Fatalf("key: %+v", key)
	}
	if hasFile(result, "50-andaru-ipsec2.netdev") || strings.Contains(conf.Real(), "ips_00000004") || strings.Contains(golden(result), "10.41.0.0/16") {
		t.Fatal("a disabled connection or route rendered")
	}
	// Only certificate authentication: nothing in swanctl.conf is secret.
	certOnly := render(t, change(t, fixture(t, "ipsec"), func(d map[string]any) {
		vpn := d["vpn"].(map[string]any)
		vpn["ipsec"] = []any{vpn["ipsec"].([]any)[1]}
	}))
	if f := fileEnding(t, certOnly, "/run/andaru-ipsec/swanctl.conf"); f.Secret || strings.Contains(f.Content, "secrets {") {
		t.Fatalf("cert-only swanctl.conf: %+v", f)
	}
}

func TestIPsecFindings(t *testing.T) {
	missing := fixture(t, "ipsec")
	delete(missing.Secrets, "IPSEC_BOB")
	missing.CertificateKeys = nil
	r := render(t, missing)
	if r.Realizable || !hasFinding(r, "error", "SECRET_UNAVAILABLE", "/vpn/ipsec/2/auth/eapUsers/1/passwordRef") || !hasFinding(r, "error", "SECRET_UNAVAILABLE", "/vpn/ipsec/1/auth/localCertId") {
		t.Fatalf("missing secrets: %+v", r.Findings)
	}
	taken := fixture(t, "ipsec")
	taken.Links = append(taken.Links, links(observed{Name: "ipsec0", Kind: "dummy"})...)
	r = render(t, taken)
	if r.Realizable || !hasFinding(r, "error", "NAME_COLLISION", "/vpn/ipsec/0/ifname") || hasFile(r, "50-andaru-ipsec0.netdev") {
		t.Fatalf("name taken: %+v", r.Findings)
	}
	if strings.Contains(fileEnding(t, r, nftPath).Content, `"ipsec0"`) || strings.Contains(fileEnding(t, r, "/run/andaru-ipsec/swanctl.conf").Content, "if_id_in = 42") {
		t.Fatal("a connection whose interface is not rendered is realized")
	}
	own := fixture(t, "ipsec")
	own.Links = append(own.Links, links(observed{Name: "ipsec0", Kind: "xfrm"})...)
	if r := render(t, own); !r.Realizable {
		t.Fatalf("own interface: %+v", r.Findings)
	}
	unfiltered := change(t, fixture(t, "ipsec"), func(d map[string]any) { delete(d, "management"); d["nat"] = map[string]any{}; d["rules"] = []any{} })
	if r := render(t, unfiltered); r.Realizable || !hasFinding(r, "error", "MANAGEMENT_REQUIRED", "/management") {
		t.Fatalf("without the filter: %+v", r.Findings)
	}
	// None enabled: no swanctl.conf, so the realizer stops strongSwan.
	none := render(t, change(t, fixture(t, "ipsec"), func(d map[string]any) {
		for _, c := range d["vpn"].(map[string]any)["ipsec"].([]any) {
			c.(map[string]any)["enabled"] = false
		}
		d["routes"] = []any{}
	}))
	if hasFile(none, "swanctl.conf") || strings.Contains(fileEnding(t, none, nftPath).Content, "4500") {
		t.Fatal("disabled connections rendered")
	}
}

// Policy-based connections get deterministic non-shifting reqids shared by swanctl.conf and the packet filter, and
// interface-based zone rules exclude policy-based IPsec packets via meta mark isolation.
func TestIPsecReqids(t *testing.T) {
	const id1 = "ips_00000002-0000-4000-8000-000000000002"
	const id2 = "ips_00000005-0000-4000-8000-000000000005"
	req1 := ipsecReqid(id1, map[int]bool{})
	req2 := ipsecReqid(id2, map[int]bool{})

	r := render(t, change(t, fixture(t, "ipsec"), func(d map[string]any) {
		vpn := d["vpn"].(map[string]any)
		list := vpn["ipsec"].([]any)
		second := map[string]any{}
		for k, v := range list[1].(map[string]any) {
			second[k] = v
		}
		second["id"], second["name"], second["remoteSubnets"] = id2, "branch-2", []any{"10.31.0.0/16"}
		vpn["ipsec"] = append(list, second)
	}))
	conf := fileEnding(t, r, "/run/andaru-ipsec/swanctl.conf").Content
	script := fileEnding(t, r, nftPath).Content
	want1 := "reqid = " + strconv.Itoa(req1)
	want2 := "reqid = " + strconv.Itoa(req2)
	if !strings.Contains(conf, want1) || !strings.Contains(conf, want2) ||
		!strings.Contains(script, "ipsec out reqid "+strconv.Itoa(req2)) ||
		!strings.Contains(script, "meta mark set meta mark | "+ipsecPolicyInMark) ||
		!strings.Contains(script, "meta mark set meta mark | "+ipsecPolicyOutMark) ||
		!strings.Contains(script, "meta mark & "+ipsecPolicyOutMark+" == 0 oifname @") {
		t.Fatalf("reqids / zone isolation:\n%s\n%s", conf, script)
	}
	// Removing connection 1 must NOT shift connection 2's reqid.
	onlySecond := render(t, change(t, fixture(t, "ipsec"), func(d map[string]any) {
		vpn := d["vpn"].(map[string]any)
		list := vpn["ipsec"].([]any)
		second := map[string]any{}
		for k, v := range list[1].(map[string]any) {
			second[k] = v
		}
		second["id"], second["name"], second["remoteSubnets"] = id2, "branch-2", []any{"10.31.0.0/16"}
		vpn["ipsec"] = []any{second}
		d["routes"] = []any{}
	}))
	if !strings.Contains(fileEnding(t, onlySecond, "/run/andaru-ipsec/swanctl.conf").Content, want2) {
		t.Fatalf("reqid shifted when earlier connection was removed")
	}
}

// Intermediate chain certificates are written as individual PEM files under x509ca/ because strongSwan reads only the
// first PEM block per file in x509/.
func TestIPsecCertificateChainSplit(t *testing.T) {
	const leaf = "-----BEGIN CERTIFICATE-----\nTEVBRg==\n-----END CERTIFICATE-----\n"
	const inter1 = "-----BEGIN CERTIFICATE-----\nSU5URVIx\n-----END CERTIFICATE-----\n"
	const inter2 = "-----BEGIN CERTIFICATE-----\nSU5URVIy\n-----END CERTIFICATE-----\n"
	r := render(t, change(t, fixture(t, "ipsec"), func(d map[string]any) {
		pki := d["pki"].(map[string]any)
		crt := pki["certificates"].([]any)[0].(map[string]any)
		crt["certPem"] = leaf
		crt["chainPem"] = inter1 + inter2
	}))
	const cid = "crt_00000001-0000-4000-8000-000000000001"
	if got := fileEnding(t, r, "/run/andaru-ipsec/x509/"+cid+".pem").Content; got != leaf {
		t.Fatalf("leaf x509 file contains chain: %q", got)
	}
	if got := fileEnding(t, r, "/run/andaru-ipsec/x509ca/"+cid+"-chain-0.pem").Content; got != inter1 {
		t.Fatalf("chain-0: %q", got)
	}
	if got := fileEnding(t, r, "/run/andaru-ipsec/x509ca/"+cid+"-chain-1.pem").Content; got != inter2 {
		t.Fatalf("chain-1: %q", got)
	}
}

// Port forwards use policy-aware ingress matching when policy-based IPsec connections exist: WAN forwards exclude
// decrypted policy-based packets, and forwards naming a policy-based zone match its inbound reqid.
func TestIPsecPolicyPortForwardIsolation(t *testing.T) {
	req := strconv.Itoa(ipsecReqid("ips_00000002-0000-4000-8000-000000000002", map[int]bool{}))
	r := render(t, change(t, fixture(t, "ipsec"), func(d map[string]any) {
		nat := d["nat"].(map[string]any)
		nat["portForwards"] = []any{
			map[string]any{
				"id":           "pfw_00000001-0000-4000-8000-000000000001",
				"name":         "wan-https",
				"enabled":      true,
				"ingressZones": []any{"zon_00000001-0000-4000-8000-000000000001"},
				"protocol":     "tcp",
				"from":         []any{},
				"to":           map[string]any{"self": true},
				"ports":        "443",
				"target":       map[string]any{"address": "192.0.2.10"},
				"filterRule":   "auto",
			},
			map[string]any{
				"id":           "pfw_00000002-0000-4000-8000-000000000002",
				"name":         "branch-http",
				"enabled":      true,
				"ingressZones": []any{"zon_00000004-0000-4000-8000-000000000004"},
				"protocol":     "tcp",
				"from":         []any{},
				"to":           map[string]any{"self": true},
				"ports":        "8080",
				"target":       map[string]any{"address": "192.0.2.20"},
				"filterRule":   "auto",
			},
		}
	}))
	script := fileEnding(t, r, nftPath).Content
	for _, want := range []string{
		"meta ipsec exists ipsec in reqid " + req + " meta mark set meta mark | " + ipsecPolicyInMark,
		"meta mark & " + ipsecPolicyInMark + " == 0 iifname @zon_00000001_0000_4000_8000_000000000001",
		"meta ipsec exists ipsec in reqid " + req + " meta nfproto ipv4 fib daddr . iif type local tcp dport 8080",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in:\n%s", want, script)
		}
	}
}

func TestIPsecPolicyOneToOne(t *testing.T) {
	req := strconv.Itoa(ipsecReqid("ips_00000002-0000-4000-8000-000000000002", map[int]bool{}))
	r := render(t, change(t, fixture(t, "ipsec"), func(d map[string]any) {
		d["nat"].(map[string]any)["oneToOne"] = []any{map[string]any{
			"id": "oto_00000001-0000-4000-8000-000000000001", "name": "branch", "enabled": true,
			"zone": "zon_00000004-0000-4000-8000-000000000004", "internal": "192.0.2.40", "external": "198.51.100.40", "destination": []any{},
		}}
	}))
	script := fileEnding(t, r, nftPath).Content
	// Validation refuses this pairing; the rendered rule still never translates policy-based tunnel traffic.
	if !strings.Contains(script, "meta mark & "+ipsecPolicyInMark+" == 0 iifname @zon_00000004_0000_4000_8000_000000000004 dnat") ||
		strings.Contains(script, "meta ipsec exists ipsec in reqid "+req+" dnat") {
		t.Fatalf("1:1 not limited to interface traffic:\n%s", script)
	}
}

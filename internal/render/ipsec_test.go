package render

import (
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

// Policy-based connections get reqids 100 and up in order, which swanctl.conf and the filter share.
func TestIPsecReqids(t *testing.T) {
	r := render(t, change(t, fixture(t, "ipsec"), func(d map[string]any) {
		vpn := d["vpn"].(map[string]any)
		list := vpn["ipsec"].([]any)
		second := map[string]any{}
		for k, v := range list[1].(map[string]any) {
			second[k] = v
		}
		second["id"], second["name"], second["remoteSubnets"] = "ips_00000005-0000-4000-8000-000000000005", "branch-2", []any{"10.31.0.0/16"}
		vpn["ipsec"] = append(list, second)
	}))
	conf := fileEnding(t, r, "/run/andaru-ipsec/swanctl.conf").Content
	script := fileEnding(t, r, nftPath).Content
	if !strings.Contains(conf, "reqid = 100") || !strings.Contains(conf, "reqid = 101") || !strings.Contains(script, "ipsec out reqid 101") {
		t.Fatalf("reqids:\n%s\n%s", conf, script)
	}
}

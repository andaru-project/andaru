package config

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/pki"
)

const (
	ipsA   = "ips_00000001-0000-4000-8000-000000000001"
	ipsB   = "ips_00000002-0000-4000-8000-000000000002"
	ipsCA  = "ca_00000001-0000-4000-8000-000000000001"
	ipsCrt = "crt_00000001-0000-4000-8000-000000000001"
)

func ipsecFixture(t *testing.T, conns ...map[string]any) map[string]any {
	t.Helper()
	d := fixtureMap(t)
	list := []any{}
	for _, c := range conns {
		list = append(list, c)
	}
	d["vpn"] = map[string]any{"ipsec": list}
	return d
}

// ipsecPKI adds a CA and a certificate it signed, as stored, so connections can name them.
func ipsecPKI(t *testing.T, d map[string]any) map[string]any {
	t.Helper()
	ca, err := pki.NewCA(pki.CARequest{CommonName: "IPsec CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 365}, nil)
	if err != nil {
		t.Fatal(err)
	}
	issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
	leaf, err := pki.NewLeaf(pki.LeafRequest{CommonName: "gw.example.com", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	stored := func(id, name string, issued pki.Issued, extra map[string]any) map[string]any {
		var v map[string]any
		if err := json.Unmarshal(pkiBody(t, name, issued, extra), &v); err != nil {
			t.Fatal(err)
		}
		v["id"] = id
		return v
	}
	d["pki"] = map[string]any{
		"cas":          []any{stored(ipsCA, "root", ca, nil)},
		"certificates": []any{stored(ipsCrt, "gw", leaf, map[string]any{"caId": ipsCA, "sans": leaf.Info.SANs, "keyAlgorithm": "ecdsa-p256", "keyRef": map[string]any{"secret": "pki_gw"}})},
	}
	return d
}

func ipsecConn(fields map[string]any) map[string]any {
	c := map[string]any{"id": ipsA, "name": "site-a", "enabled": true, "mode": "route-based", "ifname": "ipsec0", "xfrmIfId": 42, "zone": lanZone,
		"tunnelAddresses": []any{"169.254.42.1/30"}, "remoteAddress": "198.51.100.7", "ikeVersion": 2,
		"auth": map[string]any{"localType": "psk", "remoteType": "psk", "localId": "gw.example.com", "remoteId": "site-a.example.com", "pskRef": map[string]any{"secret": "IPSEC_A"}}}
	for k, v := range fields {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func policyConn(fields map[string]any) map[string]any {
	return ipsecConn(merge(map[string]any{"mode": "policy-based", "ifname": nil, "xfrmIfId": nil, "tunnelAddresses": nil,
		"localSubnets": []any{"192.0.2.0/24"}, "remoteSubnets": []any{"10.30.0.0/16"}}, fields))
}

func warriorConn(fields map[string]any) map[string]any {
	return ipsecConn(merge(map[string]any{"mode": "road-warrior", "ifname": "ipsec1", "xfrmIfId": 43, "remoteAddress": "%any", "tunnelAddresses": nil,
		"virtualPool": []any{"10.99.0.0/24"},
		"auth": map[string]any{"localType": "psk", "remoteType": "eap-mschapv2", "localId": "gw.example.com", "pskRef": map[string]any{"secret": "IPSEC_A"},
			"eapUsers": []any{map[string]any{"username": "alice", "passwordRef": map[string]any{"secret": "IPSEC_ALICE"}}}}}, fields))
}

func authWith(fields map[string]any) map[string]any {
	a := map[string]any{"localType": "psk", "remoteType": "psk", "localId": "gw.example.com", "pskRef": map[string]any{"secret": "IPSEC_A"}}
	for k, v := range fields {
		if v == nil {
			delete(a, k)
		} else {
			a[k] = v
		}
	}
	return a
}

func TestIPsecCanonicalForm(t *testing.T) {
	got := accepted(t, ipsecFixture(t, ipsecConn(map[string]any{"remoteAddress": "2001:DB8::7", "localSubnets": []any{"10.0.0.9/24"}}),
		warriorConn(map[string]any{"id": ipsB, "name": "road", "virtualPool": []any{"10.99.0.7/24", "FD00:99::1/112"}})))
	c, w := got.VPN.IPsec[0], got.VPN.IPsec[1]
	if *c.DPDDelaySeconds != 30 || c.StartAction != "trap" || !*c.NATTraversal || c.RemoteAddress != "2001:db8::7" || c.LocalSubnets[0] != "10.0.0.0/24" {
		t.Fatalf("defaults: %+v", c)
	}
	if w.StartAction != "none" || w.RemoteAddress != "" || w.VirtualPool[0] != "10.99.0.0/24" || w.VirtualPool[1] != "fd00:99::/112" {
		t.Fatalf("road warrior: %+v", w)
	}
	for _, empty := range []map[string]any{{"ipsec": []any{}}, {"wireguard": []any{}, "ipsec": []any{}}} {
		d := fixtureMap(t)
		d["vpn"] = empty
		if raw, err := canonical(accepted(t, d)); err != nil || strings.Contains(string(raw), `"vpn"`) {
			t.Fatalf("an empty vpn section is dropped: %s", raw)
		}
	}
	// A disabled connection may leave out its zone; zero DPD is kept.
	if got := accepted(t, ipsecFixture(t, ipsecConn(map[string]any{"enabled": false, "zone": nil, "dpdDelaySeconds": 0}))); *got.VPN.IPsec[0].DPDDelaySeconds != 0 {
		t.Fatalf("dpd 0: %+v", got.VPN.IPsec[0])
	}
	accepted(t, ipsecFixture(t, policyConn(nil)))
	accepted(t, ipsecFixture(t, warriorConn(map[string]any{"remoteAddress": nil})))
	accepted(t, ipsecPKI(t, ipsecFixture(t, ipsecConn(map[string]any{"auth": authWith(map[string]any{"localType": "cert", "remoteType": "cert", "pskRef": nil,
		"localCertId": ipsCrt, "caId": ipsCA, "localId": "CN=gw.example.com,O=Example"})}))))
}

func TestIPsecValidation(t *testing.T) {
	const p = "/vpn/ipsec/0"
	for name, c := range map[string]struct {
		conn map[string]any
		path string
	}{
		"mode":                     {ipsecConn(map[string]any{"mode": "transport"}), p + "/mode"},
		"ikev1":                    {ipsecConn(map[string]any{"ikeVersion": 1}), p + "/ikeVersion"},
		"ifname":                   {ipsecConn(map[string]any{"ifname": "ipsec100"}), p + "/ifname"},
		"ifname wg":                {ipsecConn(map[string]any{"ifname": "wg0"}), p + "/ifname"},
		"no ifname":                {ipsecConn(map[string]any{"ifname": nil}), p + "/ifname"},
		"no xfrm id":               {ipsecConn(map[string]any{"xfrmIfId": nil}), p + "/xfrmIfId"},
		"xfrm id range":            {ipsecConn(map[string]any{"xfrmIfId": 65536}), p + "/xfrmIfId"},
		"policy ifname":            {policyConn(map[string]any{"ifname": "ipsec0"}), p + "/ifname"},
		"policy xfrm":              {policyConn(map[string]any{"xfrmIfId": 5}), p + "/xfrmIfId"},
		"policy tunnel":            {policyConn(map[string]any{"tunnelAddresses": []any{"10.0.0.1/30"}}), p + "/tunnelAddresses"},
		"policy local subnets":     {policyConn(map[string]any{"localSubnets": nil}), p + "/localSubnets"},
		"policy remote subnets":    {policyConn(map[string]any{"remoteSubnets": nil}), p + "/remoteSubnets"},
		"no zone":                  {ipsecConn(map[string]any{"zone": nil}), p + "/zone"},
		"unknown zone":             {ipsecConn(map[string]any{"zone": "zon_00000000-0000-4000-8000-0000000000ff"}), p + "/zone"},
		"unknown segment":          {ipsecConn(map[string]any{"localSegment": "seg_00000000-0000-4000-8000-0000000000ff"}), p + "/localSegment"},
		"local address":            {ipsecConn(map[string]any{"localAddress": "224.0.0.1"}), p + "/localAddress"},
		"no remote":                {ipsecConn(map[string]any{"remoteAddress": nil}), p + "/remoteAddress"},
		"any remote":               {ipsecConn(map[string]any{"remoteAddress": "%any"}), p + "/remoteAddress"},
		"remote injection":         {ipsecConn(map[string]any{"remoteAddress": "a.example.com\n}"}), p + "/remoteAddress"},
		"remote brace":             {ipsecConn(map[string]any{"remoteAddress": "a{b"}), p + "/remoteAddress"},
		"remote port":              {ipsecConn(map[string]any{"remoteAddress": "a.example.com:500"}), p + "/remoteAddress"},
		"proposal":                 {ipsecConn(map[string]any{"proposals": []any{"aes256 }"}}), p + "/proposals/0"},
		"esp proposal":             {ipsecConn(map[string]any{"espProposals": []any{"AES256"}}), p + "/espProposals/0"},
		"tunnel address":           {ipsecConn(map[string]any{"tunnelAddresses": []any{"10.0.0.1"}}), p + "/tunnelAddresses/0"},
		"tunnel multicast":         {ipsecConn(map[string]any{"tunnelAddresses": []any{"224.0.0.1/24"}}), p + "/tunnelAddresses/0"},
		"subnet":                   {ipsecConn(map[string]any{"localSubnets": []any{"10.0.0.0"}}), p + "/localSubnets/0"},
		"subnet dup masked":        {policyConn(map[string]any{"localSubnets": []any{"192.0.2.1/24", "192.0.2.9/24"}}), p + "/localSubnets/1"},
		"remote dup masked":        {policyConn(map[string]any{"enabled": false, "remoteSubnets": []any{"10.30.0.0/16", "10.30.1.0/16"}}), p + "/remoteSubnets/1"},
		"pool dup masked disabled": {warriorConn(map[string]any{"enabled": false, "virtualPool": []any{"10.99.0.1/24", "10.99.0.2/24"}}), p + "/virtualPool/1"},
		"tunnel dup":               {ipsecConn(map[string]any{"enabled": false, "tunnelAddresses": []any{"10.0.0.1/30", "10.0.0.1/30"}}), p + "/tunnelAddresses"},
		"no pool":                  {warriorConn(map[string]any{"virtualPool": nil}), p + "/virtualPool"},
		"pool elsewhere":           {ipsecConn(map[string]any{"virtualPool": []any{"10.99.0.0/24"}}), p + "/virtualPool"},
		"warrior remote subnets":   {warriorConn(map[string]any{"remoteSubnets": []any{"10.0.0.0/8"}}), p + "/remoteSubnets"},
		"warrior trap":             {warriorConn(map[string]any{"startAction": "trap"}), p + "/startAction"},
		"dpd":                      {ipsecConn(map[string]any{"dpdDelaySeconds": 3601}), p + "/dpdDelaySeconds"},
		"start action":             {ipsecConn(map[string]any{"startAction": "always"}), p + "/startAction"},
		"no psk":                   {ipsecConn(map[string]any{"auth": authWith(map[string]any{"pskRef": nil})}), p + "/auth/pskRef"},
		"stray psk":                {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localType": "cert", "remoteType": "cert", "localCertId": ipsCrt, "caId": ipsCA})}), p + "/auth/pskRef"},
		"local id space":           {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localId": "gw example"})}), p + "/auth/localId"},
		"local id quote":           {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localId": `gw"`})}), p + "/auth/localId"},
		"local id hash":            {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localId": "gw#1"})}), p + "/auth/localId"},
		"local id newline":         {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localId": "gw\n}"})}), p + "/auth/localId"},
		"remote id brace":          {ipsecConn(map[string]any{"auth": authWith(map[string]any{"remoteId": "x}"})}), p + "/auth/remoteId"},
		"remote id backslash":      {ipsecConn(map[string]any{"auth": authWith(map[string]any{"remoteId": `x\`})}), p + "/auth/remoteId"},
		"no local id":              {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localId": nil})}), p + "/auth"},
		"no cert":                  {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localType": "cert"})}), p + "/auth/localCertId"},
		"unknown cert":             {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localType": "cert", "localCertId": "crt_00000000-0000-4000-8000-0000000000ff"})}), p + "/auth/localCertId"},
		"stray cert":               {ipsecConn(map[string]any{"auth": authWith(map[string]any{"localCertId": ipsCrt})}), p + "/auth/localCertId"},
		"no ca":                    {ipsecConn(map[string]any{"auth": authWith(map[string]any{"remoteType": "cert"})}), p + "/auth/caId"},
		"unknown ca":               {ipsecConn(map[string]any{"auth": authWith(map[string]any{"remoteType": "cert", "caId": "ca_00000000-0000-4000-8000-0000000000ff"})}), p + "/auth/caId"},
		"stray ca":                 {ipsecConn(map[string]any{"auth": authWith(map[string]any{"caId": ipsCA})}), p + "/auth/caId"},
		"no eap users":             {ipsecConn(map[string]any{"auth": authWith(map[string]any{"remoteType": "eap-mschapv2"})}), p + "/auth/eapUsers"},
		"stray eap users":          {ipsecConn(map[string]any{"auth": authWith(map[string]any{"eapUsers": []any{map[string]any{"username": "a", "passwordRef": map[string]any{"secret": "P"}}}})}), p + "/auth/eapUsers"},
		"eap user injection":       {warriorConn(map[string]any{"auth": authWith(map[string]any{"remoteType": "eap-mschapv2", "eapUsers": []any{map[string]any{"username": "a\"}", "passwordRef": map[string]any{"secret": "P"}}}})}), p + "/auth/eapUsers/0/username"},
		"eap user dup":             {warriorConn(map[string]any{"auth": authWith(map[string]any{"remoteType": "eap-mschapv2", "eapUsers": []any{map[string]any{"username": "a", "passwordRef": map[string]any{"secret": "P"}}, map[string]any{"username": "a", "passwordRef": map[string]any{"secret": "Q"}}}})}), p + "/auth/eapUsers/1/username"},
		"unknown field":            {ipsecConn(map[string]any{"leftfirewall": true}), p},
		"unknown auth field":       {ipsecConn(map[string]any{"auth": authWith(map[string]any{"leftauth": "psk"})}), p + "/auth"},
	} {
		t.Run(name, func(t *testing.T) { refusedAt(t, ipsecPKI(t, ipsecFixture(t, c.conn)), c.path) })
	}
	second := ipsecConn(map[string]any{"id": ipsB, "name": "site-b", "ifname": "ipsec1", "xfrmIfId": 43})
	refusedWith(t, ipsecFixture(t, ipsecConn(nil), merge(second, map[string]any{"ifname": "ipsec0"})), "/vpn/ipsec/1/ifname", "Another interface")
	refusedWith(t, ipsecFixture(t, ipsecConn(nil), merge(second, map[string]any{"enabled": false, "xfrmIfId": 42})), "/vpn/ipsec/1/xfrmIfId", "Another connection")
	refusedWith(t, ipsecFixture(t, ipsecConn(nil), merge(second, map[string]any{"name": "site-a"})), "/vpn/ipsec/1/name", "Name already exists")
	accepted(t, ipsecFixture(t, ipsecConn(nil), second))
	// Routes go through a route-based or road-warrior connection, never through a policy-based one.
	d := ipsecFixture(t, ipsecConn(nil), policyConn(map[string]any{"id": ipsB, "name": "branch"}))
	d["routes"] = []any{routeTo(map[string]any{"target": map[string]any{"kind": "ipsec", "id": ipsA}, "gateway": nil})}
	accepted(t, d)
	d["routes"] = []any{routeTo(map[string]any{"target": map[string]any{"kind": "ipsec", "id": ipsB}, "gateway": nil})}
	refusedAt(t, d, "/routes/0/target/id")
	d["routes"] = []any{routeTo(map[string]any{"target": map[string]any{"kind": "ipsec", "id": ipsA}})}
	refusedAt(t, d, "/routes/0/gateway")
}

func ipsecCreate(local, extra string) string {
	return `{"op":"create","collection":"vpn.ipsec","localId":"` + local + `","value":{"name":"site-a","enabled":true,"mode":"route-based","ifname":"ipsec0","xfrmIfId":42,"zone":"` + lanZone + `","remoteAddress":"198.51.100.7","ikeVersion":2` + extra + `}}`
}

func ipsecReplace(s savedState, id, value string) string {
	return `{"operations":[{"op":"replace","collection":"vpn.ipsec","id":"` + id + `","expectedVersion":"` + string(s.Versions[objectKey{"vpn.ipsec", id}].Version) + `","value":` + value + `}]}`
}

func TestIPsecSecrets(t *testing.T) {
	s := seededState(t)
	auth := `,"auth":{"localType":"psk","remoteType":"psk","localId":"gw","pskRef":{"secret":"IPSEC_A","set":"s3cret psk","expectedVersion":"0"}}`
	p := mustRun(t, &s, `{"operations":[`+ipsecCreate("a", auth)+`]}`)
	id := p.Created["a"]
	if !strings.HasPrefix(id, "ips_") || s.Secrets.Scopes["core"]["IPSEC_A"].Kind != "ipsec" || s.Secrets.Scopes["core"]["IPSEC_A"].Value != "s3cret psk" {
		t.Fatalf("created %q: %+v", id, s.Secrets.Scopes["core"])
	}
	if bytes.Contains(p.ConfigBody, []byte("s3cret")) {
		t.Fatal("the pre-shared key is in the declaration")
	}
	view, err := viewObject(&s.Config.VPN.IPsec[0], "1", s.Secrets)
	if err != nil || bytes.Contains(view.Value, []byte("s3cret")) || !bytes.Contains(view.Value, []byte(`"pskRef":{"secret":"IPSEC_A","isSet":true`)) {
		t.Fatalf("view %s %v", view.Value, err)
	}
	snap, err := publicSnapshot(s)
	if err != nil || !bytes.Contains(snap.Value, []byte(`"ipsec":[`)) || bytes.Contains(snap.Value, []byte("s3cret")) {
		t.Fatalf("snapshot %s %v", snap.Value, err)
	}
	base := `{"name":"site-a","enabled":true,"mode":"road-warrior","ifname":"ipsec0","xfrmIfId":42,"zone":"` + lanZone + `","ikeVersion":2,"virtualPool":["10.99.0.0/24"]`
	eap := func(users string) string {
		return base + `,"auth":{"localType":"psk","remoteType":"eap-mschapv2","localId":"gw","pskRef":{},"eapUsers":[` + users + `]}}`
	}
	// {} keeps the pre-shared key; EAP passwords are set, then kept by username whatever their order.
	mustRun(t, &s, ipsecReplace(s, id, eap(`{"username":"alice","passwordRef":{"secret":"IPSEC_ALICE","set":"a-pw","expectedVersion":"0"}},{"username":"bob","passwordRef":{"secret":"IPSEC_BOB","set":"b-pw","expectedVersion":"0"}}`)))
	c := s.Config.VPN.IPsec[0]
	if c.Auth.PSKRef == nil || c.Auth.PSKRef.Secret != "IPSEC_A" || s.Secrets.Scopes["core"]["IPSEC_BOB"].Kind != "ipsec" {
		t.Fatalf("eap set: %+v", c.Auth)
	}
	mustRun(t, &s, ipsecReplace(s, id, eap(`{"username":"bob","passwordRef":{}},{"username":"alice","passwordRef":{}}`)))
	if c = s.Config.VPN.IPsec[0]; c.Auth.EAPUsers[0].PasswordRef.Secret != "IPSEC_BOB" || c.Auth.EAPUsers[1].PasswordRef.Secret != "IPSEC_ALICE" {
		t.Fatalf("eap kept by username: %+v", c.Auth.EAPUsers)
	}
	// A user dropped takes its password with it.
	mustRun(t, &s, ipsecReplace(s, id, eap(`{"username":"alice","passwordRef":{}}`)))
	if _, ok := s.Secrets.Scopes["core"]["IPSEC_BOB"]; ok {
		t.Fatal("a dropped user's password stays in secrets.db")
	}
	for name, c := range map[string]struct{ value, path string }{
		"new user keeps nothing": {eap(`{"username":"carol","passwordRef":{}}`), "/auth/eapUsers/0/passwordRef"},
		"empty psk":              {base + `,"auth":{"localType":"psk","remoteType":"eap-mschapv2","localId":"gw","pskRef":{"secret":"IPSEC_A","set":"","expectedVersion":"` + string(s.Secrets.Scopes["core"]["IPSEC_A"].Version) + `"},"eapUsers":[{"username":"alice","passwordRef":{}}]}}`, "/auth/pskRef"},
		"invented unresolved":    {base + `,"auth":{"localType":"psk","remoteType":"eap-mschapv2","localId":"gw","pskRef":{"secret":"OTHER","unresolved":true},"eapUsers":[{"username":"alice","passwordRef":{}}]}}`, "/auth/pskRef"},
		"other kind":             {base + `,"auth":{"localType":"psk","remoteType":"eap-mschapv2","localId":"gw","pskRef":{"secret":"WAN_PASSWORD"},"eapUsers":[{"username":"alice","passwordRef":{}}]}}`, "/auth/pskRef"},
	} {
		t.Run(name, func(t *testing.T) {
			c2 := s
			c2.Secrets = cloneSecrets(s.Secrets)
			_, e := run(t, &c2, ipsecReplace(s, id, c.value))
			f := requireFault(t, e, "VALIDATION_FAILED")
			if len(f.Details) == 0 || f.Details[0].Path != "/operations/0/value"+c.path {
				t.Fatalf("refusal %+v", f.Details)
			}
		})
	}
	// Left out, the pre-shared key is cleared with its secret; a {} with none stored is refused.
	cert := base + `,"auth":{"localType":"psk","remoteType":"eap-mschapv2","localId":"gw","eapUsers":[{"username":"alice","passwordRef":{}}]}}`
	_, e := run(t, &s, ipsecReplace(s, id, cert))
	requireFault(t, e, "VALIDATION_FAILED") // psk local auth needs its key
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"vpn.ipsec","id":"`+id+`","expectedVersion":"`+string(s.Versions[objectKey{"vpn.ipsec", id}].Version)+`"}]}`)
	if _, ok := s.Secrets.Scopes["core"]["IPSEC_A"]; ok || s.Config.VPN.IPsec != nil {
		t.Fatal("a deleted connection leaves its secrets")
	}
	if _, ok := s.Secrets.Scopes["core"]["IPSEC_ALICE"]; ok {
		t.Fatal("a deleted connection leaves its EAP passwords")
	}
	_, e = run(t, &s, `{"operations":[`+ipsecCreate("b", `,"auth":{"localType":"psk","remoteType":"psk","localId":"gw","pskRef":{}}`)+`]}`)
	if f := requireFault(t, e, "VALIDATION_FAILED"); f.Details[0].Path != "/operations/0/value/auth/pskRef" {
		t.Fatalf("create keeping nothing: %+v", f.Details)
	}
}

func TestIPsecReferences(t *testing.T) {
	s := seededState(t)
	ca, err := pki.NewCA(pki.CARequest{CommonName: "IPsec CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 365}, nil)
	if err != nil {
		t.Fatal(err)
	}
	caID, p, err := pkiCreate(t, &s, "pki.cas", pkiBody(t, "root", ca, nil), "8")
	if err != nil {
		t.Fatal(faultText(err))
	}
	s = p.State
	peerCA, err := pki.NewCA(pki.CARequest{CommonName: "IPsec Peer CA", KeyAlgorithm: "ecdsa-p256", ValidDays: 365}, nil)
	if err != nil {
		t.Fatal(err)
	}
	peerCAID, p, err := pkiCreate(t, &s, "pki.cas", pkiBody(t, "peer-ca", peerCA, nil), "9")
	if err != nil {
		t.Fatal(faultText(err))
	}
	s = p.State
	issuer, _ := pki.ParseIssuer(ca.CertPEM, ca.KeyPEM)
	leaf, _ := pki.NewLeaf(pki.LeafRequest{CommonName: "gw.example.com", Usage: "server", KeyAlgorithm: "ecdsa-p256", ValidDays: 30}, issuer)
	crtID, p, err := pkiCreate(t, &s, "pki.certificates", pkiBody(t, "gw", leaf, map[string]any{"caId": caID, "sans": leaf.Info.SANs, "keyAlgorithm": "ecdsa-p256", "keyRef": keyWrite("pki_gw", leaf.KeyPEM)}), "10")
	if err != nil {
		t.Fatal(faultText(err))
	}
	s = p.State
	// A connection (using a separate peerCAID referenced ONLY by IPsec), and a route through it by its label, in one batch.
	auth := `,"localSegment":"` + wanSeg + `","auth":{"localType":"cert","remoteType":"cert","localId":"CN=gw","localCertId":"` + crtID + `","caId":"` + peerCAID + `"}`
	p = mustRun(t, &s, `{"operations":[`+ipsecCreate("ips", auth)+`,{"op":"create","collection":"routes","value":{"name":"site","destination":"10.40.0.0/16","target":{"kind":"ipsec","id":"@ips"},"enabled":true}}]}`)
	id := p.Created["ips"]
	if s.Config.Routes[0].Target.ID != id {
		t.Fatalf("label not resolved: %+v", s.Config.Routes)
	}
	del := func(collection, id string) string {
		return `{"operations":[{"op":"delete","collection":"` + collection + `","id":"` + id + `","expectedVersion":"` + string(s.Versions[objectKey{collection, id}].Version) + `"}]}`
	}
	check := func(t *testing.T) {
		for name, batch := range map[string]string{
			"zone": del("zones", lanZone), "segment": del("segments", wanSeg), "ca": del("pki.cas", peerCAID),
			"certificate": del("pki.certificates", crtID), "connection": del("vpn.ipsec", id),
		} {
			t.Run(name, func(t *testing.T) {
				c := s
				c.Secrets = cloneSecrets(s.Secrets)
				_, e := run(t, &c, batch)
				requireFault(t, e, "VALIDATION_FAILED")
			})
		}
	}
	check(t)
	// Disabled, it is still referenced.
	mustRun(t, &s, ipsecReplace(s, id, `{"name":"site-a","enabled":false,"mode":"route-based","ifname":"ipsec0","xfrmIfId":42,"zone":"`+lanZone+`","remoteAddress":"198.51.100.7","ikeVersion":2`+auth+`}`))
	check(t)
}

func TestIPsecUnresolvedApply(t *testing.T) {
	d := func(enabled, unresPSK, unresEAP, unresKey bool) *declaration {
		c := ipsecConnection{identity: identity{ID: ipsA}, Enabled: enabled, Mode: "road-warrior",
			Auth: ipsecAuth{LocalType: "cert", RemoteType: "eap-mschapv2", LocalCertID: ipsCrt, PSKRef: &secretReference{Secret: "P", Unresolved: unresPSK},
				EAPUsers: []ipsecEAPUser{{Username: "a", PasswordRef: secretReference{Secret: "E", Unresolved: unresEAP}}}}}
		return &declaration{VPN: vpnSettings{IPsec: []ipsecConnection{c}},
			PKI: pkiSettings{Certificates: []pkiCertificate{{identity: identity{ID: ipsCrt, Name: "gw"}, KeyRef: secretReference{Secret: "K", Unresolved: unresKey}}}}}
	}
	for _, c := range []struct {
		name                   string
		enabled, psk, eap, key bool
		blocks                 string
	}{
		{"psk", true, true, false, false, "/vpn/ipsec/0/auth/pskRef"},
		{"eap", true, false, true, false, "/vpn/ipsec/0/auth/eapUsers/0/passwordRef"},
		{"cert key", true, false, false, true, "/vpn/ipsec/0/auth/localCertId"},
		{"disabled", false, true, true, true, ""},
		{"resolved", true, false, false, false, ""},
	} {
		err := checkResolvedReferences(d(c.enabled, c.psk, c.eap, c.key))
		var got string
		if f, ok := err.(*Fault); ok {
			got = f.Details[0].Path
		}
		if got != c.blocks {
			t.Errorf("%s: blocked at %q", c.name, got)
		}
	}
	into, keys := map[string]string{}, map[string]string{}
	addKey := func(id, key string) { keys[id] = key }
	secrets := secretDocument{Scopes: map[string]map[string]secretEntry{"core": {"P": {Value: "p"}, "E": {Value: "e"}, "K": {Value: "k"}}}}
	enabledDecl := d(true, false, false, false)
	realizedIPsecSecrets(enabledDecl, secrets, certificateIndex(enabledDecl), into, addKey)
	if into["P"] != "p" || into["E"] != "e" || keys[ipsCrt] != "k" {
		t.Fatalf("realized %v %v", into, keys)
	}
	into, keys = map[string]string{}, map[string]string{}
	disabledDecl := d(false, false, false, false)
	realizedIPsecSecrets(disabledDecl, secrets, certificateIndex(disabledDecl), into, addKey)
	if len(into)+len(keys) != 0 {
		t.Fatalf("a disabled connection's secrets are realized: %v %v", into, keys)
	}
}

func TestIPsecSameRealization(t *testing.T) {
	in := func(vpn string) RealizationInput {
		return RealizationInput{Value: json.RawMessage(`{"links":[],"vpn":{"ipsec":` + vpn + `}}`)}
	}
	const on = `{"id":"i1","name":"a","enabled":true,"mode":"route-based","ifname":"ipsec0","xfrmIfId":1,"ikeVersion":2,"auth":{"localType":"psk","remoteType":"eap-mschapv2","localId":"gw","pskRef":{"secret":"P","isSet":true,"version":"3","updatedAt":"x"},"eapUsers":[{"username":"u","passwordRef":{"secret":"E","isSet":true,"version":"3"}}]}}`
	const off = `{"id":"i2","name":"b","enabled":false,"mode":"policy-based","ikeVersion":2,"auth":{"localType":"psk","remoteType":"psk","localId":"gw","pskRef":{"secret":"Q","isSet":true,"version":"3"}}}`
	base := in(`[` + on + `,` + off + `]`)
	for _, c := range []struct {
		name  string
		other RealizationInput
		same  bool
	}{
		{"renamed and described", in(`[` + strings.Replace(on, `"name":"a"`, `"name":"z","description":"d"`, 1) + `,` + off + `]`), true},
		{"secret rewritten", in(`[` + strings.Replace(on, `"version":"3","updatedAt":"x"`, `"version":"4","updatedAt":"y"`, 1) + `,` + off + `]`), true},
		{"eap secret rewritten", in(`[` + strings.Replace(on, `"secret":"E","isSet":true,"version":"3"`, `"secret":"E","isSet":true,"version":"5"`, 1) + `,` + off + `]`), true},
		{"disabled edited", in(`[` + on + `,` + strings.Replace(off, `"localId":"gw"`, `"localId":"x"`, 1) + `]`), true},
		{"disabled removed", in(`[` + on + `]`), true},
		{"psk rebound", in(`[` + strings.Replace(on, `"secret":"P"`, `"secret":"R"`, 1) + `,` + off + `]`), false},
		{"eap rebound", in(`[` + strings.Replace(on, `"secret":"E"`, `"secret":"F"`, 1) + `,` + off + `]`), false},
		{"xfrm id", in(`[` + strings.Replace(on, `"xfrmIfId":1`, `"xfrmIfId":2`, 1) + `,` + off + `]`), false},
		{"one enabled", in(`[` + on + `,` + strings.Replace(off, `"enabled":false`, `"enabled":true`, 1) + `]`), false},
	} {
		if got := SameRealization(base, c.other); got != c.same {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestKeptRecoveryKeepsIPsec(t *testing.T) {
	psk := ipsecConn(map[string]any{"auth": authWith(map[string]any{"pskRef": map[string]any{"secret": "IPSEC_A", "isSet": true, "version": "3"}})})
	warrior := warriorConn(map[string]any{"id": ipsB, "name": "road", "auth": authWith(map[string]any{"localId": "rw.example.com", "remoteType": "eap-mschapv2", "pskRef": map[string]any{"secret": "IPSEC_W", "isSet": true, "version": "3"},
		"eapUsers": []any{map[string]any{"username": "alice", "passwordRef": map[string]any{"secret": "IPSEC_ALICE", "isSet": true, "version": "3"}}}})})
	cert := policyConn(map[string]any{"id": "ips_00000003-0000-4000-8000-000000000003", "name": "branch", "auth": authWith(map[string]any{"localType": "cert", "remoteType": "cert", "pskRef": nil, "localCertId": ipsCrt, "caId": ipsCA})})
	d := ipsecPKI(t, ipsecFixture(t, psk, warrior, cert))
	d["routes"] = []any{routeTo(map[string]any{"target": map[string]any{"kind": "ipsec", "id": ipsA}, "gateway": nil}),
		routeTo(map[string]any{"id": rteB, "name": "warrior", "target": map[string]any{"kind": "ipsec", "id": ipsB}, "gateway": nil})}
	raw, err := json.Marshal(map[string]any{"declaration": d, "secrets": map[string]string{"WAN_PASSWORD": "TEST_ONLY_SECRET", "IPSEC_A": "psk", "IPSEC_ALICE": "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := KeptDeclaration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateBackupDocuments(docs); err != nil {
		t.Fatalf("%v: %s", err, docs.Configuration)
	}
	conf := string(docs.Configuration)
	if !strings.Contains(conf, `"pskRef":{"secret":"IPSEC_A"}`) || !strings.Contains(conf, `"pskRef":{"secret":"IPSEC_W","unresolved":true}`) || !strings.Contains(conf, `"passwordRef":{"secret":"IPSEC_ALICE"}`) {
		t.Fatalf("references: %s", conf)
	}
	if strings.Contains(conf, "branch") || strings.Contains(conf, ipsCrt) {
		t.Fatalf("a connection naming PKI the copy lacks is kept: %s", conf)
	}
	if !strings.Contains(string(docs.ConfigurationSecrets), `"IPSEC_A":{"kind":"ipsec"`) || !strings.Contains(string(docs.ConfigurationSecrets), `"IPSEC_ALICE":{"kind":"ipsec"`) {
		t.Fatalf("kinds: %s", docs.ConfigurationSecrets)
	}
}

func TestIPsecRebasedCollisions(t *testing.T) {
	s := seededState(t)
	create := func(id, name, ifname string, xfrm int, next Version) {
		body := `{"name":"` + name + `","enabled":true,"mode":"route-based","ifname":"` + ifname + `","xfrmIfId":` + strconv.Itoa(xfrm) + `,"zone":"` + lanZone + `","remoteAddress":"198.51.100.7","ikeVersion":2,"auth":{"localType":"psk","remoteType":"psk","localId":"gw-` + name + `","pskRef":{"secret":"P_` + name + `","set":"k","expectedVersion":"0"}}}`
		c, _ := ObjectChange("create", "vpn.ipsec", "", nil, []byte(body))
		_ = c.AssignID(0, id)
		p, err := prepareChanges(&s, c, next, fixedTime)
		if err != nil {
			t.Fatal(faultText(err))
		}
		s = p.State
	}
	create(ipsA, "a", "ipsec0", 1, "8")
	create(ipsB, "b", "ipsec1", 2, "9")
	vA := s.Versions[objectKey{"vpn.ipsec", ipsA}].Version
	for field, body := range map[string]string{
		"/ifname":   `{"name":"a","enabled":true,"mode":"route-based","ifname":"ipsec1","xfrmIfId":1,"zone":"` + lanZone + `","remoteAddress":"198.51.100.7","ikeVersion":2,"auth":{"localType":"psk","remoteType":"psk","localId":"gw-a","pskRef":{}}}`,
		"/xfrmIfId": `{"name":"a","enabled":true,"mode":"route-based","ifname":"ipsec0","xfrmIfId":2,"zone":"` + lanZone + `","remoteAddress":"198.51.100.7","ikeVersion":2,"auth":{"localType":"psk","remoteType":"psk","localId":"gw-a","pskRef":{}}}`,
	} {
		c, _ := ObjectChange("replace", "vpn.ipsec", ipsA, &vA, []byte(body))
		_, err := prepareChanges(&s, c, "10", fixedTime)
		if f := requireFault(t, err, "VALIDATION_FAILED"); len(f.Details) != 1 || f.Details[0].Path != field {
			t.Fatalf("%s: %+v", field, f)
		}
	}
}

func TestIPsecSecurityValidation(t *testing.T) {
	base := func() *declaration {
		return &declaration{
			Zones: []zone{{identity: identity{ID: lanZone, Name: "lan"}}},
			VPN: vpnSettings{IPsec: []ipsecConnection{{
				identity:      identity{ID: ipsA, Name: "site-a"},
				Enabled:       true,
				Mode:          "route-based",
				Ifname:        "ipsec0",
				XFRMIfID:      1,
				Zone:          lanZone,
				RemoteAddress: "198.51.100.1",
				IKEVersion:    2,
				Auth: ipsecAuth{
					LocalType:  "psk",
					RemoteType: "psk",
					LocalID:    "gw.example.com",
					RemoteID:   "peer-a.example.com",
					PSKRef:     &secretReference{Secret: "P_A"},
				},
			}}},
		}
	}
	// Weak/deprecated IKE & ESP proposals (including strongSwan's "default") rejected.
	for _, prop := range []string{"default", "3des-md5-modp1024", "aes128-sha1-modp2048", "blowfish128-sha256-ecp256", "null-sha256", "aes256--ecp256", "aes256-sha256-modp768"} {
		d := base()
		d.VPN.IPsec[0].Proposals = []string{prop}
		if errs := validateIPsec(d); len(errs) == 0 || errs[0].Path != "/vpn/ipsec/0/proposals/0" {
			t.Errorf("weak IKE proposal %q accepted: %+v", prop, errs)
		}
		d2 := base()
		d2.VPN.IPsec[0].ESPProposals = []string{prop}
		if errs := validateIPsec(d2); len(errs) == 0 || errs[0].Path != "/vpn/ipsec/0/espProposals/0" {
			t.Errorf("weak ESP proposal %q accepted: %+v", prop, errs)
		}
	}
	// Strong modern proposals accepted.
	dGood := base()
	dGood.VPN.IPsec[0].Proposals = []string{"aes256gcm16-prfsha384-ecp384", "chacha20poly1305-prfsha256-curve25519"}
	dGood.VPN.IPsec[0].ESPProposals = []string{"aes256gcm16-ecp384-esn"}
	if errs := validateIPsec(dGood); len(errs) != 0 {
		t.Fatalf("modern proposals rejected: %+v", errs)
	}
	// Unusable virtualPool prefixes (including fec0::1/9 -> fe80::/9 and 126.0.0.0/7 overlapping 127.0.0.0/8) rejected.
	for _, badPool := range []string{"0.0.0.0/0", "::/0", "0.0.0.1/8", "126.0.0.0/7", "127.0.0.0/8", "224.0.0.0/4", "fe80::/64", "fec0::1/9"} {
		d := base()
		d.VPN.IPsec[0].Mode = "road-warrior"
		d.VPN.IPsec[0].RemoteAddress = "%any"
		d.VPN.IPsec[0].VirtualPool = []string{badPool}
		if errs := validateIPsec(d); len(errs) == 0 || errs[0].Path != "/vpn/ipsec/0/virtualPool/0" {
			t.Errorf("bad virtualPool %q accepted: %+v", badPool, errs)
		}
	}
	// Cross-connection PSK (localId, remoteId) collision with equivalent strongSwan identity spellings rejected.
	dPSK := base()
	dup := dPSK.VPN.IPsec[0]
	dup.ID, dup.Name, dup.Ifname, dup.XFRMIfID = ipsB, "site-b", "ipsec1", 2
	dup.Auth.LocalID = "@GW.example.com"
	dup.Auth.RemoteID = "fqdn:peer-a.example.com"
	dup.Auth.PSKRef = &secretReference{Secret: "P_B"}
	dPSK.VPN.IPsec = append(dPSK.VPN.IPsec, dup)
	if errs := validateIPsec(dPSK); len(errs) == 0 || errs[0].Path != "/vpn/ipsec/1/auth/remoteId" {
		t.Errorf("equivalent PSK identity collision accepted: %+v", errs)
	}
	// Cross-connection EAP username collision rejected.
	dEAP := base()
	dEAP.VPN.IPsec[0].Mode = "road-warrior"
	dEAP.VPN.IPsec[0].RemoteAddress = "%any"
	dEAP.VPN.IPsec[0].VirtualPool = []string{"10.99.0.0/24"}
	dEAP.VPN.IPsec[0].Auth.RemoteType = "eap-mschapv2"
	dEAP.VPN.IPsec[0].Auth.RemoteID = ""
	dEAP.VPN.IPsec[0].Auth.EAPUsers = []ipsecEAPUser{{Username: "alice", PasswordRef: secretReference{Secret: "E1"}}}
	rw2 := dEAP.VPN.IPsec[0]
	rw2.ID, rw2.Name, rw2.Ifname, rw2.XFRMIfID = ipsB, "rw-2", "ipsec1", 2
	rw2.Auth.LocalID = "gw2.example.com"
	rw2.Auth.PSKRef = &secretReference{Secret: "P_B"}
	rw2.Auth.EAPUsers = []ipsecEAPUser{{Username: "alice", PasswordRef: secretReference{Secret: "E2"}}}
	rw2.Enabled = false // usernames stay unique even across a disabled connection
	dEAP.VPN.IPsec = append(dEAP.VPN.IPsec, rw2)
	if errs := validateIPsec(dEAP); len(errs) == 0 || errs[0].Path != "/vpn/ipsec/1/auth/eapUsers/0/username" {
		t.Errorf("cross-connection EAP username collision accepted: %+v", errs)
	}
	// A second enabled EAP connection would admit the first's users, so it is refused.
	dEAP.VPN.IPsec[1].Enabled = true
	dEAP.VPN.IPsec[1].Auth.EAPUsers = []ipsecEAPUser{{Username: "bob", PasswordRef: secretReference{Secret: "E2"}}}
	if errs := validateIPsec(dEAP); len(errs) != 1 || errs[0].Path != "/vpn/ipsec/1/auth/remoteType" {
		t.Errorf("second enabled EAP connection accepted: %+v", errs)
	}
	// DNs that differ only in case or spacing are one strongSwan identity.
	if normalizeSwanIdentity("C=US, CN=gw") != normalizeSwanIdentity("asn1dn:c=US,cn=gw") {
		t.Error("DN identities not normalized like strongSwan")
	}
}

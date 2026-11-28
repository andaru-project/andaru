package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

const (
	wgA     = "wg_00000001-0000-4000-8000-000000000001"
	wgB     = "wg_00000002-0000-4000-8000-000000000002"
	wgpA    = "wgp_00000001-0000-4000-8000-000000000001"
	wgpB    = "wgp_00000002-0000-4000-8000-000000000002"
	lanZone = "zon_00000000-0000-4000-8000-000000000002"
	wanSeg  = "seg_00000000-0000-4000-8000-000000000001"
)

func wgKey(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func wgPublic(t *testing.T, private string) string {
	t.Helper()
	p, err := WireGuardPublicKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wgFixture(t *testing.T, ifaces ...map[string]any) map[string]any {
	t.Helper()
	d := fixtureMap(t)
	list := []any{}
	for _, w := range ifaces {
		list = append(list, w)
	}
	d["vpn"] = map[string]any{"wireguard": list}
	return d
}

func wgPeer(fields map[string]any) map[string]any {
	p := map[string]any{"id": wgpA, "name": "laptop", "enabled": true, "publicKey": wgKey(9), "allowedIps": []any{"10.200.0.2/32"}}
	for k, v := range fields {
		if v == nil {
			delete(p, k)
		} else {
			p[k] = v
		}
	}
	return p
}

func wgIface(fields map[string]any) map[string]any {
	w := map[string]any{"id": wgA, "name": "road", "ifname": "wg0", "enabled": true, "zone": lanZone, "listenPort": 51820,
		"addresses": []any{"10.200.0.1/24"}, "privateKeyRef": map[string]any{"secret": "WG0_KEY"}, "peers": []any{wgPeer(nil)}}
	for k, v := range fields {
		if v == nil {
			delete(w, k)
		} else {
			w[k] = v
		}
	}
	return w
}

func TestWireGuardCanonicalForm(t *testing.T) {
	got := accepted(t, wgFixture(t, wgIface(map[string]any{"addresses": []any{"10.200.0.1/24", "FD00:0::1/64"},
		"peers": []any{wgPeer(map[string]any{"id": wgpB, "name": "b", "publicKey": wgKey(8), "allowedIps": []any{"10.20.0.9/24"}}), wgPeer(nil)}})))
	w := got.VPN.WireGuard[0]
	if w.MTU != 1420 || w.AllowListenPort == nil || !*w.AllowListenPort || w.Addresses[1] != "fd00::1/64" {
		t.Fatalf("defaults and addresses: %+v", w)
	}
	if w.Peers[0].ID != wgpA || w.Peers[1].AllowedIPs[0] != "10.20.0.0/24" || w.Peers[1].RouteAllowedIPs == nil || !*w.Peers[1].RouteAllowedIPs {
		t.Fatalf("peers: %+v", w.Peers)
	}
	if got := accepted(t, wgFixture(t)); got.VPN.WireGuard != nil {
		t.Fatalf("an empty list stays absent: %+v", got.VPN)
	}
	raw, err := canonical(accepted(t, fixtureMap(t)))
	if err != nil || strings.Contains(string(raw), `"vpn"`) {
		t.Fatalf("a declaration without VPNs keeps its form: %s", raw)
	}
	for _, empty := range []map[string]any{{}, {"wireguard": []any{}}} {
		d := fixtureMap(t)
		d["vpn"] = empty
		if raw, err := canonical(accepted(t, d)); err != nil || strings.Contains(string(raw), `"vpn"`) {
			t.Fatalf("an empty vpn section is dropped: %s", raw)
		}
	}
	// A disabled interface may be incomplete.
	accepted(t, wgFixture(t, wgIface(map[string]any{"enabled": false, "zone": nil, "addresses": nil, "peers": nil})))
}

func TestWireGuardValidation(t *testing.T) {
	const p = "/vpn/wireguard/0"
	for name, c := range map[string]struct {
		fields map[string]any
		path   string
	}{
		"ifname":              {map[string]any{"ifname": "wg100"}, p + "/ifname"},
		"ifname leading zero": {map[string]any{"ifname": "wg01"}, p + "/ifname"},
		"ifname other":        {map[string]any{"ifname": "eth0"}, p + "/ifname"},
		"no zone":             {map[string]any{"zone": nil}, p + "/zone"},
		"unknown zone":        {map[string]any{"zone": "zon_00000000-0000-4000-8000-0000000000ff"}, p + "/zone"},
		"firewall zone":       {map[string]any{"zone": "zon_firewall"}, p + "/zone"},
		"port":                {map[string]any{"listenPort": 0}, p + "/listenPort"},
		"port high":           {map[string]any{"listenPort": 65536}, p + "/listenPort"},
		"mtu":                 {map[string]any{"mtu": 1279}, p + "/mtu"},
		"no address":          {map[string]any{"addresses": []any{}}, p + "/addresses"},
		"bad address":         {map[string]any{"addresses": []any{"10.200.0.1"}}, p + "/addresses/0"},
		"multicast address":   {map[string]any{"addresses": []any{"224.0.0.1/24"}}, p + "/addresses/0"},
		"listen segment":      {map[string]any{"allowListenSegments": []any{"seg_00000000-0000-4000-8000-0000000000ff"}}, p + "/allowListenSegments/0"},
		"listen segment dup":  {map[string]any{"allowListenSegments": []any{wanSeg, wanSeg}}, p + "/allowListenSegments/1"},
		"public key":          {map[string]any{"publicKey": "abc"}, p + "/publicKey"},
		"no private key":      {map[string]any{"privateKeyRef": nil}, p},
		"unknown field":       {map[string]any{"table": "main"}, p},
		"peer key":            {map[string]any{"peers": []any{wgPeer(map[string]any{"publicKey": "AAAA"})}}, p + "/peers/0/publicKey"},
		"peer no ips":         {map[string]any{"peers": []any{wgPeer(map[string]any{"allowedIps": []any{}})}}, p + "/peers/0/allowedIps"},
		"peer bad ip":         {map[string]any{"peers": []any{wgPeer(map[string]any{"allowedIps": []any{"10.0.0.1"}})}}, p + "/peers/0/allowedIps/0"},
		"peer endpoint":       {map[string]any{"peers": []any{wgPeer(map[string]any{"endpoint": "vpn.example.com"})}}, p + "/peers/0/endpoint"},
		"peer endpoint port":  {map[string]any{"peers": []any{wgPeer(map[string]any{"endpoint": "vpn.example.com:0"})}}, p + "/peers/0/endpoint"},
		"peer endpoint v6":    {map[string]any{"peers": []any{wgPeer(map[string]any{"endpoint": "fd00::1:51820"})}}, p + "/peers/0/endpoint"},
		"peer endpoint space": {map[string]any{"peers": []any{wgPeer(map[string]any{"endpoint": "a b:1"})}}, p + "/peers/0/endpoint"},
		"peer keepalive":      {map[string]any{"peers": []any{wgPeer(map[string]any{"persistentKeepalive": 65536})}}, p + "/peers/0/persistentKeepalive"},
		"peer id":             {map[string]any{"peers": []any{wgPeer(map[string]any{"id": "wgp_1"})}}, p + "/peers/0/id"},
		"own public key":      {map[string]any{"publicKey": wgKey(9)}, p + "/peers/0/publicKey"},
	} {
		t.Run(name, func(t *testing.T) { refusedAt(t, wgFixture(t, wgIface(c.fields)), c.path) })
	}
	for name, endpoint := range map[string]string{"name": "vpn.example.com:51820", "v4": "192.0.2.1:1", "v6": "[2001:db8::1]:51820"} {
		t.Run("endpoint "+name, func(t *testing.T) {
			accepted(t, wgFixture(t, wgIface(map[string]any{"peers": []any{wgPeer(map[string]any{"endpoint": endpoint})}})))
		})
	}
	two := func(a, b map[string]any) map[string]any {
		return wgIface(map[string]any{"peers": []any{wgPeer(a), wgPeer(b)}})
	}
	other := map[string]any{"id": wgpB, "name": "phone", "publicKey": wgKey(8), "allowedIps": []any{"10.200.0.3/32"}}
	refusedWith(t, wgFixture(t, two(nil, merge(other, map[string]any{"name": "laptop"}))), p+"/peers/1/name", "Another peer")
	refusedWith(t, wgFixture(t, two(nil, merge(other, map[string]any{"publicKey": wgKey(9)}))), p+"/peers/1/publicKey", "Another peer")
	refusedWith(t, wgFixture(t, two(nil, merge(other, map[string]any{"id": wgpA}))), p+"/peers/1/id", "Duplicate peer")
	// The same prefix, written another way, may not go to two enabled peers; a disabled one may hold it.
	refusedWith(t, wgFixture(t, two(nil, merge(other, map[string]any{"allowedIps": []any{"10.200.0.2/32"}}))), p+"/peers/1/allowedIps/0", "Another enabled peer")
	refusedWith(t, wgFixture(t, two(map[string]any{"allowedIps": []any{"10.30.0.0/24"}}, merge(other, map[string]any{"allowedIps": []any{"10.30.0.7/24"}}))), p+"/peers/1/allowedIps/0", "Another enabled peer")
	accepted(t, wgFixture(t, two(nil, merge(other, map[string]any{"enabled": false, "allowedIps": []any{"10.200.0.2/32"}}))))
	second := wgIface(map[string]any{"id": wgB, "name": "site"})
	refusedWith(t, wgFixture(t, wgIface(nil), second), "/vpn/wireguard/1/ifname", "Another interface")
	refusedWith(t, wgFixture(t, wgIface(nil), merge(second, map[string]any{"ifname": "wg1", "name": "road"})), "/vpn/wireguard/1/name", "Name already exists")
	accepted(t, wgFixture(t, wgIface(nil), merge(second, map[string]any{"ifname": "wg1"})))
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func wgCreate(local, key, extra string) string {
	return `{"op":"create","collection":"vpn.wireguard","localId":"` + local + `","value":{"name":"road","ifname":"wg0","enabled":true,"zone":"` + lanZone + `","listenPort":51820,"addresses":["10.200.0.1/24"],"privateKeyRef":{"secret":"WG0_KEY","set":"` + key + `","expectedVersion":"0"}` + extra + `}}`
}

func wgReplace(s savedState, id, value string) string {
	return `{"operations":[{"op":"replace","collection":"vpn.wireguard","id":"` + id + `","expectedVersion":"` + string(s.Versions[objectKey{"vpn.wireguard", id}].Version) + `","value":` + value + `}]}`
}

func TestWireGuardKeysAndSecrets(t *testing.T) {
	s := seededState(t)
	peers := `,"peers":[{"name":"laptop","enabled":true,"publicKey":"` + wgKey(9) + `","allowedIps":["10.200.0.2/32"],"presharedKeyRef":{"secret":"WG0_LAPTOP_PSK","set":"` + wgKey(5) + `","expectedVersion":"0"}}]`
	p := mustRun(t, &s, `{"operations":[`+wgCreate("road", wgKey(1), peers)+`]}`)
	id := p.Created["road"]
	w := s.Config.VPN.WireGuard[0]
	if !strings.HasPrefix(id, "wg_") || w.PublicKey != wgPublic(t, wgKey(1)) || !strings.HasPrefix(w.Peers[0].ID, "wgp_") {
		t.Fatalf("created %q: %+v", id, w)
	}
	core := s.Secrets.Scopes["core"]
	if core["WG0_KEY"].Kind != "wireguard" || core["WG0_LAPTOP_PSK"].Kind != "wireguard" || core["WG0_KEY"].Value != wgKey(1) {
		t.Fatalf("secrets %+v", core)
	}
	view, err := viewObject(&s.Config.VPN.WireGuard[0], "1", s.Secrets)
	if err != nil || bytes.Contains(view.Value, []byte(wgKey(1))) || bytes.Contains(view.Value, []byte(wgKey(5))) || !bytes.Contains(view.Value, []byte(`"presharedKeyRef":{"secret":"WG0_LAPTOP_PSK","isSet":true`)) {
		t.Fatalf("view %s %v", view.Value, err)
	}
	peerID := w.Peers[0].ID
	// A replace that leaves the private key out, or writes {}, keeps it and the public key; a peer's {} keeps its
	// pre-shared key whatever the peers' order; a client's publicKey is ignored.
	base := `{"name":"road","ifname":"wg0","enabled":true,"zone":"` + lanZone + `","listenPort":51820,"addresses":["10.200.0.1/24"],"publicKey":"` + wgKey(3) + `"`
	peer := `{"id":"` + peerID + `","name":"laptop","enabled":true,"publicKey":"` + wgKey(9) + `","allowedIps":["10.200.0.2/32"]}`
	newPeer := `{"name":"phone","enabled":true,"publicKey":"` + wgKey(8) + `","allowedIps":["10.200.0.3/32"]}`
	keep := strings.Replace(peer, `]}`, `],"presharedKeyRef":{}}`, 1)
	mustRun(t, &s, wgReplace(s, id, base+`,"peers":[`+newPeer+`,`+keep+`]}`))
	w = s.Config.VPN.WireGuard[0]
	laptop := w.Peers[slicesIndex(w.Peers, peerID)]
	if w.PublicKey != wgPublic(t, wgKey(1)) || w.PrivateKeyRef.Secret != "WG0_KEY" || laptop.PresharedKeyRef == nil || laptop.PresharedKeyRef.Secret != "WG0_LAPTOP_PSK" || len(w.Peers) != 2 {
		t.Fatalf("kept by peer ID: %+v", w)
	}
	mustRun(t, &s, wgReplace(s, id, base+`,"privateKeyRef":{},"peers":[`+keep+`]}`))
	if w = s.Config.VPN.WireGuard[0]; w.PublicKey != wgPublic(t, wgKey(1)) || w.Peers[0].PresharedKeyRef == nil {
		t.Fatalf("{} keeps: %+v", w)
	}
	// A pre-shared key left out is dropped, and its secret goes with it.
	mustRun(t, &s, wgReplace(s, id, base+`,"peers":[`+peer+`]}`))
	if w = s.Config.VPN.WireGuard[0]; w.Peers[0].PresharedKeyRef != nil {
		t.Fatalf("left out, the key stays: %+v", w.Peers[0])
	}
	if _, ok := s.Secrets.Scopes["core"]["WG0_LAPTOP_PSK"]; ok {
		t.Fatal("the dropped pre-shared key stays in secrets.db")
	}
	// Rotating the private key under a new name derives the new public key and drops the old secret.
	mustRun(t, &s, wgReplace(s, id, base+`,"privateKeyRef":{"secret":"WG0_KEY2","set":"`+wgKey(2)+`","expectedVersion":"0"},"peers":[`+peer+`]}`))
	if w = s.Config.VPN.WireGuard[0]; w.PublicKey != wgPublic(t, wgKey(2)) {
		t.Fatalf("rotated: %+v", w)
	}
	if _, ok := s.Secrets.Scopes["core"]["WG0_KEY"]; ok {
		t.Fatal("the rotated-out key stays in secrets.db")
	}
	// Bad key material is refused where it was written.
	for name, c := range map[string]struct{ value, path string }{
		"not base64": {base + `,"privateKeyRef":{"secret":"WG0_KEY2","set":"not a key","expectedVersion":"` + string(s.Secrets.Scopes["core"]["WG0_KEY2"].Version) + `"},"peers":[` + peer + `]}`, "/privateKeyRef"},
		"zero key":   {base + `,"privateKeyRef":{"secret":"WG0_KEY2","set":"` + wgKey(0) + `","expectedVersion":"` + string(s.Secrets.Scopes["core"]["WG0_KEY2"].Version) + `"},"peers":[` + peer + `]}`, "/privateKeyRef"},
		"short psk":  {base + `,"peers":[` + strings.Replace(peer, `]}`, `],"presharedKeyRef":{"secret":"P","set":"AAAA","expectedVersion":"0"}}`, 1) + `]}`, "/peers/0/presharedKeyRef"},
		"own key":    {base + `,"peers":[` + strings.Replace(peer, wgKey(9), wgPublic(t, wgKey(2)), 1) + `]}`, "/peers/0/publicKey"},
		"other kind": {base + `,"privateKeyRef":{"secret":"WAN_PASSWORD"},"peers":[` + peer + `]}`, "/privateKeyRef"},
	} {
		t.Run(name, func(t *testing.T) {
			c2 := s
			c2.Secrets = cloneSecrets(s.Secrets) // a refused batch may have written the in-memory secrets
			_, e := run(t, &c2, wgReplace(s, id, c.value))
			f := requireFault(t, e, "VALIDATION_FAILED")
			if len(f.Details) == 0 || f.Details[0].Path != "/operations/0/value"+c.path {
				t.Fatalf("refusal %+v", f.Details)
			}
		})
	}
	_, e := run(t, &s, `{"operations":[{"op":"create","collection":"vpn.wireguard","value":{"name":"x","ifname":"wg1","enabled":false,"listenPort":1}}]}`)
	if f := requireFault(t, e, "VALIDATION_FAILED"); f.Details[0].Path != "/operations/0/value/privateKeyRef" {
		t.Fatalf("create without a key: %+v", f.Details)
	}
	mustRun(t, &s, `{"operations":[{"op":"delete","collection":"vpn.wireguard","id":"`+id+`","expectedVersion":"`+string(s.Versions[objectKey{"vpn.wireguard", id}].Version)+`"}]}`)
	if _, ok := s.Secrets.Scopes["core"]["WG0_KEY2"]; ok || s.Config.VPN.WireGuard != nil {
		t.Fatal("a deleted interface leaves its key")
	}
}

func cloneSecrets(doc secretDocument) secretDocument {
	out := secretDocument{SchemaVersion: doc.SchemaVersion, Scopes: map[string]map[string]secretEntry{}}
	for scope, entries := range doc.Scopes {
		out.Scopes[scope] = maps.Clone(entries)
	}
	return out
}

func slicesIndex(peers []wireguardPeer, id string) int {
	for i, p := range peers {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func TestWireGuardReferences(t *testing.T) {
	s := seededState(t)
	// An interface, and a route through it by its label, in one batch.
	p := mustRun(t, &s, `{"operations":[`+wgCreate("wg", wgKey(1), `,"allowListenSegments":["`+wanSeg+`"]`)+`,{"op":"create","collection":"routes","value":{"name":"site","destination":"10.50.0.0/16","target":{"kind":"wireguard","id":"@wg"},"enabled":true}}]}`)
	id := p.Created["wg"]
	if s.Config.Routes[0].Target.ID != id {
		t.Fatalf("label not resolved: %+v", s.Config.Routes)
	}
	for name, batch := range map[string]string{
		"zone":      `{"operations":[{"op":"delete","collection":"zones","id":"` + lanZone + `","expectedVersion":"7"}]}`,
		"segment":   `{"operations":[{"op":"delete","collection":"segments","id":"` + wanSeg + `","expectedVersion":"7"}]}`,
		"interface": `{"operations":[{"op":"delete","collection":"vpn.wireguard","id":"` + id + `","expectedVersion":"` + string(s.Version) + `"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := s
			_, e := run(t, &c, batch)
			requireFault(t, e, "VALIDATION_FAILED")
		})
	}
	// Disabled, it is still referenced.
	mustRun(t, &s, wgReplace(s, id, `{"name":"road","ifname":"wg0","enabled":false,"zone":"`+lanZone+`","listenPort":51820}`))
	_, e := run(t, &s, `{"operations":[{"op":"delete","collection":"zones","id":"`+lanZone+`","expectedVersion":"7"}]}`)
	requireFault(t, e, "VALIDATION_FAILED")
}

func TestWireGuardUnresolvedApply(t *testing.T) {
	d := func(enabled, peerEnabled bool, unresolvedKey, unresolvedPSK bool) *declaration {
		w := wireguardInterface{identity: identity{ID: wgA}, Enabled: enabled, PrivateKeyRef: secretReference{Secret: "K", Unresolved: unresolvedKey},
			Peers: []wireguardPeer{{identity: identity{ID: wgpA}, Enabled: peerEnabled, PresharedKeyRef: &secretReference{Secret: "P", Unresolved: unresolvedPSK}}}}
		return &declaration{VPN: vpnSettings{WireGuard: []wireguardInterface{w}}}
	}
	for _, c := range []struct {
		name                             string
		enabled, peer, unresKey, unresPS bool
		blocks                           string
	}{
		{"enabled key", true, true, true, false, "/vpn/wireguard/0/privateKeyRef"},
		{"enabled peer psk", true, true, false, true, "/vpn/wireguard/0/peers/0/presharedKeyRef"},
		{"disabled peer psk", true, false, false, true, ""},
		{"disabled interface", false, true, true, true, ""},
		{"resolved", true, true, false, false, ""},
	} {
		err := checkResolvedReferences(d(c.enabled, c.peer, c.unresKey, c.unresPS))
		var got string
		if f, ok := err.(*Fault); ok {
			got = f.Details[0].Path
		}
		if got != c.blocks {
			t.Errorf("%s: blocked at %q", c.name, got)
		}
	}
	into := map[string]string{}
	realizedWireGuardSecrets(d(true, false, false, false), secretDocument{Scopes: map[string]map[string]secretEntry{"core": {"K": {Value: "k"}, "P": {Value: "p"}}}}, into)
	if len(into) != 1 || into["K"] != "k" {
		t.Fatalf("a disabled peer's key is realized: %v", into)
	}
}

func TestWireGuardSameRealization(t *testing.T) {
	in := func(vpn string) RealizationInput {
		return RealizationInput{Value: json.RawMessage(`{"links":[],"vpn":{"wireguard":` + vpn + `}}`)}
	}
	const peer = `{"id":"p1","name":"laptop","enabled":true,"publicKey":"k","allowedIps":["10.0.0.2/32"],"presharedKeyRef":{"secret":"P","isSet":true,"version":"3","updatedAt":"x"}}`
	const on = `{"id":"w1","name":"a","ifname":"wg0","enabled":true,"listenPort":1,"privateKeyRef":{"secret":"K","isSet":true,"version":"3"},"peers":[` + peer + `]}`
	const off = `{"id":"w2","name":"b","ifname":"wg1","enabled":false,"listenPort":2,"privateKeyRef":{"secret":"L","isSet":true,"version":"3"}}`
	base := in(`[` + on + `,` + off + `]`)
	for _, c := range []struct {
		name  string
		other RealizationInput
		same  bool
	}{
		{"renamed and described", in(`[` + strings.Replace(on, `"name":"a"`, `"name":"z","description":"d"`, 1) + `,` + off + `]`), true},
		{"peer renamed", in(`[` + strings.Replace(on, `"name":"laptop"`, `"name":"pc"`, 1) + `,` + off + `]`), true},
		{"secret rewritten", in(`[` + strings.Replace(on, `"version":"3","updatedAt":"x"`, `"version":"4","updatedAt":"y"`, 1) + `,` + off + `]`), true},
		{"disabled one edited", in(`[` + on + `,` + strings.Replace(off, `"listenPort":2`, `"listenPort":3`, 1) + `]`), true},
		{"disabled one removed", in(`[` + on + `]`), true},
		{"enabled port", in(`[` + strings.Replace(on, `"listenPort":1`, `"listenPort":9`, 1) + `,` + off + `]`), false},
		{"peer disabled", in(`[` + strings.Replace(on, `"enabled":true,"publicKey"`, `"enabled":false,"publicKey"`, 1) + `,` + off + `]`), false},
		{"peer ips", in(`[` + strings.Replace(on, "10.0.0.2/32", "10.0.0.3/32", 1) + `,` + off + `]`), false},
		{"one enabled", in(`[` + on + `,` + strings.Replace(off, `"enabled":false`, `"enabled":true`, 1) + `]`), false},
	} {
		if got := SameRealization(base, c.other); got != c.same {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	keyed := in(`[` + on + `]`)
	keyed.Secrets = map[string]string{"K": "new"}
	if SameRealization(in(`[`+on+`]`), keyed) {
		t.Error("a new key value realizes as the old")
	}
}

func TestKeptRecoveryKeepsWireGuard(t *testing.T) {
	d := wgFixture(t, wgIface(map[string]any{"privateKeyRef": map[string]any{"secret": "WG0_KEY", "isSet": true, "version": "3"}, "publicKey": wgPublic(t, wgKey(1)),
		"peers": []any{wgPeer(map[string]any{"presharedKeyRef": map[string]any{"secret": "WG0_PSK", "isSet": true, "version": "3"}})}}))
	d["routes"] = []any{routeTo(map[string]any{"target": map[string]any{"kind": "wireguard", "id": wgA}, "gateway": nil})}
	raw, err := json.Marshal(map[string]any{"declaration": d, "secrets": map[string]string{"WAN_PASSWORD": "TEST_ONLY_SECRET", "WG0_KEY": wgKey(1)}})
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
	if !strings.Contains(string(docs.Configuration), `"privateKeyRef":{"secret":"WG0_KEY"}`) || !strings.Contains(string(docs.Configuration), `"presharedKeyRef":{"secret":"WG0_PSK","unresolved":true}`) {
		t.Fatalf("references: %s", docs.Configuration)
	}
	if !strings.Contains(string(docs.ConfigurationSecrets), `"WG0_KEY":{"kind":"wireguard"`) {
		t.Fatalf("kinds: %s", docs.ConfigurationSecrets)
	}
}

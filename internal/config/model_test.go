package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

//go:embed testdata/network.json
var networkFixture []byte

func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	return networkFixture
}
func fixtureMap(t *testing.T) map[string]any {
	t.Helper()
	var d map[string]any
	if e := json.Unmarshal(fixtureBytes(t), &d); e != nil {
		t.Fatal(e)
	}
	return d
}
func linksOf(d map[string]any) []any     { return d["links"].([]any) }
func segmentsOf(d map[string]any) []any  { return d["segments"].([]any) }
func item(a []any, i int) map[string]any { return a[i].(map[string]any) }
func v4(d map[string]any, i int) map[string]any {
	return item(segmentsOf(d), i)["addressing"].(map[string]any)["ipv4"].(map[string]any)
}
func v6(d map[string]any, i int) map[string]any {
	return item(segmentsOf(d), i)["addressing"].(map[string]any)["ipv6"].(map[string]any)
}
func decodeMap(t *testing.T, d map[string]any) (declaration, error) {
	t.Helper()
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	return decodeDeclaration(b)
}

func TestStrictJSONBoundary(t *testing.T) {
	for _, raw := range []string{`{"p":"\ud800"}`, `{"p":1,"p":2}`, `{} {}`, `{"p":null}`, `{"p":1500.0}`, `{"p":1e3}`, `{"p":11111111111111111111}`, "{\"p\":\"\xff\"}", strings.Repeat("[", 33) + "0" + strings.Repeat("]", 33), `{"p":` + strings.Repeat("1", 1024*1024-6) + `}`, strings.Repeat(" ", 1024*1024+1)} {
		if _, e := decodeStrict([]byte(raw)); e == nil {
			t.Fatalf("accepted invalid bounded JSON (%d bytes)", len(raw))
		}
	}
	for _, raw := range []string{`{"p":"\ud800\udc00"}`, `{"p":-0}`, strings.Repeat("[", 32) + "0" + strings.Repeat("]", 32), `{"p":"€𐀀"}` + strings.Repeat(" ", 1024*1024-len(`{"p":"€𐀀"}`))} {
		if _, e := decodeStrict([]byte(raw)); e != nil {
			t.Fatalf("valid boundary rejected: %v", e)
		}
	}
}

func TestNetworkConstraints(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"unknown field", func(d map[string]any) { item(linksOf(d), 0)["surprise"] = true }},
		{"missing name", func(d map[string]any) { delete(item(linksOf(d), 0), "name") }},
		{"missing ID", func(d map[string]any) { delete(item(linksOf(d), 0), "id") }},
		{"duplicate ID", func(d map[string]any) { item(linksOf(d), 1)["id"] = item(linksOf(d), 0)["id"] }},
		{"duplicate name", func(d map[string]any) { item(linksOf(d), 1)["name"] = item(linksOf(d), 0)["name"] }},
		{"multicast MAC", func(d map[string]any) { item(linksOf(d), 0)["permanentMac"] = "01:00:5e:00:00:01" }},
		{"zero MAC", func(d map[string]any) { item(linksOf(d), 0)["permanentMac"] = "00:00:00:00:00:00" }},
		{"wrong kind field", func(d map[string]any) { item(linksOf(d), 1)["permanentMac"] = "52:54:00:00:00:01" }},
		{"duplicate VLAN", func(d map[string]any) { item(linksOf(d), 3)["vlanId"] = 6 }},
		{"VLAN oversized MTU", func(d map[string]any) { item(linksOf(d), 1)["mtu"] = 1501 }},
		{"PPP oversized MTU", func(d map[string]any) { item(linksOf(d), 2)["mtu"] = 1493 }},
		{"IPv6 too small MTU", func(d map[string]any) { item(linksOf(d), 4)["mtu"] = 1279 }},
		{"missing parent", func(d map[string]any) { item(linksOf(d), 1)["parent"] = "lnk_00000000-0000-4000-8000-999999999999" }},
		{"enslaved VLAN parent", func(d map[string]any) { item(linksOf(d), 1)["parent"] = item(linksOf(d), 3)["id"] }},
		{"segment enslaved member", func(d map[string]any) { item(segmentsOf(d), 1)["link"] = item(linksOf(d), 3)["id"] }},
		{"missing zone", func(d map[string]any) { item(segmentsOf(d), 1)["zone"] = "zon_00000000-0000-4000-8000-999999999999" }},
		{"IPv4 family", func(d map[string]any) { v4(d, 1)["addresses"] = []any{"2001:db8::7/64"} }},
		{"IPv4 network", func(d map[string]any) { v4(d, 1)["addresses"] = []any{"192.0.2.0/24"} }},
		{"IPv4 broadcast", func(d map[string]any) { v4(d, 1)["addresses"] = []any{"192.0.2.255/24"} }},
		{"loopback", func(d map[string]any) { v4(d, 1)["addresses"] = []any{"127.0.0.1/8"} }},
		{"unspecified", func(d map[string]any) { v4(d, 1)["addresses"] = []any{"0.0.0.0/32"} }},
		{"offlink gateway", func(d map[string]any) { v4(d, 1)["gateway"] = "198.51.100.1" }},
		{"delegation wrong source", func(d map[string]any) { v6(d, 1)["from"] = item(segmentsOf(d), 1)["id"] }},
		{"delegation collision", func(d map[string]any) {
			addSegment(d, "3", item(linksOf(d), 0)["id"].(string), map[string]any{"mode": "none"}, map[string]any{"mode": "delegated", "from": item(segmentsOf(d), 0)["id"], "subnetId": "0001"})
			v6(d, 1)["subnetId"] = "1"
		}},
		{"overlapping static subnets", func(d map[string]any) {
			addSegment(d, "3", item(linksOf(d), 0)["id"].(string), map[string]any{"mode": "static", "addresses": []any{"192.0.2.129/25"}}, map[string]any{"mode": "none"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := fixtureMap(t)
			tc.change(d)
			if _, e := decodeMap(t, d); e == nil {
				t.Fatal("accepted invalid topology")
			}
		})
	}
	for _, address := range []string{"192.0.2.0/31", "192.0.2.1/31", "192.0.2.7/32"} {
		d := fixtureMap(t)
		v4(d, 1)["addresses"] = []any{address}
		if _, e := decodeMap(t, d); e != nil {
			t.Fatalf("valid host %s: %v", address, e)
		}
	}
	t.Run("empty bridge", func(t *testing.T) {
		d := fixtureMap(t)
		item(linksOf(d), 4)["members"] = []any{}
		if _, e := decodeMap(t, d); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("scoped IPv6 link locals", func(t *testing.T) {
		d := fixtureMap(t)
		v6(d, 1)["mode"] = "static"
		delete(v6(d, 1), "from")
		delete(v6(d, 1), "subnetId")
		v6(d, 1)["addresses"] = []any{"fe80::1/64"}
		addSegment(d, "3", item(linksOf(d), 0)["id"].(string), map[string]any{"mode": "none"}, map[string]any{"mode": "static", "addresses": []any{"fe80::1/64"}})
		if _, e := decodeMap(t, d); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("same segment redundant coverage", func(t *testing.T) {
		d := fixtureMap(t)
		v4(d, 1)["addresses"] = []any{"192.0.2.7/24", "192.0.2.8/25"}
		if _, e := decodeMap(t, d); e != nil {
			t.Fatal(e)
		}
	})
}

func addSegment(d map[string]any, n, lnk string, a4, a6 map[string]any) {
	d["segments"] = append(segmentsOf(d), map[string]any{"id": "seg_00000000-0000-4000-8000-00000000000" + n, "name": "extra" + n, "link": lnk, "zone": item(d["zones"].([]any), 0)["id"], "addressing": map[string]any{"ipv4": a4, "ipv6": a6}})
}

func addLink(d map[string]any, n string, fields map[string]any) {
	l := map[string]any{"id": "lnk_00000000-0000-4000-8000-00000000000" + n, "name": "extra-link-" + n, "kind": "ethernet", "mtu": 1500, "enabled": true}
	for k, v := range fields {
		l[k] = v
	}
	d["links"] = append(linksOf(d), l)
}

func TestConstraintErrorPaths(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{"parent cycle", func(d map[string]any) {
			addLink(d, "6", map[string]any{"kind": "vlan", "vlanId": 20})
			addLink(d, "7", map[string]any{"kind": "vlan", "vlanId": 21})
			item(linksOf(d), 5)["parent"] = item(linksOf(d), 6)["id"]
			item(linksOf(d), 6)["parent"] = item(linksOf(d), 5)["id"]
		}, "/links/5"},
		{"duplicate permanent MAC", func(d map[string]any) {
			addLink(d, "6", map[string]any{"permanentMac": "52:54:00:aa:bb:01"})
		}, "/links/5/permanentMac"},
		{"empty bond", func(d map[string]any) {
			addLink(d, "6", map[string]any{"kind": "bond", "mode": "active-backup", "members": []any{}})
		}, "/links/5/members"},
		{"member with two masters", func(d map[string]any) {
			addLink(d, "6", map[string]any{"kind": "bridge", "members": []any{item(linksOf(d), 3)["id"]}})
		}, "/links/5/members"},
		{"two segments on one link", func(d map[string]any) {
			addSegment(d, "3", item(segmentsOf(d), 0)["link"].(string), map[string]any{"mode": "none"}, map[string]any{"mode": "none"})
		}, "/segments/2/link"},
		{"duplicate local address", func(d map[string]any) {
			v4(d, 1)["addresses"] = []any{"192.0.2.7/24", "192.0.2.7/25"}
		}, "/segments/1/addressing/ipv4/addresses/1"},
		{"PPPoE IPv4 mode mismatch", func(d map[string]any) {
			item(segmentsOf(d), 0)["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "static", "addresses": []any{"198.51.100.9/24"}}
		}, "/segments/0/addressing/ipv4/mode"},
		{"RA with IPv6 none", func(d map[string]any) {
			item(segmentsOf(d), 1)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "none", "ra": map[string]any{"enabled": true, "managed": false, "otherInfo": true}}
		}, "/segments/1/addressing/ipv6/ra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := fixtureMap(t)
			tc.change(d)
			_, e := decodeMap(t, d)
			ve, _ := e.(validationError)
			if !slices.ContainsFunc(ve, func(detail FieldError) bool { return detail.Path == tc.want }) {
				t.Fatalf("error %v; fields %+v; want %+v", e, ve, tc.want)
			}
		})
	}
}

func TestCanonicalNetworkRoundTrip(t *testing.T) {
	d := fixtureMap(t)
	item(d["zones"].([]any), 0)["description"] = "<safe> €"
	v6(d, 1)["subnetId"] = "ABBA"
	item(linksOf(d), 0)["permanentMac"] = "52:54:00:AA:BB:01"
	got, e := decodeMap(t, d)
	if e != nil {
		t.Fatal(e)
	}
	a, e := canonical(got)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(a, []byte("192.0.2.7/24")) || !bytes.Contains(a, []byte("<safe> €")) || bytes.Contains(a, []byte("ABBA")) || a[len(a)-1] != '\n' {
		t.Fatalf("lost host/canonical text: %s", a)
	}
	arr := linksOf(d)
	for i, j := 0, len(arr)-1; i < j; i, j = i+1, j-1 {
		arr[i], arr[j] = arr[j], arr[i]
	}
	again, e := decodeMap(t, d)
	if e != nil {
		t.Fatal(e)
	}
	b, e := canonical(again)
	if e != nil || !bytes.Equal(a, b) {
		t.Fatal("same network encoded differently")
	}
	if _, e := decodeDeclaration([]byte(`{"schemaVersion":2,"links":[],"segments":[],"zones":[]}`)); e == nil {
		t.Fatal("accepted future schema")
	}
}

func TestSchemaLoaderIsOffline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "external.json")
	if e := os.WriteFile(path, []byte(`{"type":"string"}`), 0600); e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"file://` + path + `"}`)
	c, e := newSchemaCompiler(map[string][]byte{"https://andaru.invalid/test": raw})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := c.Compile("https://andaru.invalid/test"); e == nil {
		t.Fatal("read unregistered filesystem schema")
	}
}

func TestCanonicalDocumentSize(t *testing.T) {
	valid := map[string]string{"v": strings.Repeat("x", documentLimit-9)}
	body, err := canonical(valid)
	if err != nil || len(body) != documentLimit {
		t.Fatalf("exact document limit: bytes=%d error=%v", len(body), err)
	}
	if _, err := canonical(map[string]string{"v": strings.Repeat("x", documentLimit-8)}); err == nil {
		t.Fatal("newline escaped the document limit")
	}
}

func TestAdvertisementModes(t *testing.T) {
	const ra = "/segments/1/addressing/ipv6/ra"
	static := func(d map[string]any, ra map[string]any) {
		item(segmentsOf(d), 1)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "static", "addresses": []any{"fd00:1::1/64"}, "ra": ra}
	}
	for name, tc := range map[string]struct {
		change func(map[string]any)
		want   string // "" accepts; "schema" is any schema refusal
	}{
		"unmanaged": {func(d map[string]any) { static(d, map[string]any{"mode": "unmanaged"}) }, ""},
		"all options": {func(d map[string]any) {
			static(d, map[string]any{"mode": "unmanaged", "priority": "high", "routerLifetime": 0, "validLifetime": 600, "preferredLifetime": 300, "pref64": "64:ff9b::/96", "dns": map[string]any{"servers": []any{"fd00:1::1"}, "searchDomains": []any{"home.arpa"}}})
		}, ""},
		"unknown mode":                      {func(d map[string]any) { static(d, map[string]any{"mode": "sometimes"}) }, "schema"},
		"lifetime 3":                        {func(d map[string]any) { static(d, map[string]any{"mode": "router", "routerLifetime": 3}) }, ra + "/routerLifetime"},
		"lifetime 9001":                     {func(d map[string]any) { static(d, map[string]any{"mode": "router", "routerLifetime": 9001}) }, ra + "/routerLifetime"},
		"valid below the default preferred": {func(d map[string]any) { static(d, map[string]any{"mode": "unmanaged", "validLifetime": 600}) }, ra + "/preferredLifetime"},
		"preferred above the default valid": {func(d map[string]any) { static(d, map[string]any{"mode": "unmanaged", "preferredLifetime": 90000}) }, ra + "/preferredLifetime"},
		"advertised IPv4 DNS server": {func(d map[string]any) {
			static(d, map[string]any{"mode": "unmanaged", "dns": map[string]any{"servers": []any{"192.0.2.53"}}})
		}, ra + "/dns/servers/0"},
		"preferred over valid": {func(d map[string]any) {
			static(d, map[string]any{"mode": "unmanaged", "validLifetime": 600, "preferredLifetime": 700})
		}, ra + "/preferredLifetime"},
		"pref64 length": {func(d map[string]any) { static(d, map[string]any{"mode": "unmanaged", "pref64": "64:ff9b::/97"}) }, ra + "/pref64"},
		"pref64 IPv4":   {func(d map[string]any) { static(d, map[string]any{"mode": "unmanaged", "pref64": "10.0.0.0/8"}) }, ra + "/pref64"},
		"advertised DNS server": {func(d map[string]any) {
			static(d, map[string]any{"mode": "unmanaged", "dns": map[string]any{"servers": []any{"nope"}}})
		}, ra + "/dns/servers/0"},
		"lifetimes on delegated": {func(d map[string]any) { v6(d, 1)["ra"] = map[string]any{"mode": "unmanaged", "validLifetime": 600} }, ra + "/validLifetime"},
		"without IPv6": {func(d map[string]any) {
			item(segmentsOf(d), 1)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "none", "ra": map[string]any{"mode": "unmanaged"}}
		}, ra},
		"disabled without IPv6": {func(d map[string]any) {
			item(segmentsOf(d), 1)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "none", "ra": map[string]any{"mode": "disabled"}}
		}, ""},
		"legacy and new mixed": {func(d map[string]any) {
			v6(d, 1)["ra"] = map[string]any{"mode": "router", "enabled": true, "managed": false, "otherInfo": false}
		}, "schema"},
	} {
		t.Run(name, func(t *testing.T) {
			d := fixtureMap(t)
			tc.change(d)
			_, e := decodeMap(t, d)
			ve, isValidation := e.(validationError)
			switch {
			case tc.want == "" && e != nil:
				t.Fatalf("refused: %v %+v", e, ve)
			case tc.want == "schema" && e == nil:
				t.Fatal("accepted")
			case tc.want != "" && tc.want != "schema" && (!isValidation || !slices.ContainsFunc(ve, func(f FieldError) bool { return f.Path == tc.want })):
				t.Fatalf("error %v; fields %+v; want %s", e, ve, tc.want)
			}
		})
	}
	for raw, want := range map[string]string{
		`{"enabled":true,"managed":false,"otherInfo":true}`:   "stateless",
		`{"enabled":true,"managed":true,"otherInfo":true}`:    "managed",
		`{"enabled":true,"managed":false,"otherInfo":false}`:  "unmanaged",
		`{"enabled":false,"managed":false,"otherInfo":false}`: "disabled",
	} {
		d := fixtureMap(t)
		var a any
		_ = json.Unmarshal([]byte(raw), &a)
		v6(d, 1)["ra"] = a
		got, e := decodeMap(t, d)
		if e != nil || got.Segments[1].Addressing.IPv6.RA.mode() != want {
			t.Fatalf("%s: %v, mode %q, want %q", raw, e, got.Segments[1].Addressing.IPv6.RA.mode(), want)
		}
	}
}

func TestDelegatedStableAddress(t *testing.T) {
	d := fixtureMap(t)
	v6(d, 1)["addresses"] = []any{"fd00:a5d:1::1/64"}
	if _, e := decodeMap(t, d); e != nil {
		t.Fatalf("stable address refused: %v", e)
	}
	addLink(d, "6", map[string]any{"permanentMac": "52:54:00:aa:bb:66"})
	addSegment(d, "3", "lnk_00000000-0000-4000-8000-000000000006", map[string]any{"mode": "none"}, map[string]any{"mode": "static", "addresses": []any{"fd00:a5d:1::1/64"}})
	_, e := decodeMap(t, d)
	if ve, _ := e.(validationError); !slices.ContainsFunc(ve, func(f FieldError) bool { return f.Message == "Local address is already assigned." }) {
		t.Fatalf("duplicate accepted: %v", e)
	}
}

func TestDHCPv6ClientOptions(t *testing.T) {
	d := fixtureMap(t)
	for k, v := range map[string]any{"prefixOnly": true, "withoutRA": true, "sendRelease": true, "duid": "00:04:AA:bb:cc:dd"} {
		v6(d, 0)[k] = v
	}
	got, e := decodeMap(t, d)
	if a := got.Segments[0].Addressing.IPv6; e != nil || !a.PrefixOnly || !a.WithoutRA || !a.SendRelease || a.DUID != "00:04:aa:bb:cc:dd" {
		t.Fatalf("%v %+v", e, a)
	}
	for _, bad := range []string{"xyz", "00:04"} {
		d := fixtureMap(t)
		v6(d, 0)["duid"] = bad
		if _, e := decodeMap(t, d); e == nil {
			t.Fatalf("duid %q accepted", bad)
		}
	}
	d = fixtureMap(t)
	item(segmentsOf(d), 0)["addressing"].(map[string]any)["ipv6"] = map[string]any{"mode": "slaac", "prefixOnly": true}
	if _, e := decodeMap(t, d); e == nil {
		t.Fatal("prefixOnly accepted on slaac")
	}
}

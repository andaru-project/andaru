package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
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
		{"parent cycle", func(d map[string]any) {
			item(linksOf(d), 1)["parent"] = item(linksOf(d), 3)["id"]
			item(linksOf(d), 3)["parent"] = item(linksOf(d), 1)["id"]
		}},
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
	if _, e := compileSchema(raw, "https://andaru.invalid/test"); e == nil {
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

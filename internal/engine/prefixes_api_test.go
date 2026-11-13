package engine

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func writeUnder(t *testing.T, root, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The prefix view: what the WAN holds, the room it leaves, and where each segment and pool sits in it.
func TestPrefixesStatus(t *testing.T) {
	root := t.TempDir()
	prefixesRoot = root
	t.Cleanup(func() { prefixesRoot = "" })
	writeUnder(t, root, "/run/systemd/network/50-andaru-ens18.network", "[DHCPv6]\nPrefixDelegationHint=::/48\n")
	writeUnder(t, root, "/run/systemd/network/50-andaru-ens19.network", "[Network]\nIPv6SendRA=yes\n[DHCPPrefixDelegation]\nUplinkInterface=ens18\nSubnetId=0x1\n")
	writeUnder(t, root, "/run/systemd/network/50-andaru-ens20.network", "[Network]\nIPv6SendRA=yes\n[DHCPPrefixDelegation]\nUplinkInterface=ens18\nSubnetId=0x100\n")
	writeUnder(t, root, "/run/andaru-kea/kea-dhcp6.template.json", `{"subnets":[{"id":7,"interface":"ens19","uplink":"ens18","subnetId":"1","pool":{"subnetId":"8","length":60,"delegatedLength":64}}]}`)
	h := newHandler(nil, http.NotFoundHandler())
	get := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/status/prefixes"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		validPrefixesResponse(t, rec.Body.Bytes())
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body["wans"].([]any)[0].(map[string]any)
	}
	// Nothing seen yet: the room follows the request.
	w := get()
	if w["current"] != nil || w["subnetIds"].(map[string]any)["last"] != "ffff" || w["requested"] != float64(48) {
		t.Fatalf("%v", w)
	}
	writeUnder(t, root, "/var/lib/andaru/delegated-prefixes.json", `{"wans":{"ens18":{"requested":48,"current":{"prefix":"2001:db8:aa00::/56","preferred":"2099-01-01T00:00:00Z","valid":"2099-01-01T00:00:00Z"}}}}`)
	w = get()
	segments := w["segments"].([]any)
	if w["subnetIds"].(map[string]any)["last"] != "ff" || w["current"].(map[string]any)["prefix"] != "2001:db8:aa00::/56" {
		t.Fatalf("%v", w)
	}
	if s := segments[0].(map[string]any); s["interface"] != "ens19" || s["prefix"] != "2001:db8:aa00:1::/64" {
		t.Fatalf("%v", segments)
	}
	if s := segments[1].(map[string]any); s["interface"] != "ens20" || s["prefix"] != nil {
		t.Fatalf("%v", segments)
	}
	if p := w["pools"].([]any)[0].(map[string]any); p["interface"] != "ens19" || p["prefix"] != "2001:db8:aa00:80::/60" {
		t.Fatalf("%v", w["pools"])
	}
	if nf := w["notFitting"].([]any); len(nf) != 1 {
		t.Fatalf("%v", nf)
	}
}

func validPrefixesResponse(t *testing.T, body []byte) {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(openAPIDocument))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("openapi.json#/components/schemas/PrefixesResponse")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err := s.Validate(v); err != nil {
		t.Fatalf("the response does not match the served schema: %v\n%s", err, body)
	}
}

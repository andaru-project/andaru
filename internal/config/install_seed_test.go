//go:build !integration

// The root suite runs this package's test binary outside the source tree, where the fixtures are not.
package config

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The installer's seed fixtures (docs/dev/install-seed.md): each port's addressing is Andaru's own configuration
// object, and passes the validator's checks of each answer on its own. Slice 3's reader adds the checks across answers.
func TestInstallSeedFixturesAddressing(t *testing.T) {
	c, err := newSchemaCompiler(map[string][]byte{schemaURI: SchemaBundle})
	if err != nil {
		t.Fatal(err)
	}
	addressing, err := c.Compile(schemaURI + "#/$defs/addressing")
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("../../image/iso/testdata/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no seed fixtures: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		seed := v.(map[string]any)
		if seed["version"] != json.Number("1") {
			t.Errorf("%s: version is %#v, not the number 1", f, seed["version"])
		}
		for _, port := range []string{"management", "wan"} {
			raw, present := seed[port]
			if !present && port == "wan" {
				continue
			}
			p, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s: %s is not an object", f, port)
				continue
			}
			if err := addressing.Validate(p["addressing"]); err != nil {
				t.Errorf("%s: %s addressing: %v", f, port, err)
				continue
			}
			checkSeedAnswers(t, f+": "+port, p["addressing"].(map[string]any))
		}
	}
}

// checkSeedAnswers runs the validator's own checks of a single address, gateway, DUID or DNS list. The doc's other
// rules (one address, the RA rules, the subnetId bound) are the installer's, held by its specs.
func checkSeedAnswers(t *testing.T, where string, a map[string]any) {
	t.Helper()
	for _, family := range []string{"ipv4", "ipv6"} {
		fam := a[family].(map[string]any)
		for _, raw := range asStrings(fam["addresses"]) {
			p, err := netip.ParsePrefix(raw)
			if err != nil || (family == "ipv4") != p.Addr().Is4() || p.Addr().Is4In6() || p.Addr().Zone() != "" || !validLocal(p) {
				t.Errorf("%s: %s address %q is refused by the validator", where, family, raw)
			}
		}
		if raw, ok := fam["gateway"].(string); ok {
			g, err := netip.ParseAddr(raw)
			if err != nil || (family == "ipv4") != g.Is4() || g.Is4In6() || g.Zone() != "" || !validUnicast(g) {
				t.Errorf("%s: %s gateway %q is refused by the validator", where, family, raw)
			}
		}
		if raw, ok := fam["duid"].(string); ok && !validDUID(raw) {
			t.Errorf("%s: duid %q is refused by the validator", where, raw)
		}
	}
	if d, ok := a["dns"].(map[string]any); ok {
		validateDNSList(where+" dns", &dns{Servers: asStrings(d["servers"]), SearchDomains: asStrings(d["searchDomains"])}, func(path, msg string) { t.Errorf("%s: %s", path, msg) })
	}
}

func asStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, s := range list {
		out = append(out, s.(string))
	}
	return out
}

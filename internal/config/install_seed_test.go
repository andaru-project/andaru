package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The installer's seed fixtures (docs/dev/install-seed.md): each port's addressing is Andaru's own configuration
// object. Slice 3's reader adds the validator's checks.
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
			}
		}
	}
}

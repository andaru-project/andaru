package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The installer's seed fixtures (docs/dev/install-seed.md): each port's addressing is Andaru's own, so slice 3 can copy
// it into the first configuration unchanged.
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
		if fmt.Sprint(seed["version"]) != "1" {
			t.Errorf("%s: version is %v, not 1", f, seed["version"])
		}
		for _, port := range []string{"management", "wan"} {
			p, ok := seed[port].(map[string]any)
			if !ok {
				if port == "management" {
					t.Errorf("%s: no management port", f)
				}
				continue
			}
			if err := addressing.Validate(p["addressing"]); err != nil {
				t.Errorf("%s: %s addressing: %v", f, port, err)
			}
		}
	}
}

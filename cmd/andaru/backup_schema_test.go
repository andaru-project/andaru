package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// The engine shows every configuration in the current form, whatever form it was saved or backed up in.
func TestDeclarationViewIsOfTheCurrentSchema(t *testing.T) {
	for _, tc := range []struct {
		version int
		valid   bool
	}{{3, false}, {4, true}, {5, false}} {
		raw := json.RawMessage(fmt.Sprintf(`{"schemaVersion":%d,"links":[],"segments":[],"zones":[]}`, tc.version))
		if validDeclarationView(raw) != tc.valid {
			t.Fatalf("schema %d support mismatch", tc.version)
		}
	}
}

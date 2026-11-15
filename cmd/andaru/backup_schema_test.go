package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestDeclarationViewSupportsRestoredSchemas(t *testing.T) {
	for _, tc := range []struct {
		version int
		valid   bool
	}{{1, true}, {2, true}, {3, false}} {
		raw := json.RawMessage(fmt.Sprintf(`{"schemaVersion":%d,"links":[],"segments":[],"zones":[]}`, tc.version))
		if validDeclarationView(raw) != tc.valid {
			t.Fatalf("schema %d support mismatch", tc.version)
		}
	}
}

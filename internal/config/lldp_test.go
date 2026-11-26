package config

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestLLDPLinkSchemaAndSafePointer(t *testing.T) {
	set, err := schemas()
	if err != nil {
		t.Fatal(err)
	}
	linkSchema := set.objects["links"]
	unmarshal := func(raw []byte) any {
		t.Helper()
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	validCases := []string{
		`{"name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01","lldp":"receive","emitLldp":"nearest-bridge"}`,
		`{"name":"eth1","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:02","lldp":"routers-only","emitLldp":"customer-bridge"}`,
		`{"name":"eth2","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:03","lldp":"off","emitLldp":"non-tpmr-bridge"}`,
		`{"name":"eth3","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:04","lldp":"off","emitLldp":"off"}`,
	}
	for _, raw := range validCases {
		if err := linkSchema.Validate(unmarshal([]byte(raw))); err != nil {
			t.Fatalf("expected valid LLDP link %s, got: %v", raw, err)
		}
	}

	var parsed link
	if err := json.Unmarshal([]byte(validCases[0]), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.LLDP != "receive" || parsed.EmitLLDP != "nearest-bridge" {
		t.Fatalf("unexpected LLDP unmarshal: %+v", parsed)
	}

	// Non-ethernet links do not accept LLDP fields; invalid enum values return exact safePointer paths.
	invalidCases := []struct {
		name     string
		raw      string
		wantPath string
	}{
		{"bad lldp enum", `{"name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01","lldp":"all"}`, "/lldp"},
		{"bad emitLldp enum", `{"name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01","emitLldp":"everywhere"}`, "/emitLldp"},
		{"lldp on vlan", `{"name":"vl10","kind":"vlan","mtu":1500,"enabled":true,"parent":"@p0","vlanId":10,"lldp":"receive"}`, ""},
		{"emitLldp on bridge", `{"name":"br0","kind":"bridge","mtu":1500,"enabled":true,"members":[],"emitLldp":"nearest-bridge"}`, ""},
		{"lldp on bond", `{"name":"bo0","kind":"bond","mtu":1500,"enabled":true,"members":["@p0"],"mode":"802.3ad","lldp":"receive"}`, ""},
		{"lldp on pppoe", `{"name":"isp","kind":"pppoe","mtu":1492,"enabled":true,"parent":"@p0","username":"u","lldp":"receive"}`, ""},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			err := linkSchema.Validate(unmarshal([]byte(tc.raw)))
			if err == nil {
				t.Fatalf("expected schema rejection for %s", tc.name)
			}
			if tc.wantPath != "" {
				ve, ok := schemaErrors(err).(validationError)
				if !ok {
					t.Fatalf("expected validationError, got %T", schemaErrors(err))
				}
				found := false
				for _, d := range ve {
					if d.Path == tc.wantPath {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected schemaErrors path %q, got %+v", tc.wantPath, ve)
				}
			}
		})
	}
}

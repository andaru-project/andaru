package config

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestBridgeAndBondOptionsValidation(t *testing.T) {
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

	validBridge := []byte(`{
		"name":"LAN bridge","kind":"bridge","mtu":1500,"enabled":true,"members":[],
		"stp":true,"stpPriority":4096,"forwardDelay":15,"helloTime":2,"maxAge":20,
		"multicastSnooping":false
	}`)
	if err := linkSchema.Validate(unmarshal(validBridge)); err != nil {
		t.Fatalf("expected valid bridge options, got: %v", err)
	}

	validBond := []byte(`{
		"name":"Uplink bond","kind":"bond","mtu":1500,"enabled":true,"members":["@p0"],
		"mode":"802.3ad","lacpRate":"fast","miiMonitorMs":100,"transmitHashPolicy":"layer3+4"
	}`)
	if err := linkSchema.Validate(unmarshal(validBond)); err != nil {
		t.Fatalf("expected valid bond options, got: %v", err)
	}

	// Schema range / enum rejections, including verified safePointer paths returned by schemaErrors.
	schemaBad := []struct {
		name     string
		body     string
		wantPath string
	}{
		{"stpPriority out of range", `{"name":"br","kind":"bridge","mtu":1500,"enabled":true,"members":[],"stp":true,"stpPriority":70000}`, "/stpPriority"},
		{"forwardDelay below min", `{"name":"br","kind":"bridge","mtu":1500,"enabled":true,"members":[],"stp":true,"forwardDelay":1}`, "/forwardDelay"},
		{"helloTime above max", `{"name":"br","kind":"bridge","mtu":1500,"enabled":true,"members":[],"stp":true,"helloTime":11}`, "/helloTime"},
		{"maxAge below min", `{"name":"br","kind":"bridge","mtu":1500,"enabled":true,"members":[],"stp":true,"maxAge":5}`, "/maxAge"},
		{"stp wrong type", `{"name":"br","kind":"bridge","mtu":1500,"enabled":true,"members":[],"stp":"yes"}`, "/stp"},
		{"multicastSnooping wrong type", `{"name":"br","kind":"bridge","mtu":1500,"enabled":true,"members":[],"multicastSnooping":"no"}`, "/multicastSnooping"},
		{"bridge field on ethernet", `{"name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01","stp":true}`, ""},
		{"bond field on bridge", `{"name":"br","kind":"bridge","mtu":1500,"enabled":true,"members":[],"lacpRate":"fast"}`, ""},
		{"bad lacpRate", `{"name":"bo","kind":"bond","mtu":1500,"enabled":true,"members":["@p0"],"mode":"802.3ad","lacpRate":"turbo"}`, "/lacpRate"},
		{"bad transmitHashPolicy", `{"name":"bo","kind":"bond","mtu":1500,"enabled":true,"members":["@p0"],"mode":"802.3ad","transmitHashPolicy":"layer5"}`, "/transmitHashPolicy"},
		{"miiMonitorMs negative", `{"name":"bo","kind":"bond","mtu":1500,"enabled":true,"members":["@p0"],"mode":"802.3ad","miiMonitorMs":-1}`, "/miiMonitorMs"},
		{"miiMonitorMs above max", `{"name":"bo","kind":"bond","mtu":1500,"enabled":true,"members":["@p0"],"mode":"802.3ad","miiMonitorMs":60001}`, "/miiMonitorMs"},
	}
	for _, tc := range schemaBad {
		t.Run(tc.name, func(t *testing.T) {
			err := linkSchema.Validate(unmarshal([]byte(tc.body)))
			if err == nil {
				t.Fatalf("expected schema error for %s", tc.name)
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

	// Semantic validation: STP tuning requires stp=true; lacpRate requires 802.3ad; transmitHashPolicy requires xor/802.3ad/tlb.
	ethID := "lnk_00000001-0000-4000-8000-000000000001"
	brID := "lnk_00000002-0000-4000-8000-000000000002"
	boID := "lnk_00000003-0000-4000-8000-000000000003"
	boolPtr := func(b bool) *bool { return &b }
	intPtr := func(i int) *int { return &i }
	empty := []string{}
	members := []string{ethID}

	semanticCases := []struct {
		name     string
		links    []link
		wantPath string
	}{
		{
			name: "stpPriority without stp",
			links: []link{
				{identity: identity{ID: brID, Name: "br0"}, Kind: "bridge", MTU: 1500, Enabled: true, Members: &empty, STPPriority: intPtr(4096)},
			},
			wantPath: "/links/0/stpPriority",
		},
		{
			name: "forwardDelay with stp=false",
			links: []link{
				{identity: identity{ID: brID, Name: "br0"}, Kind: "bridge", MTU: 1500, Enabled: true, Members: &empty, STP: boolPtr(false), ForwardDelay: intPtr(10)},
			},
			wantPath: "/links/0/forwardDelay",
		},
		{
			name: "helloTime without stp",
			links: []link{
				{identity: identity{ID: brID, Name: "br0"}, Kind: "bridge", MTU: 1500, Enabled: true, Members: &empty, HelloTime: intPtr(2)},
			},
			wantPath: "/links/0/helloTime",
		},
		{
			name: "maxAge without stp",
			links: []link{
				{identity: identity{ID: brID, Name: "br0"}, Kind: "bridge", MTU: 1500, Enabled: true, Members: &empty, MaxAge: intPtr(20)},
			},
			wantPath: "/links/0/maxAge",
		},
		{
			name: "maxAge too small for helloTime",
			links: []link{
				{identity: identity{ID: brID, Name: "br0"}, Kind: "bridge", MTU: 1500, Enabled: true, Members: &empty, STP: boolPtr(true), HelloTime: intPtr(10), MaxAge: intPtr(20)},
			},
			wantPath: "/links/0/maxAge",
		},
		{
			name: "forwardDelay too small for maxAge",
			links: []link{
				{identity: identity{ID: brID, Name: "br0"}, Kind: "bridge", MTU: 1500, Enabled: true, Members: &empty, STP: boolPtr(true), ForwardDelay: intPtr(10), MaxAge: intPtr(20)},
			},
			wantPath: "/links/0/forwardDelay",
		},
		{
			name: "lacpRate on active-backup",
			links: []link{
				{identity: identity{ID: ethID, Name: "eth0"}, Kind: "ethernet", MTU: 1500, Enabled: true, PermanentMAC: "52:54:00:00:00:01"},
				{identity: identity{ID: boID, Name: "bond0"}, Kind: "bond", MTU: 1500, Enabled: true, Members: &members, Mode: "active-backup", LACPRate: "fast"},
			},
			wantPath: "/links/1/lacpRate",
		},
		{
			name: "transmitHashPolicy on active-backup",
			links: []link{
				{identity: identity{ID: ethID, Name: "eth0"}, Kind: "ethernet", MTU: 1500, Enabled: true, PermanentMAC: "52:54:00:00:00:01"},
				{identity: identity{ID: boID, Name: "bond0"}, Kind: "bond", MTU: 1500, Enabled: true, Members: &members, Mode: "active-backup", TransmitHashPolicy: "layer3+4"},
			},
			wantPath: "/links/1/transmitHashPolicy",
		},
		{
			name: "transmitHashPolicy on balance-tlb rejected",
			links: []link{
				{identity: identity{ID: ethID, Name: "eth0"}, Kind: "ethernet", MTU: 1500, Enabled: true, PermanentMAC: "52:54:00:00:00:01"},
				{identity: identity{ID: boID, Name: "bond0"}, Kind: "bond", MTU: 1500, Enabled: true, Members: &members, Mode: "balance-tlb", TransmitHashPolicy: "layer3+4"},
			},
			wantPath: "/links/1/transmitHashPolicy",
		},
		{
			name: "valid stp bridge and 802.3ad bond",
			links: []link{
				{identity: identity{ID: ethID, Name: "eth0"}, Kind: "ethernet", MTU: 1500, Enabled: true, PermanentMAC: "52:54:00:00:00:01"},
				{identity: identity{ID: boID, Name: "bond0"}, Kind: "bond", MTU: 1500, Enabled: true, Members: &members, Mode: "802.3ad", LACPRate: "fast", MIIMonitorMs: intPtr(100), TransmitHashPolicy: "layer3+4"},
				{identity: identity{ID: brID, Name: "br0"}, Kind: "bridge", MTU: 1500, Enabled: true, Members: &empty, STP: boolPtr(true), STPPriority: intPtr(4096), ForwardDelay: intPtr(15), HelloTime: intPtr(2), MaxAge: intPtr(20), MulticastSnooping: boolPtr(false)},
			},
		},
		{
			name: "valid balance-xor hash policy",
			links: []link{
				{identity: identity{ID: ethID, Name: "eth0"}, Kind: "ethernet", MTU: 1500, Enabled: true, PermanentMAC: "52:54:00:00:00:01"},
				{identity: identity{ID: boID, Name: "bond0"}, Kind: "bond", MTU: 1500, Enabled: true, Members: &members, Mode: "balance-xor", TransmitHashPolicy: "layer2+3", MIIMonitorMs: intPtr(0)},
			},
		},
	}

	for _, tc := range semanticCases {
		t.Run(tc.name, func(t *testing.T) {
			d := declaration{Links: tc.links}
			details := validateTopology(&d)
			if tc.wantPath == "" {
				if len(details) != 0 {
					t.Fatalf("expected no validation errors, got: %+v", details)
				}
				return
			}
			for _, detail := range details {
				if detail.Path == tc.wantPath {
					return
				}
			}
			t.Fatalf("expected error at %s, got: %+v", tc.wantPath, details)
		})
	}

	// JSON round-trip preserves all new fields cleanly.
	var parsed link
	if err := json.Unmarshal(validBridge, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.STP == nil || !*parsed.STP || parsed.STPPriority == nil || *parsed.STPPriority != 4096 || parsed.MulticastSnooping == nil || *parsed.MulticastSnooping {
		t.Fatalf("unexpected bridge unmarshal: %+v", parsed)
	}
}

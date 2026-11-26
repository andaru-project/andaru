package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestZoneOptionalColorSchemaAndCanonical(t *testing.T) {
	set, err := schemas()
	if err != nil {
		t.Fatal(err)
	}
	zoneSchema := set.objects["zones"]

	unmarshal := func(raw []byte) any {
		t.Helper()
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// Valid zone with optional color passes schema and preserves color.
	valid := []byte(`{"name":"DMZ","description":"Public servers","color":"violet"}`)
	if err := zoneSchema.Validate(unmarshal(valid)); err != nil {
		t.Fatalf("expected valid zone with color, got: %v", err)
	}
	var colored zone
	if err := json.Unmarshal(valid, &colored); err != nil {
		t.Fatal(err)
	}
	if colored.Color != "violet" {
		t.Fatalf("zone color = %q, want violet", colored.Color)
	}

	// Valid zone without color passes schema and omits color on marshal.
	plain := []byte(`{"name":"LAN"}`)
	if err := zoneSchema.Validate(unmarshal(plain)); err != nil {
		t.Fatalf("expected valid zone without color, got: %v", err)
	}
	var z zone
	if err := json.Unmarshal(plain, &z); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(z)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"color"`) {
		t.Fatalf("expected empty color to be omitted, got %s", raw)
	}

	// Unknown color value is rejected by schema.
	invalid := []byte(`{"name":"DMZ","color":"chartreuse"}`)
	if err := zoneSchema.Validate(unmarshal(invalid)); err == nil {
		t.Fatal("expected schema rejection for unsupported zone color")
	}
}

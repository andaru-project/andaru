package config

import (
	"testing"
)

func TestValidSecretScope(t *testing.T) {
	for _, good := range []string{"core", "addon:org.andaru.tailscale", "addon:com.example.dns-1"} {
		if !validSecretScope(good) {
			t.Errorf("validSecretScope(%q) = false, want true", good)
		}
	}
	for _, bad := range []string{"", "addon:", "addon:bad", "addon:com.example.dns_1", "other:org.andaru.tailscale", "addon:org.andaru..tailscale"} {
		if validSecretScope(bad) {
			t.Errorf("validSecretScope(%q) = true, want false", bad)
		}
	}
}

func TestValidateAddonSecretWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   map[string]any
		wantErr bool
	}{
		{name: "ref only", input: map[string]any{"secret": "auth_key"}},
		{name: "unresolved ref", input: map[string]any{"secret": "auth_key", "unresolved": true}},
		{name: "valid set", input: map[string]any{"secret": "auth_key", "set": "tskey-1", "expectedVersion": "0"}},
		{name: "unknown field", input: map[string]any{"secret": "auth_key", "extra": 1}, wantErr: true},
		{name: "non-string secret", input: map[string]any{"secret": 123}, wantErr: true},
		{name: "non-string set", input: map[string]any{"secret": "auth_key", "set": 123}, wantErr: true},
		{name: "nul in set", input: map[string]any{"secret": "auth_key", "set": "a\x00b"}, wantErr: true},
		{name: "expectedVersion without set", input: map[string]any{"secret": "auth_key", "expectedVersion": "0"}, wantErr: true},
		{name: "unresolved with set", input: map[string]any{"secret": "auth_key", "set": "x", "unresolved": true}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := validateAddonSecretWrite(tc.input, "/addons/0/settings/authKey")
			if (f != nil) != tc.wantErr {
				t.Fatalf("fault = %+v, wantErr = %v", f, tc.wantErr)
			}
		})
	}
}

func TestValidateAddonsNonRoot(t *testing.T) {
	zoneID := "zon_01234567-89ab-4def-8123-456789abcdef"
	d := &declaration{
		Zones: []zone{{ID: zoneID, Name: "VPN"}},
		Addons: []addonConfig{
			{
				ID:      "org.andaru.tailscale",
				Enabled: true,
				Settings: map[string]any{
					"hostname": "gw",
					"authKey":  map[string]any{"secret": "auth_key"},
				},
				Interfaces: []addonInterface{{Name: "tailscale0", Zone: zoneID}},
				BusGrants: addonBusGrants{
					Publish: map[string]string{"notification/addon.tailscale": "granted"},
				},
			},
		},
	}
	if errs := validateAddons(d); len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}

	d.Addons[0].Settings["nullField"] = nil
	if errs := validateAddons(d); len(errs) == 0 {
		t.Fatal("expected rejection for explicit null setting")
	}
}

package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
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
	delete(d.Addons[0].Settings, "nullField")

	// Stored settings must reject write-only secret commands and malformed objects (e.g. during restore).
	for _, badObj := range []map[string]any{
		{"secret": "auth_key", "set": "plaintext"},
		{"secret": "auth_key", "expectedVersion": "0"},
		{"secret": "auth_key", "extra": true},
		{"unresolved": true},
		{"secret": 123},
		{"secret": "auth_key", "unresolved": "yes"},
	} {
		d.Addons[0].Settings["authKey"] = badObj
		if errs := validateAddons(d); len(errs) == 0 {
			t.Fatalf("expected validateAddons rejection for object setting %#v", badObj)
		}
	}
	d.Addons[0].Settings["authKey"] = map[string]any{"secret": "auth_key", "unresolved": true}
	if errs := validateAddons(d); len(errs) != 0 {
		t.Fatalf("expected valid unresolved stored secret ref, got %+v", errs)
	}
}

func TestValidateBackupDocumentsRejectsSecretWriteInAddonSettings(t *testing.T) {
	raw := []byte(`{"schemaVersion":4,"links":[],"segments":[],"zones":[],"addons":[{"id":"org.andaru.tailscale","enabled":false,"settings":{"authKey":{"secret":"auth_key","set":"plaintext"}}}]}`)
	if err := ValidateBackupDocuments(RestoreDocuments{Configuration: raw}); err == nil {
		t.Fatal("expected ValidateBackupDocuments to reject plaintext secret write in restored add-on settings")
	}
}

func TestShippedTailscaleCatalogConfigValidation(t *testing.T) {
	cat, err := addon.LoadCatalog(filepath.Join("..", "..", "image", "files", "usr", "share", "andaru", "addons"))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	zoneID := "zon_01234567-89ab-4def-8123-456789abcdef"

	// Enabled without required tailscale0 zone binding must be rejected.
	dNoZone := &declaration{
		Zones: []zone{{ID: zoneID, Name: "VPN"}},
		Addons: []addonConfig{{
			ID:      "org.andaru.tailscale",
			Enabled: true,
		}},
	}
	if errs := validateAddonsAgainstCatalog(cat, dNoZone, nil, nil); len(errs) == 0 {
		t.Fatal("expected rejection when enabled Tailscale has no zone bound to tailscale0")
	}

	// Enabled with zone bound succeeds and materializes default routingMode with --netfilter-mode=off.
	dValid := &declaration{
		Zones: []zone{{ID: zoneID, Name: "VPN"}},
		Addons: []addonConfig{{
			ID:         "org.andaru.tailscale",
			Enabled:    true,
			Interfaces: []addonInterface{{Name: "tailscale0", Zone: zoneID}},
		}},
	}
	if errs := validateAddonsAgainstCatalog(cat, dValid, nil, nil); len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %+v", errs)
	}
	rm, ok := addon.ToStringSlice(dValid.Addons[0].Settings["routingMode"])
	const wantDefaultRouting = "--netfilter-mode=off --accept-routes=false --advertise-exit-node=false"
	if !ok || len(rm) != 1 || rm[0] != wantDefaultRouting {
		t.Fatalf("materialized routingMode = %#v, want [%s]", dValid.Addons[0].Settings["routingMode"], wantDefaultRouting)
	}

	// Invalid CIDR in advertiseRoutes must be rejected.
	dBadCIDR := &declaration{
		Zones: []zone{{ID: zoneID, Name: "VPN"}},
		Addons: []addonConfig{{
			ID:         "org.andaru.tailscale",
			Enabled:    true,
			Settings:   map[string]any{"advertiseRoutes": []string{"not-a-cidr"}},
			Interfaces: []addonInterface{{Name: "tailscale0", Zone: zoneID}},
		}},
	}
	if errs := validateAddonsAgainstCatalog(cat, dBadCIDR, nil, nil); len(errs) == 0 {
		t.Fatal("expected rejection for invalid advertiseRoutes CIDR")
	}

	// Backup restoration must validate cataloged enabled add-ons fail-closed without mutating input,
	// while still permitting retired/disabled add-ons and schema upgrades (ownSecrets=true).
	current := &savedState{
		Version:  "1",
		Config:   declaration{SchemaVersion: 4},
		Versions: map[objectKey]objectVersion{},
		Secrets:  emptySecrets(),
	}
	for _, badRouting := range []any{[]string{}, []string{"--netfilter-mode=on"}} {
		badDecl := &declaration{
			SchemaVersion: 4,
			Zones:         []zone{{ID: zoneID, Name: "VPN"}},
			Addons: []addonConfig{{
				ID:         "org.andaru.tailscale",
				Enabled:    true,
				Settings:   map[string]any{"routingMode": badRouting},
				Interfaces: []addonInterface{{Name: "tailscale0", Zone: zoneID}},
			}},
		}
		if _, _, err := prepareRestoration(current, decodedBackup{configuration: badDecl}, "2", time.Now().UTC(), cat, nil); err == nil {
			t.Fatalf("prepareRestoration unexpectedly accepted hostile routingMode %#v", badRouting)
		}
	}
	// Cross-version backup with a retired/unknown add-on and a disabled add-on restores cleanly.
	crossVer := &declaration{
		SchemaVersion: 4,
		Zones:         []zone{{ID: zoneID, Name: "VPN"}},
		Addons: []addonConfig{
			{ID: "org.andaru.retired", Enabled: true},
			{ID: "org.andaru.tailscale", Enabled: false, Settings: map[string]any{"routingMode": []string{"--netfilter-mode=on"}}},
		},
	}
	if _, _, err := prepareRestoration(current, decodedBackup{configuration: crossVer}, "2", time.Now().UTC(), cat, nil); err != nil {
		t.Fatalf("prepareRestoration rejected retired/disabled add-ons in backup: %v", err)
	}
	// Schema upgrade (ownSecrets=true) must never deadlock on retired or manifest-changed add-ons.
	upgradeDecl := &declaration{
		SchemaVersion: 4,
		Zones:         []zone{{ID: zoneID, Name: "VPN"}},
		Addons: []addonConfig{
			{ID: "org.andaru.retired", Enabled: true},
			{ID: "org.andaru.tailscale", Enabled: true, Settings: map[string]any{"routingMode": []string{"--netfilter-mode=on"}}},
		},
	}
	if _, _, err := prepareRestoration(current, decodedBackup{configuration: upgradeDecl, ownSecrets: true}, "2", time.Now().UTC(), cat, nil); err != nil {
		t.Fatalf("prepareRestoration(ownSecrets=true) deadlocked upgrade: %v", err)
	}
}

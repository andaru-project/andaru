package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/authz"
)

const testTailscaleCatalogManifest = `{
  "manifestVersion": 1,
  "id": "org.andaru.tailscale",
  "slug": "tailscale",
  "name": "Tailscale",
  "version": "1.0.0",
  "application": { "name": "Tailscale", "version": "1.82.0" },
  "requires": [{ "name": "andaru.extension-api", "version": ">=1.0.0 <2.0.0" }],
  "permissions": [
    { "name": "view", "title": "View status" },
    { "name": "configure", "title": "Edit settings" },
    { "name": "apply", "title": "Apply settings" },
    { "name": "manage", "title": "Start and stop" }
  ],
  "runtime": {
    "image": "ghcr.io/tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "network": "host",
    "profile": "network-interface",
    "capabilities": ["NET_ADMIN"],
    "devices": ["/dev/net/tun"],
    "env": { "TS_USERSPACE": "false", "TS_EXTRA_ARGS": "--netfilter-mode=off" }
  },
  "interfaces": [
    { "name": "tailscale0", "title": "Tailscale tunnel", "zoneRequired": true }
  ],
  "bus": {
    "publish": [{ "topic": "notification/addon.tailscale", "title": "Alerts" }],
    "subscribe": [{ "topic": "config/head", "title": "Config saves" }]
  },
  "properties": [
    {
      "name": "authKey",
      "type": "secret-reference",
      "title": "Auth key",
      "binding": { "type": "file", "name": "auth_key", "format": "raw", "env": "TS_AUTHKEY", "prefix": "file:" }
    },
    {
      "name": "hostname",
      "type": "string",
      "format": "hostname",
      "title": "Hostname",
      "binding": { "type": "environment", "name": "TS_HOSTNAME" }
    },
    {
      "name": "acceptRoutes",
      "type": "boolean",
      "default": false,
      "title": "Accept routes",
      "binding": { "type": "argument", "name": "--accept-routes", "style": "flag" }
    },
    {
      "name": "fallbackZone",
      "type": "zone-reference",
      "title": "Fallback zone"
    }
  ]
}`

func testCatalog(t *testing.T) *addon.Catalog {
	t.Helper()
	dir := t.TempDir()
	tsDir := filepath.Join(dir, "tailscale")
	if err := os.MkdirAll(tsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tsDir, "manifest.json"), []byte(testTailscaleCatalogManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := addon.LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func openAddonTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAddonConfigSecretsZonesBackupAndUninstall(t *testing.T) {
	ctx := t.Context()
	store := openAddonTestStore(t)
	store.SetCatalog(testCatalog(t))
	actor := authz.Actor{Kind: "system", ID: "root"}

	// Create a zone for tailscale0 and a second zone for fallbackZone.
	zRes, err := store.Save(ctx, actor, Changes{Operations: []Operation{
		{Op: "create", Collection: "zones", LocalID: "ts", Value: json.RawMessage(`{"name":"Tailscale"}`)},
		{Op: "create", Collection: "zones", LocalID: "fb", Value: json.RawMessage(`{"name":"Fallback"}`)},
	}})
	if err != nil {
		t.Fatalf("create zones: %v", err)
	}
	tsZone := zRes.Created["ts"]
	fbZone := zRes.Created["fb"]

	// Enable add-on without required zone for tailscale0 -> rejected by catalog validator.
	_, err = store.Save(ctx, actor, Changes{Operations: []Operation{{
		Op:         "create",
		Collection: "addons",
		Value:      json.RawMessage(`{"id":"org.andaru.tailscale","enabled":true,"settings":{"hostname":"gw"}}`),
	}}})
	if err == nil {
		t.Fatal("expected missing required zone error")
	}

	// Create add-on with scoped secret, zone binding, bus grant, and defaultable acceptRoutes.
	createBody := `{"id":"org.andaru.tailscale","enabled":true,"settings":{"hostname":"gw","fallbackZone":"` + fbZone + `","authKey":{"secret":"auth_key","set":"tskey-auth-SECRET-1","expectedVersion":"0"}},"interfaces":[{"name":"tailscale0","zone":"` + tsZone + `"}],"busGrants":{"publish":{"notification/addon.tailscale":"granted"},"subscribe":{"config/head":"denied"}}}`
	res, err := store.Save(ctx, actor, Changes{Operations: []Operation{{
		Op:         "create",
		Collection: "addons",
		Value:      json.RawMessage(createBody),
	}}})
	if err != nil {
		t.Fatalf("create addon: %v", err)
	}
	v1 := res.SavedVersion

	// Public snapshot never exposes secret value, shows metadata, and materializes default acceptRoutes=false.
	snap, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snap.Value), "tskey-auth-SECRET-1") {
		t.Fatal("secret value leaked in public snapshot")
	}
	if !strings.Contains(string(snap.Value), `"acceptRoutes":false`) {
		t.Fatalf("default acceptRoutes was not materialized: %s", snap.Value)
	}

	// RealizationInput carries AddonSecrets in its own namespace and keeps core Secrets clean.
	in1, err := store.RealizationInput(ctx, nil)
	if err != nil {
		t.Fatalf("RealizationInput: %v", err)
	}
	if got := in1.AddonSecrets["org.andaru.tailscale"]["auth_key"]; got != "tskey-auth-SECRET-1" {
		t.Fatalf("AddonSecrets = %+v", in1.AddonSecrets)
	}
	if _, leaked := in1.Secrets["auth_key"]; leaked {
		t.Fatal("addon secret leaked into core Secrets")
	}

	// Deleting either referenced zone must be rejected (both interface zone and zone-reference setting).
	for _, zID := range []string{tsZone, fbZone} {
		_, err = store.Save(ctx, actor, Changes{Operations: []Operation{{
			Op:              "delete",
			Collection:      "zones",
			ID:              zID,
			ExpectedVersion: &zRes.SavedVersion,
		}}})
		if err == nil {
			t.Fatalf("expected deleting referenced zone %s to fail", zID)
		}
	}

	// Updating add-on secret changes realization (SameRealization == false).
	replaceBody := `{"id":"org.andaru.tailscale","enabled":true,"settings":{"hostname":"gw","fallbackZone":"` + fbZone + `","authKey":{"secret":"auth_key","set":"tskey-auth-SECRET-2","expectedVersion":"` + string(v1) + `"}},"interfaces":[{"name":"tailscale0","zone":"` + tsZone + `"}],"busGrants":{"publish":{"notification/addon.tailscale":"granted"}}}`
	res2, err := store.Save(ctx, actor, Changes{Operations: []Operation{{
		Op:              "replace",
		Collection:      "addons",
		ID:              "org.andaru.tailscale",
		ExpectedVersion: &v1,
		Value:           json.RawMessage(replaceBody),
	}}})
	if err != nil {
		t.Fatalf("replace addon secret: %v", err)
	}
	in2, err := store.RealizationInput(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if SameRealization(in1, in2) {
		t.Fatal("SameRealization returned true after changing addon secret")
	}

	// Backup & restore with secrets preserves scoped addon secret; without secrets marks it unresolved and blocks apply.
	withSecrets, err := store.Export(ctx, true)
	if err != nil {
		t.Fatalf("export with secrets: %v", err)
	}
	withoutSecrets, err := store.Export(ctx, false)
	if err != nil {
		t.Fatalf("export without secrets: %v", err)
	}

	destWith := openAddonTestStore(t)
	if _, err := destWith.Restore(ctx, actor, "0", RestoreDocuments{
		Configuration:        withSecrets.Configuration,
		ConfigurationSecrets: withSecrets.ConfigurationSecrets,
	}); err != nil {
		t.Fatalf("Restore with secrets: %v", err)
	}
	destIn, err := destWith.RealizationInput(ctx, nil)
	if err != nil {
		t.Fatalf("destWith RealizationInput: %v", err)
	}
	if got := destIn.AddonSecrets["org.andaru.tailscale"]["auth_key"]; got != "tskey-auth-SECRET-2" {
		t.Fatalf("restored addon secret = %q", got)
	}

	destNoSec := openAddonTestStore(t)
	if _, err := destNoSec.Restore(ctx, actor, "0", RestoreDocuments{
		Configuration: withoutSecrets.Configuration,
	}); err != nil {
		t.Fatalf("Restore without secrets: %v", err)
	}
	if _, err := destNoSec.RealizationInput(ctx, nil); err == nil {
		t.Fatal("expected RealizationInput to fail while enabled add-on has unresolved secret")
	}

	// Uninstall (delete) drops the add-on and its scoped secrets from the new saved head.
	v2 := res2.SavedVersion
	if _, err := store.Save(ctx, actor, Changes{Operations: []Operation{{
		Op:              "delete",
		Collection:      "addons",
		ID:              "org.andaru.tailscale",
		ExpectedVersion: &v2,
	}}}); err != nil {
		t.Fatalf("delete addon: %v", err)
	}
	afterDel, err := store.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(afterDel.ConfigurationSecrets), "addon:org.andaru.tailscale") {
		t.Fatalf("addon secret scope survived uninstall: %s", afterDel.ConfigurationSecrets)
	}
}

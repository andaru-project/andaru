package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

const certSyncManifest = `{
  "manifestVersion": 1, "id": "org.andaru.proxy", "slug": "proxy", "name": "Proxy", "version": "1.0.0",
  "application": { "name": "Proxy", "version": "1.0.0" },
  "requires": [{ "name": "andaru.extension-api", "version": ">=1.0.0 <2.0.0" }],
  "permissions": [{ "name": "view", "title": "View" }],
  "runtime": { "image": "ghcr.io/example/proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "network": "host", "profile": "unprivileged" },
  "properties": [{ "name": "tls", "type": "certificate-reference", "title": "TLS", "binding": { "type": "certificate", "name": "web", "format": "bundle" } }]
}`

// cert-sync stages what the live generation's version binds, with the head's current certificates, and spawns
// realize again only when that or the live generation changes.
func TestCertSyncStagesLiveBindings(t *testing.T) {
	stateDir, runtimeDir := t.TempDir(), t.TempDir()
	catalogDir := filepath.Join(t.TempDir(), "proxy")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil || os.WriteFile(filepath.Join(catalogDir, "manifest.json"), []byte(certSyncManifest), 0o644) != nil {
		t.Fatal("write catalog")
	}
	catalog, err := addon.LoadCatalog(filepath.Dir(catalogDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := generation.Dir(stateDir)
	container := "/run/containers/systemd/andaru-addon-proxy.container"
	live := func(version string) {
		id, err := generation.Create(dir, generation.Manifest{SavedVersion: version, Files: []generation.FileEntry{{Path: container, Mode: "0600"}}}, map[string][]byte{container: []byte("[Container]\n")})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"confirmed": id, "lastKnownGood": nil, "pending": nil, "live": id})
		if err := os.WriteFile(filepath.Join(dir, "pointers.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	live("5")
	var asked []config.Version
	key := "KEY_ONE"
	bindings := func(_ context.Context, v config.Version) ([]config.CertificateConsumer, map[string]config.CertificateMaterial, error) {
		asked = append(asked, v)
		return []config.CertificateConsumer{
			{Kind: "management", CertificateID: "crt_a"},
			{Kind: "addon", AddonID: "org.andaru.proxy", Setting: "tls", CertificateID: "crt_a"},
			{Kind: "addon", AddonID: "org.andaru.proxy", Setting: "tls", CertificateID: "crt_unresolved"},
		}, map[string]config.CertificateMaterial{
			"crt_a":          {CertPEM: "CERT\n", ChainPEM: "CHAIN\n", Key: key},
			"crt_unresolved": {Unresolved: true},
		}, nil
	}
	var placed []generation.CertSync
	c := newCertSync(stateDir, runtimeDir, catalog, bindings, func(_ context.Context, file string) error {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("staged file %v %v", info, err)
		}
		raw, _ := os.ReadFile(file)
		var p generation.CertSync
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		placed = append(placed, p)
		return nil
	})
	if err := c.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "5" {
		t.Fatalf("bindings read at %v, want the live generation's version 5", asked)
	}
	if len(placed) != 1 || placed[0].Web == nil || placed[0].Web.Cert != "CERT\nCHAIN\n" || placed[0].Web.Key != key ||
		placed[0].Addons["proxy"]["web.pem"] != "CERT\nCHAIN\nKEY_ONE\n" {
		t.Fatalf("placed %+v", placed)
	}
	if matches, _ := filepath.Glob(filepath.Join(runtimeDir, "cert-sync-*.json")); len(matches) != 0 {
		t.Fatalf("staged payload left behind: %v", matches)
	}
	if err := c.sync(context.Background()); err != nil || len(placed) != 1 {
		t.Fatalf("unchanged sync spawned realize: %v %d", err, len(placed))
	}
	key = "KEY_TWO" // renewed in the head
	if err := c.sync(context.Background()); err != nil || len(placed) != 2 || !strings.Contains(placed[1].Web.Key, "KEY_TWO") {
		t.Fatalf("renewal not placed: %v %+v", err, placed)
	}
	live("6") // an activation places its own copies, which are replaced even when nothing else changed
	if err := c.sync(context.Background()); err != nil || len(placed) != 3 || asked[len(asked)-1] != "6" {
		t.Fatalf("activation not followed: %v %d %v", err, len(placed), asked)
	}
	if placed[2].ExpectedLive == nil || *placed[2].ExpectedLive == 0 {
		t.Fatalf("expected ExpectedLive set on payload, got %+v", placed[2])
	}
}

func TestCertSyncKeepsWebWhenDeletedInHead(t *testing.T) {
	stateDir, runtimeDir := t.TempDir(), t.TempDir()
	dir := generation.Dir(stateDir)
	id, err := generation.Create(dir, generation.Manifest{SavedVersion: "5"}, map[string][]byte{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"confirmed": id, "lastKnownGood": nil, "pending": nil, "live": id})
	if err := os.WriteFile(filepath.Join(dir, "pointers.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	bindings := func(_ context.Context, _ config.Version) ([]config.CertificateConsumer, map[string]config.CertificateMaterial, error) {
		return []config.CertificateConsumer{{Kind: "management", CertificateID: "crt_deleted"}}, map[string]config.CertificateMaterial{}, nil
	}
	var got generation.CertSync
	c := newCertSync(stateDir, runtimeDir, nil, bindings, func(_ context.Context, file string) error {
		b, _ := os.ReadFile(file)
		return json.Unmarshal(b, &got)
	})
	if err := c.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !got.KeepWeb || got.Web != nil {
		t.Fatalf("expected KeepWeb=true, Web=nil when cert deleted in head; got %+v", got)
	}
	// invalidate clears c.last so the next sync re-places files even with an unchanged live generation.
	calls := 0
	c.realize = func(context.Context, string) error { calls++; return nil }
	c.invalidate()
	if err := c.sync(context.Background()); err != nil || calls != 1 {
		t.Fatalf("invalidate did not force re-sync: err=%v calls=%d", err, calls)
	}
}

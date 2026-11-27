package addon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validTailscaleManifest = `{
  "manifestVersion": 1,
  "id": "org.andaru.tailscale",
  "slug": "tailscale",
  "name": "Tailscale",
  "version": "1.0.0",
  "application": { "name": "Tailscale", "version": "1.82.0" },
  "requires": [{ "name": "andaru.extension-api", "version": ">=1.0.0 <2.0.0" }],
  "permissions": [
    { "name": "view", "title": "View status" },
    { "name": "configure", "title": { "i18n": "perm.configure" } },
    { "name": "apply", "title": "Apply configuration" },
    { "name": "manage", "title": "Start and stop Tailscale" },
    { "name": "logs", "title": "Read logs" }
  ],
  "runtime": {
    "image": "ghcr.io/tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "network": "host",
    "profile": "network-interface",
    "capabilities": ["NET_ADMIN"],
    "devices": ["/dev/net/tun"],
    "env": {
      "TS_USERSPACE": "false",
      "TS_EXTRA_ARGS": "--netfilter-mode=off"
    }
  },
  "interfaces": [
    { "name": "tailscale0", "title": "Tailscale tunnel", "zoneRequired": true }
  ],
  "storage": [
    { "name": "state", "purpose": "state", "mountPath": "/var/lib/tailscale", "backup": false }
  ],
  "bus": {
    "publish": [
      { "topic": "notification/addon.tailscale", "title": "Tailscale alerts" }
    ],
    "subscribe": [
      { "topic": "config/head", "title": "Configuration saves" }
    ]
  },
  "logs": [
    { "name": "application", "title": "Application", "permission": "logs", "source": { "type": "runtime" }, "format": "text" }
  ],
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
      "title": "Node hostname",
      "binding": { "type": "environment", "name": "TS_HOSTNAME" }
    },
    {
      "name": "advertiseRoutes",
      "type": "string-list",
      "itemFormat": "cidr",
      "title": "Advertised routes",
      "binding": { "type": "environment", "name": "TS_ROUTES", "encoding": { "mode": "join", "separator": "," } }
    },
    {
      "name": "acceptRoutes",
      "type": "boolean",
      "default": false,
      "title": "Accept routes",
      "binding": { "type": "argument", "name": "--accept-routes", "style": "flag" }
    },
    {
      "name": "logLevel",
      "type": "enum",
      "options": [
        { "value": "INFO", "title": "Info" },
        { "value": "DEBUG", "title": "Debug" }
      ],
      "minItems": 1,
      "maxItems": 1,
      "default": ["INFO"],
      "title": "Log level",
      "binding": { "type": "argument", "name": "--log-level" }
    }
  ]
}`

func TestParseManifestValid(t *testing.T) {
	m, err := ParseManifest([]byte(validTailscaleManifest))
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}
	if m.ID != "org.andaru.tailscale" || m.Slug != "tailscale" {
		t.Fatalf("identity = %q/%q", m.ID, m.Slug)
	}
	if m.Runtime.StopTimeoutSeconds != 5 || m.Runtime.SettingsActivation != "recreate" {
		t.Fatalf("defaults not applied: %+v", m.Runtime)
	}
}

func TestParseManifestRejections(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantErr string
		isVer   bool
	}{
		{
			name:   "unsupported manifest version precedes unknown fields",
			mutate: func(s string) string { return `{"manifestVersion": 2, "futureField": true}` },
			isVer:  true,
		},
		{
			name:   "incompatible extension API version",
			mutate: func(s string) string { return strings.Replace(s, `">=1.0.0 <2.0.0"`, `">=2.0.0"`, 1) },
			isVer:  true,
		},
		{
			name:    "unknown field with json pointer",
			mutate:  func(s string) string { return strings.Replace(s, `"version": "1.0.0",`, `"version": "1.0.0", "bogus": 1,`, 1) },
			wantErr: "/bogus",
		},
		{
			name:    "duplicate json key rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"slug": "tailscale",`, `"slug": "tailscale", "slug": "other",`, 1) },
			wantErr: "invalid JSON",
		},
		{
			name:    "tag-only image rejected",
			mutate:  func(s string) string { return strings.Replace(s, `@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef`, `:latest`, 1) },
			wantErr: "/runtime/image",
		},
		{
			name:    "bridge networking rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"network": "host"`, `"network": "bridge"`, 1) },
			wantErr: "/runtime/network",
		},
		{
			name:    "reserved ANDARU_ env rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"TS_HOSTNAME"`, `"ANDARU_HOSTNAME"`, 1) },
			wantErr: "ANDARU_",
		},
		{
			name:    "duplicate env binding collision with runtime.env",
			mutate:  func(s string) string { return strings.Replace(s, `"TS_HOSTNAME"`, `"TS_USERSPACE"`, 1) },
			wantErr: "already bound",
		},
		{
			name:    "bus subscribe wildcard bypass rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"config/head"`, `"#"`, 1) },
			wantErr: "first topic level",
		},
		{
			name:    "bus subscribe notification/# rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"config/head"`, `"notification/+"`, 1) },
			wantErr: "forbidden namespace",
		},
		{
			name:    "bus publish forged notification source rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"notification/addon.tailscale"`, `"notification/engine"`, 1) },
			wantErr: "forbidden namespace",
		},
		{
			name:    "reserved interface en* rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"tailscale0"`, `"ens99"`, 1) },
			wantErr: "/interfaces/0/name",
		},
		{
			name:    "reserved certificate-reference rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"type": "secret-reference"`, `"type": "certificate-reference"`, 1) },
			wantErr: "reserved",
		},
		{
			name:    "stopTimeoutSeconds above 10 rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"network": "host",`, `"network": "host", "stopTimeoutSeconds": 15,`, 1) },
			wantErr: "/runtime/stopTimeoutSeconds",
		},
		{
			name:    "unprivileged profile cannot request capabilities",
			mutate:  func(s string) string { return strings.Replace(s, `"profile": "network-interface"`, `"profile": "unprivileged"`, 1) },
			wantErr: "/runtime/capabilities",
		},
		{
			name:    "null in manifest rejected",
			mutate:  func(s string) string { return strings.Replace(s, `"backup": false`, `"backup": null`, 1) },
			wantErr: "null",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.mutate(validTailscaleManifest)))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.isVer && !errors.Is(err, ErrUnsupportedManifestVersion) {
				t.Fatalf("got %v, want ErrUnsupportedManifestVersion", err)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateSettingsDefaultsNullAndConstraints(t *testing.T) {
	m, err := ParseManifest([]byte(validTailscaleManifest))
	if err != nil {
		t.Fatal(err)
	}

	// Empty input materializes defaults (acceptRoutes=false, logLevel=["INFO"]).
	out, errs := ValidateSettings(m, map[string]any{}, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if out["acceptRoutes"] != false {
		t.Fatalf("acceptRoutes = %v, want false", out["acceptRoutes"])
	}
	if got, ok := out["logLevel"].([]any); !ok || len(got) != 1 || got[0] != "INFO" {
		t.Fatalf("logLevel = %#v, want [INFO]", out["logLevel"])
	}

	// Explicit null rejected; unknown setting rejected; invalid CIDR rejected; duplicate CIDR rejected.
	_, errs = ValidateSettings(m, map[string]any{
		"hostname":        nil,
		"unknownKey":      "x",
		"advertiseRoutes": []any{"192.168.10.0/24", "192.168.10.0/24", "not-a-cidr"},
	}, nil)
	if len(errs) < 4 {
		t.Fatalf("expected >=4 errors, got %+v", errs)
	}

	// Valid settings with secret ref and CIDR list pass.
	out, errs = ValidateSettings(m, map[string]any{
		"hostname":        "andaru-gw",
		"advertiseRoutes": []any{"192.168.10.0/24", "fd00::/48"},
		"authKey":         map[string]any{"secret": "auth_key"},
	}, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if out["hostname"] != "andaru-gw" {
		t.Fatalf("hostname = %v", out["hostname"])
	}
}

func TestLoadCatalog(t *testing.T) {
	// Missing dir returns empty catalog.
	empty, err := LoadCatalog(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(empty.All()) != 0 {
		t.Fatalf("missing dir: %v %+v", err, empty)
	}

	dir := t.TempDir()
	tsDir := filepath.Join(dir, "tailscale")
	if err := os.MkdirAll(tsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tsDir, "manifest.json"), []byte(validTailscaleManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.All()) != 1 {
		t.Fatalf("len = %d", len(cat.All()))
	}
	if m, ok := cat.ByID("org.andaru.tailscale"); !ok || m.Slug != "tailscale" {
		t.Fatalf("ByID: %v %+v", ok, m)
	}
	if m, ok := cat.BySlug("tailscale"); !ok || m.ID != "org.andaru.tailscale" {
		t.Fatalf("BySlug: %v %+v", ok, m)
	}

	// Duplicate interface across two add-ons fails catalog load.
	dupDir := filepath.Join(dir, "other")
	if err := os.MkdirAll(dupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(validTailscaleManifest, `"org.andaru.tailscale"`, `"org.andaru.other"`, 1)
	other = strings.Replace(other, `"slug": "tailscale"`, `"slug": "other"`, 1)
	other = strings.Replace(other, `"notification/addon.tailscale"`, `"notification/addon.other"`, 1)
	if err := os.WriteFile(filepath.Join(dupDir, "manifest.json"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(dir); err == nil || !strings.Contains(err.Error(), "tailscale0") {
		t.Fatalf("expected duplicate interface error, got %v", err)
	}
}

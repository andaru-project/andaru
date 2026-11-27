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
	if m.Runtime.StopTimeoutSeconds == nil || *m.Runtime.StopTimeoutSeconds != 5 || m.Runtime.SettingsActivation != "recreate" {
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

func TestManifestCaseCollisionAndWebUIOrigin(t *testing.T) {
	// Storage names differing only by case collide on ANDARU_STORAGE_<UPPER_NAME>.
	dupStorage := strings.Replace(validTailscaleManifest,
		`{ "name": "state", "purpose": "state", "mountPath": "/var/lib/tailscale", "backup": false }`,
		`{ "name": "state", "purpose": "state", "mountPath": "/var/lib/tailscale", "backup": false }, { "name": "STATE", "purpose": "data", "mountPath": "/var/lib/other", "backup": false }`, 1)
	if _, err := ParseManifest([]byte(dupStorage)); err == nil || !strings.Contains(err.Error(), "ANDARU_STORAGE_STATE") {
		t.Fatalf("expected storage env collision, got %v", err)
	}

	// Secret file names differing only by case collide on ANDARU_SECRET_<UPPER_NAME>_FILE.
	dupSecret := strings.Replace(validTailscaleManifest,
		`"binding": { "type": "file", "name": "auth_key", "format": "raw", "env": "TS_AUTHKEY", "prefix": "file:" }`,
		`"binding": { "type": "file", "name": "auth_key", "format": "raw", "env": "TS_AUTHKEY", "prefix": "file:" } }, { "name": "secondKey", "type": "secret-reference", "title": "Second", "binding": { "type": "file", "name": "AUTH_KEY" }`, 1)
	if _, err := ParseManifest([]byte(dupSecret)); err == nil || !strings.Contains(err.Error(), "ANDARU_SECRET_AUTH_KEY_FILE") {
		t.Fatalf("expected secret file env collision, got %v", err)
	}

	// WebUI without hostnameSetting is rejected so add-on WebUI never shares Andaru's host.
	noHostWebUI := strings.Replace(validTailscaleManifest,
		`"logs": [`,
		`"endpoints": [{ "name": "ui", "protocol": "https", "port": 8443, "exposure": "managed" }], "webui": { "title": "UI", "permission": "view", "endpoint": "ui", "path": "/" }, "logs": [`, 1)
	if _, err := ParseManifest([]byte(noHostWebUI)); err == nil || !strings.Contains(err.Error(), "/webui/hostnameSetting") {
		t.Fatalf("expected missing hostnameSetting rejection, got %v", err)
	}
}

func TestManifestHardeningRegressions(t *testing.T) {
	// Registry with host:port accepted; unqualified image rejected.
	withPort := strings.Replace(validTailscaleManifest, "ghcr.io/tailscale/tailscale@sha256:", "registry.local:5000/org/tailscale@sha256:", 1)
	if _, err := ParseManifest([]byte(withPort)); err != nil {
		t.Fatalf("expected host:port registry to pass, got %v", err)
	}
	unqualified := strings.Replace(validTailscaleManifest, "ghcr.io/tailscale/tailscale@sha256:", "tailscale/tailscale@sha256:", 1)
	if _, err := ParseManifest([]byte(unqualified)); err == nil {
		t.Fatal("expected unqualified image without registry domain to be rejected")
	}

	// Explicit stopTimeoutSeconds: 0 rejected rather than defaulted.
	zeroStop := strings.Replace(validTailscaleManifest, `"network": "host",`, `"network": "host", "stopTimeoutSeconds": 0,`, 1)
	if _, err := ParseManifest([]byte(zeroStop)); err == nil || !strings.Contains(err.Error(), "stopTimeoutSeconds") {
		t.Fatalf("expected stopTimeoutSeconds=0 rejection, got %v", err)
	}

	// Newline in static runtime.env value rejected.
	nlEnv := strings.Replace(validTailscaleManifest, `"TS_USERSPACE": "false"`, `"TS_USERSPACE": "false\nEVIL=1"`, 1)
	if _, err := ParseManifest([]byte(nlEnv)); err == nil {
		t.Fatal("expected newline in runtime.env to be rejected")
	}

	// Storage mountPath under /run/andaru rejected.
	reservedMount := strings.Replace(validTailscaleManifest, `"/var/lib/tailscale"`, `"/run/andaru/secrets"`, 1)
	if _, err := ParseManifest([]byte(reservedMount)); err == nil || !strings.Contains(err.Error(), "/run/andaru") {
		t.Fatalf("expected /run/andaru storage mount rejection, got %v", err)
	}

	// Reserved "number" type rejected (Andaru canonical JSON profile is integer-only).
	numProp := strings.Replace(validTailscaleManifest, `"type": "boolean",`, `"type": "number",`, 1)
	if _, err := ParseManifest([]byte(numProp)); err == nil || !strings.Contains(err.Error(), "integer-only") {
		t.Fatalf("expected reserved number type rejection, got %v", err)
	}

	// Settings hostile inputs: NUL byte, newline in env-bound setting, IPv6 zone ID.
	m, err := ParseManifest([]byte(`{
	  "manifestVersion": 1, "id": "org.test.probe", "slug": "probe", "name": "Probe", "version": "1.0.0",
	  "application": {"name":"Probe","version":"1"}, "requires": [{"name":"andaru.extension-api","version":"1.0.0"}],
	  "permissions": [{"name":"view","title":"View"}],
	  "runtime": {"image":"ghcr.io/org/probe@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","network":"host"},
	  "properties": [
	    {"name":"note","type":"string","title":"Note","binding":{"type":"environment","name":"NOTE"}},
	    {"name":"addr","type":"string","format":"ipv6","title":"Addr"}
	  ]
	}`))
	if err != nil {
		t.Fatalf("parse probe manifest: %v", err)
	}
	if _, errs := ValidateSettings(m, map[string]any{"note": "a\nB=1"}, nil); len(errs) == 0 {
		t.Fatal("expected newline in env-bound string setting to be rejected")
	}
	if _, errs := ValidateSettings(m, map[string]any{"note": "a\x00b"}, nil); len(errs) == 0 {
		t.Fatal("expected NUL byte in string setting to be rejected")
	}
	if _, errs := ValidateSettings(m, map[string]any{"addr": "fe80::1%eth0"}, nil); len(errs) == 0 {
		t.Fatal("expected IPv6 zone ID to be rejected")
	}
}

package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/host"
)

const renderTestTailscaleDigest = "ghcr.io/tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const renderTestTailscaleManifest = `{
  "manifestVersion": 1,
  "id": "org.andaru.tailscale",
  "slug": "tailscale",
  "name": "Tailscale",
  "version": "1.0.0",
  "application": { "name": "Tailscale", "version": "1.82.0" },
  "requires": [{ "name": "andaru.extension-api", "version": ">=1.0.0 <2.0.0" }],
  "permissions": [{ "name": "view", "title": "View" }],
  "runtime": {
    "image": "` + renderTestTailscaleDigest + `",
    "network": "host",
    "profile": "network-interface",
    "capabilities": ["NET_ADMIN"],
    "devices": ["/dev/net/tun"],
    "env": { "TS_USERSPACE": "false", "TS_EXTRA_ARGS": "--netfilter-mode=off" },
    "settingsActivation": "recreate",
    "stopTimeoutSeconds": 5
  },
  "interfaces": [
    { "name": "tailscale0", "title": "Tailscale tunnel", "zoneRequired": true }
  ],
  "storage": [
    { "name": "state", "purpose": "state", "mountPath": "/var/lib/tailscale", "backup": false }
  ],
  "endpoints": [
    { "name": "health", "protocol": "http", "port": 9002, "portSetting": "/healthPort", "exposure": "internal" }
  ],
  "health": {
    "endpoint": "health",
    "path": "/healthz",
    "expectedStatus": 200,
    "timeoutSeconds": 2,
    "startPeriodSeconds": 5
  },
  "bus": {
    "publish": [{ "topic": "notification/addon.tailscale", "title": "Alerts" }],
    "subscribe": [{ "topic": "config/head", "title": "Config" }]
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
      "name": "advertiseRoutes",
      "type": "string-list",
      "itemFormat": "cidr",
      "title": "Routes",
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
      "name": "tags",
      "type": "string-list",
      "title": "Tags",
      "binding": { "type": "argument", "name": "--tag", "encoding": { "mode": "repeat" } }
    },
    {
      "name": "healthPort",
      "type": "integer",
      "minimum": 1,
      "maximum": 65535,
      "default": 9002,
      "title": "Health port",
      "binding": { "type": "environment", "name": "TS_HEALTH_PORT" }
    }
  ]
}`

func testAddonCatalog(t *testing.T, manifest string) *addon.Catalog {
	t.Helper()
	dir := t.TempDir()
	sub := filepath.Join(dir, "tailscale")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := addon.LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	return cat
}

func TestRenderAddonQuadletEnvSecretsAndNFTZone(t *testing.T) {
	cat := testAddonCatalog(t, renderTestTailscaleManifest)
	facts := allFacts
	facts.PodmanTool = true
	facts.StagedImages = []string{renderTestTailscaleDigest}

	decl := json.RawMessage(`{
		"links": [{"id":"lnk_lan","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"02:00:00:00:00:01"}],
		"segments": [{"id":"seg_lan","link":"lnk_lan","zone":"zon_lan","addressing":{"ipv4":{"mode":"static","addresses":["10.0.0.1/24"]},"ipv6":{"mode":"none"}}}],
		"zones": [{"id":"zon_lan"},{"id":"zon_vpn"}],
		"management": {"segment":"seg_lan"},
		"addons": [{
			"id": "org.andaru.tailscale",
			"enabled": true,
			"settings": {
				"authKey": {"secret":"auth_key"},
				"hostname": "gw-1",
				"advertiseRoutes": ["10.0.0.0/24", "10.0.1.0/24"],
				"acceptRoutes": true,
				"tags": ["tag:edge", "tag:$100% \"quoted\""],
				"healthPort": 9102
			},
			"interfaces": [{"name":"tailscale0","zone":"zon_vpn"}],
			"busGrants": {
				"publish": {"notification/addon.tailscale":"granted"},
				"subscribe": {"config/head":"denied"}
			}
		}]
	}`)

	res, err := Network(Input{
		Declaration:  decl,
		AddonSecrets: map[string]map[string]string{"org.andaru.tailscale": {"auth_key": "tskey-auth-secret-123"}},
		Catalog:      cat,
		Links:        []host.LinkStatus{{IfIndex: 2, Name: "enp1s0", LinkType: "ether", LinkAddress: "02:00:00:00:00:01", PermanentAddress: "02:00:00:00:00:01"}},
		Facts:        facts,
	})
	if err != nil {
		t.Fatalf("Network: %v", err)
	}
	if !res.Realizable {
		t.Fatalf("expected Realizable=true, findings=%+v", res.Findings)
	}

	byPath := map[string]File{}
	for _, f := range res.Files {
		byPath[f.Path] = f
	}

	// 1. Quadlet .container checks.
	ctr, ok := byPath["/run/containers/systemd/andaru-addon-tailscale.container"]
	if !ok {
		t.Fatalf("missing quadlet file; got paths: %v", res.Files)
	}
	if ctr.Mode != "0600" {
		t.Fatalf("container mode = %q, want 0600", ctr.Mode)
	}
	for _, want := range []string{
		"# andaru-health-url: http://127.0.0.1:9102/healthz 200",
		"# andaru-health-timeout: 2",
		"# andaru-health-start-period: 5",
		"ConditionPathExists=!/run/andaru-boot/safe-mode",
		"ConditionPathExists=!/var/lib/andaru/safe-mode-next",
		"ConditionPathExists=!/run/andaru-boot/addons-held",
		"ConditionKernelCommandLine=!andaru.safe=1",
		"Image=" + renderTestTailscaleDigest,
		"Pull=never",
		"Network=host",
		"NoNewPrivileges=true",
		"DropCapability=ALL",
		"StopTimeout=5",
		"AddCapability=NET_ADMIN",
		"AddDevice=/dev/net/tun",
		"Volume=/var/lib/andaru/addons/tailscale/state:/var/lib/tailscale:Z,U",
		"Volume=/run/andaru-addons/tailscale/secrets:/run/andaru/secrets:ro,Z,U",
		"Volume=/run/andaru-addons/tailscale/bus:/run/andaru/bus:ro,Z,U",
		"Volume=/run/andaru-addons/bus-socket:/run/andaru/bus-socket:ro,z",
		"EnvironmentFile=/run/andaru-addons/tailscale/andaru-addon-tailscale.env",
		`Exec="--accept-routes" "--tag" "tag:edge" "--tag" "tag:$$100%% \"quoted\""`,
		"TimeoutStopSec=15",
	} {
		if !strings.Contains(ctr.Content, want) {
			t.Errorf("container missing %q\nfull:\n%s", want, ctr.Content)
		}
	}

	// 2. Env file checks.
	envFile, ok := byPath["/run/andaru-addons/tailscale/andaru-addon-tailscale.env"]
	if !ok {
		t.Fatal("missing env file")
	}
	for _, want := range []string{
		"ANDARU_ADDON_ID=org.andaru.tailscale\n",
		"ANDARU_ADDON_SLUG=tailscale\n",
		"ANDARU_STORAGE_STATE=/var/lib/tailscale\n",
		"ANDARU_SECRET_AUTH_KEY_FILE=/run/andaru/secrets/andaru-addon-tailscale-auth_key.secret\n",
		"ANDARU_BUS_SOCKET=/run/andaru/bus-socket/bus.sock\n",
		"ANDARU_BUS_TOKEN_FILE=/run/andaru/bus/token\n",
		"TS_AUTHKEY=file:/run/andaru/secrets/andaru-addon-tailscale-auth_key.secret\n",
		"TS_EXTRA_ARGS=--netfilter-mode=off\n",
		"TS_HEALTH_PORT=9102\n",
		"TS_HOSTNAME=gw-1\n",
		"TS_ROUTES=10.0.0.0/24,10.0.1.0/24\n",
		"TS_USERSPACE=false\n",
	} {
		if !strings.Contains(envFile.Content, want) {
			t.Errorf("env file missing %q\nfull:\n%s", want, envFile.Content)
		}
	}

	// 3. Secret file checks (redacted preview, raw real value, 0400).
	secFile, ok := byPath["/run/andaru-addons/tailscale/secrets/andaru-addon-tailscale-auth_key.secret"]
	if !ok {
		t.Fatal("missing secret file")
	}
	if secFile.Mode != "0400" || !secFile.Secret {
		t.Fatalf("secret file mode=%q secret=%v", secFile.Mode, secFile.Secret)
	}
	if strings.Contains(secFile.Content, "tskey-auth-secret-123") {
		t.Fatalf("preview leaked secret: %q", secFile.Content)
	}
	if secFile.Real() != "tskey-auth-secret-123" {
		t.Fatalf("Real() = %q", secFile.Real())
	}

	// 4. nftables zone interface-name set includes tailscale0 specifically in zon_vpn.
	nft, ok := byPath[nftPath]
	if !ok {
		t.Fatal("missing nftables file")
	}
	if !strings.Contains(nft.Content, `set zon_vpn { type ifname; elements = { "tailscale0" }; }`) {
		t.Fatalf("nft missing tailscale0 in zon_vpn:\n%s", nft.Content)
	}
}

func TestRenderAddonBlockingFindingsAndNoBusMountWhenDenied(t *testing.T) {
	cat := testAddonCatalog(t, renderTestTailscaleManifest)
	baseDecl := func(enabled, unresolved bool) json.RawMessage {
		en := "false"
		if enabled {
			en = "true"
		}
		sec := `{"secret":"auth_key"}`
		if unresolved {
			sec = `{"secret":"auth_key","unresolved":true}`
		}
		return json.RawMessage(`{
			"links": [],
			"segments": [],
			"zones": [{"id":"zon_vpn"}],
			"addons": [{
				"id": "org.andaru.tailscale",
				"enabled": ` + en + `,
				"settings": {"authKey": ` + sec + `, "acceptRoutes": false, "tags": []},
				"interfaces": [{"name":"tailscale0","zone":"zon_vpn"}],
				"busGrants": {"publish": {"notification/addon.tailscale":"denied"}}
			}]
		}`)
	}

	// Disabled add-on renders no files and emits no staging/secret findings.
	disabledRes, err := Network(Input{Declaration: baseDecl(false, true), Catalog: cat, Facts: allFacts})
	if err != nil {
		t.Fatal(err)
	}
	if !disabledRes.Realizable {
		t.Fatalf("disabled add-on should stay realizable, got %+v", disabledRes.Findings)
	}
	for _, f := range disabledRes.Files {
		if strings.Contains(f.Path, "tailscale") {
			t.Fatalf("disabled add-on should not render %s", f.Path)
		}
	}

	// Enabled add-on with missing Podman, unstaged image, and unresolved secret emits all 3 blocking findings.
	blockedRes, err := Network(Input{Declaration: baseDecl(true, true), Catalog: cat, Facts: allFacts})
	if err != nil {
		t.Fatal(err)
	}
	if blockedRes.Realizable {
		t.Fatal("expected Realizable=false")
	}
	var codes []string
	for _, f := range blockedRes.Findings {
		codes = append(codes, f.Code)
	}
	for _, want := range []string{"TOOL_MISSING", "ADDON_IMAGE_NOT_STAGED", "SECRET_UNRESOLVED"} {
		if !slices.Contains(codes, want) {
			t.Errorf("missing finding %q in %v", want, codes)
		}
	}

	// Denied bus grants omit bus mounts and bus env vars; empty list and false flag omit Exec=.
	facts := allFacts
	facts.PodmanTool = true
	facts.StagedImages = []string{renderTestTailscaleDigest}
	okRes, err := Network(Input{
		Declaration:  baseDecl(true, false),
		AddonSecrets: map[string]map[string]string{"org.andaru.tailscale": {"auth_key": "ok"}},
		Catalog:      cat,
		Facts:        facts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !okRes.Realizable {
		t.Fatalf("expected Realizable=true, findings=%+v", okRes.Findings)
	}
	for _, f := range okRes.Files {
		if strings.HasSuffix(f.Path, ".container") {
			if strings.Contains(f.Content, "bus-socket") || strings.Contains(f.Content, "/run/andaru/bus") || strings.Contains(f.Content, "# andaru-bus-grants:") {
				t.Fatalf("denied bus grants should not mount bus:\n%s", f.Content)
			}
			if strings.Contains(f.Content, "Exec=") {
				t.Fatalf("false flag and empty list should not emit Exec=:\n%s", f.Content)
			}
		}
		if strings.HasSuffix(f.Path, ".env") {
			if strings.Contains(f.Content, "ANDARU_BUS_") {
				t.Fatalf("denied bus grants should not set ANDARU_BUS_*:\n%s", f.Content)
			}
		}
	}

	// Changing granted bus topics changes the rendered .container hash so apply restarts the add-on.
	h1, ok1 := addonGrantedBusHash(addonBusGrants{Publish: map[string]string{"notification/addon.tailscale": "granted"}})
	h2, ok2 := addonGrantedBusHash(addonBusGrants{Publish: map[string]string{"notification/addon.tailscale": "granted"}, Subscribe: map[string]string{"apply/+": "granted"}})
	if !ok1 || !ok2 || h1 == h2 {
		t.Fatalf("expected distinct non-empty bus grant hashes, got (%q,%v) vs (%q,%v)", h1, ok1, h2, ok2)
	}
}

func TestRenderAddonHardeningNewlineLiteralEnvEntrypointAndHealthCmd(t *testing.T) {
	// Test unsafe env value and unsafe argument value independently, asserting ADDON_SETTING_INVALID
	// at the exact property path and verifying no add-on files are emitted.
	cat := testAddonCatalog(t, renderTestTailscaleManifest)
	facts := allFacts
	facts.PodmanTool = true
	facts.StagedImages = []string{renderTestTailscaleDigest}
	for _, tc := range []struct {
		name     string
		settings string
		wantPath string
	}{
		{name: "env newline", settings: `{"hostname": "good\nINJECTED=evil"}`, wantPath: "/addons/0/settings/hostname"},
		{name: "arg newline", settings: `{"tags": ["ok\n[Service]\nExecStart=/bin/sh"]}`, wantPath: "/addons/0/settings/tags"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decl := json.RawMessage(`{"links":[],"segments":[],"zones":[{"id":"zon_vpn"}],"addons":[{"id":"org.andaru.tailscale","enabled":true,"settings":` + tc.settings + `}]}`)
			res, err := Network(Input{Declaration: decl, Catalog: cat, Facts: facts})
			if err != nil {
				t.Fatal(err)
			}
			if res.Realizable {
				t.Fatalf("expected Realizable=false for %s", tc.name)
			}
			found := slices.ContainsFunc(res.Findings, func(f Finding) bool {
				return f.Code == "ADDON_SETTING_INVALID" && f.Path == tc.wantPath
			})
			if !found {
				t.Fatalf("expected ADDON_SETTING_INVALID at %s, got %+v", tc.wantPath, res.Findings)
			}
			for _, f := range res.Files {
				if strings.Contains(f.Path, "tailscale") {
					t.Fatalf("must not emit add-on files on unsafe setting: %s", f.Path)
				}
			}
		})
	}

	// Unreadable staged-image index reports ADDON_STAGED_STATE_UNREADABLE instead of ADDON_IMAGE_NOT_STAGED.
	badFacts := facts
	badFacts.StagedImages = nil
	badFacts.StagedImagesErr = "decode staged add-on images: corrupt JSON"
	badRes, err := Network(Input{
		Declaration: json.RawMessage(`{"links":[],"segments":[],"zones":[],"addons":[{"id":"org.andaru.tailscale","enabled":true,"settings":{}}]}`),
		Catalog:     cat,
		Facts:       badFacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(badRes.Findings, func(f Finding) bool { return f.Code == "ADDON_STAGED_STATE_UNREADABLE" }) {
		t.Fatalf("expected ADDON_STAGED_STATE_UNREADABLE, got %+v", badRes.Findings)
	}
	if slices.ContainsFunc(badRes.Findings, func(f Finding) bool { return f.Code == "ADDON_IMAGE_NOT_STAGED" }) {
		t.Fatalf("must not emit ADDON_IMAGE_NOT_STAGED when staged index is unreadable: %+v", badRes.Findings)
	}

	// Literal Podman --env-file (spaces/empty unquoted), raw Entrypoint=, JSON array HealthCmd=, default env binding name.
	const customManifest = `{
	  "manifestVersion": 1, "id": "org.test.app", "slug": "app", "name": "App", "version": "1.0.0",
	  "application": {"name":"App","version":"1"}, "requires": [{"name":"andaru.extension-api","version":"1.0.0"}],
	  "permissions": [{"name":"view","title":"View"}],
	  "runtime": {
	    "image": "` + renderTestTailscaleDigest + `",
	    "network": "host",
	    "command": ["/usr/bin/app", "--serve"],
	    "env": {"APP_FLAGS": "--a --b", "EMPTY_VAR": ""}
	  },
	  "health": {"healthCmd": ["/usr/bin/app", "health", "--quiet"]},
	  "properties": [
	    {"name":"APP_MODE","type":"string","default":"prod","title":"Mode","binding":{"type":"environment"}}
	  ]
	}`
	dir := t.TempDir()
	sub := filepath.Join(dir, "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "manifest.json"), []byte(customManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	customCat, err := addon.LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog custom: %v", err)
	}
	customDecl := json.RawMessage(`{"links":[],"segments":[],"zones":[],"addons":[{"id":"org.test.app","enabled":true,"settings":{}}]}`)
	cres, err := Network(Input{Declaration: customDecl, Catalog: customCat, Facts: facts})
	if err != nil {
		t.Fatal(err)
	}
	if !cres.Realizable {
		t.Fatalf("expected custom Realizable=true, findings=%+v", cres.Findings)
	}
	byPath := map[string]File{}
	for _, f := range cres.Files {
		byPath[f.Path] = f
	}
	envContent := byPath["/run/andaru-addons/app/andaru-addon-app.env"].Content
	for _, want := range []string{"APP_FLAGS=--a --b\n", "EMPTY_VAR=\n", "APP_MODE=prod\n"} {
		if !strings.Contains(envContent, want) {
			t.Errorf("env missing literal %q:\n%s", want, envContent)
		}
	}
	ctrContent := byPath["/run/containers/systemd/andaru-addon-app.container"].Content
	for _, want := range []string{
		"Entrypoint=/usr/bin/app\n",
		`Exec="--serve"`,
		`HealthCmd=["/usr/bin/app","health","--quiet"]`,
	} {
		if !strings.Contains(ctrContent, want) {
			t.Errorf("container missing %q:\n%s", want, ctrContent)
		}
	}
}

package render

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/host"
)

// The shipped Tailscale catalog manifest loads cleanly, validates under default and customized settings,
// renders a confined host-network Quadlet with --netfilter-mode=off and all four safe-mode conditions,
// and binds tailscale0 into its declared Andaru firewall zone set.
func TestShippedTailscaleManifestLoadsAndRenders(t *testing.T) {
	catalogDir := filepath.Join("..", "..", "image", "files", "usr", "share", "andaru", "addons")
	cat, err := addon.LoadCatalog(catalogDir)
	if err != nil {
		t.Fatalf("LoadCatalog(%s): %v", catalogDir, err)
	}
	m, ok := cat.BySlug("tailscale")
	if !ok {
		t.Fatal("shipped catalog missing tailscale manifest")
	}
	if m.ID != "org.andaru.tailscale" {
		t.Fatalf("manifest id = %q", m.ID)
	}
	if len(m.Storage) != 1 || m.Storage[0].MountPath != "/var/lib/tailscale" || m.Storage[0].Backup {
		t.Fatalf("unexpected Tailscale storage spec: %+v", m.Storage)
	}

	// Validate default materialization ensures TS_EXTRA_ARGS always includes --netfilter-mode=off.
	materialized, errs := addon.ValidateSettings(m, nil, map[string]bool{"zone-vpn": true})
	if len(errs) != 0 {
		t.Fatalf("ValidateSettings(nil): %+v", errs)
	}
	rm, ok := addon.ToStringSlice(materialized["routingMode"])
	if !ok || len(rm) != 1 || rm[0] != "--netfilter-mode=off" {
		t.Fatalf("default routingMode = %#v, want [--netfilter-mode=off]", materialized["routingMode"])
	}
	// Reject attempts to bypass --netfilter-mode=off or supply arbitrary flags.
	for _, bad := range []map[string]any{
		{"routingMode": []string{}},
		{"routingMode": []string{"--netfilter-mode=on"}},
		{"routingMode": []string{"--netfilter-mode=off", "--netfilter-mode=off --accept-routes"}},
	} {
		if _, badErrs := addon.ValidateSettings(m, bad, map[string]bool{"zone-vpn": true}); len(badErrs) == 0 {
			t.Fatalf("ValidateSettings(%v) unexpectedly succeeded", bad)
		}
	}

	decl := []byte(`{
  "schemaVersion": 4,
  "links": [
    {"id": "link-lan", "name": "lan0", "kind": "ethernet", "permanentMac": "02:00:00:00:00:01", "mtu": 1500, "enabled": true}
  ],
  "zones": [
    {"id": "zone-lan", "name": "LAN", "inputDefault": "accept", "forwardDefault": "drop"},
    {"id": "zone-vpn", "name": "VPN", "inputDefault": "drop", "forwardDefault": "drop"}
  ],
  "segments": [
    {
      "id": "seg-lan", "name": "LAN", "link": "link-lan", "zone": "zone-lan", "role": "lan",
      "addressing": {"ipv4": {"mode": "static", "addresses": ["192.168.10.1/24"]}}
    }
  ],
  "management": {"segment": "seg-lan"},
  "addons": [
    {
      "id": "org.andaru.tailscale",
      "enabled": true,
      "settings": {
        "authKey": {"secret": "authKey"},
        "hostname": "andaru-edge",
        "advertiseRoutes": ["192.168.10.0/24"],
        "routingMode": ["--netfilter-mode=off --accept-routes"]
      },
      "interfaces": [{"name": "tailscale0", "zone": "zone-vpn"}]
    }
  ]
}`)

	facts := allFacts
	facts.PodmanTool = true
	facts.StagedImages = []string{m.Runtime.Image}

	res, err := Network(Input{
		Declaration: decl,
		Links: []host.LinkStatus{{
			IfIndex:          2,
			Name:             "ens18",
			LinkType:         "ether",
			MTU:              1500,
			OperState:        "UP",
			PermanentAddress: "02:00:00:00:00:01",
		}},
		Facts:   facts,
		Catalog: cat,
		AddonSecrets: map[string]map[string]string{
			"org.andaru.tailscale": {"authKey": "tskey-auth-test-value"},
		},
	})
	if err != nil {
		t.Fatalf("render.Network: %v", err)
	}
	if !res.Realizable {
		t.Fatalf("expected realizable render, findings=%+v", res.Findings)
	}

	files := map[string]File{}
	for _, f := range res.Files {
		files[f.Path] = f
	}

	quadlet, ok := files["/run/containers/systemd/andaru-addon-tailscale.container"]
	if !ok {
		t.Fatal("missing rendered Tailscale Quadlet unit")
	}
	for _, want := range []string{
		"ConditionPathExists=!/run/andaru-boot/safe-mode",
		"ConditionPathExists=!/var/lib/andaru/safe-mode-next",
		"ConditionPathExists=!/run/andaru-boot/addons-held",
		"ConditionKernelCommandLine=!andaru.safe=1",
		"Image=" + m.Runtime.Image,
		"Pull=never",
		"Network=host",
		"NoNewPrivileges=true",
		"DropCapability=ALL",
		"AddCapability=NET_ADMIN",
		"AddDevice=/dev/net/tun",
		"Volume=/var/lib/andaru/addons/tailscale/state:/var/lib/tailscale:Z,U",
		"Volume=/run/andaru-addons/tailscale/secrets:/run/andaru/secrets:ro,Z,U",
		"EnvironmentFile=/run/andaru-addons/tailscale/andaru-addon-tailscale.env",
		"StopTimeout=10",
		"TimeoutStopSec=20",
	} {
		if !strings.Contains(quadlet.Content, want) {
			t.Errorf("Quadlet missing %q:\n%s", want, quadlet.Content)
		}
	}
	if strings.Contains(quadlet.Content, "NET_RAW") || strings.Contains(quadlet.Content, "Privileged=") {
		t.Errorf("Quadlet over-privileged:\n%s", quadlet.Content)
	}

	envFile, ok := files["/run/andaru-addons/tailscale/andaru-addon-tailscale.env"]
	if !ok {
		t.Fatal("missing rendered Tailscale env file")
	}
	for _, want := range []string{
		"TS_USERSPACE=false",
		"TS_STATE_DIR=/var/lib/tailscale",
		"TS_ACCEPT_DNS=false",
		"TS_AUTH_ONCE=true",
		"TS_HOSTNAME=andaru-edge",
		"TS_ROUTES=192.168.10.0/24",
		"TS_EXTRA_ARGS=--netfilter-mode=off --accept-routes",
		"TS_AUTHKEY=file:/run/andaru/secrets/andaru-addon-tailscale-auth_key.secret",
	} {
		if !strings.Contains(envFile.Content, want) {
			t.Errorf("env file missing %q:\n%s", want, envFile.Content)
		}
	}

	secFile, ok := files["/run/andaru-addons/tailscale/secrets/andaru-addon-tailscale-auth_key.secret"]
	if !ok || !secFile.Secret || secFile.Mode != "0400" || secFile.Real() != "tskey-auth-test-value" {
		t.Fatalf("unexpected rendered secret file: ok=%v secret=%v mode=%q", ok, secFile.Secret, secFile.Mode)
	}
	if strings.Contains(secFile.Content, "tskey-auth-test-value") {
		t.Fatalf("secret preview leaked raw authKey: %q", secFile.Content)
	}

	nft, ok := files[nftPath]
	if !ok {
		t.Fatalf("missing rendered nftables file %s", nftPath)
	}
	if !strings.Contains(nft.Content, `set zone_vpn { type ifname; elements = { "tailscale0" }; }`) {
		t.Errorf("nftables ruleset missing tailscale0 in zone_vpn:\n%s", nft.Content)
	}

	// Disabled Tailscale must emit no Quadlet and omit tailscale0 from nftables.
	declDisabled := strings.Replace(
		string(decl),
		"\"id\": \"org.andaru.tailscale\",\n      \"enabled\": true",
		"\"id\": \"org.andaru.tailscale\",\n      \"enabled\": false",
		1,
	)
	resDisabled, err := Network(Input{
		Declaration: []byte(declDisabled),
		Links: []host.LinkStatus{{
			IfIndex:          2,
			Name:             "ens18",
			LinkType:         "ether",
			MTU:              1500,
			OperState:        "UP",
			PermanentAddress: "02:00:00:00:00:01",
		}},
		Facts:   facts,
		Catalog: cat,
	})
	if err != nil {
		t.Fatalf("render.Network(disabled): %v", err)
	}
	if !resDisabled.Realizable {
		t.Fatalf("expected realizable render when disabled, findings=%+v", resDisabled.Findings)
	}
	for _, f := range resDisabled.Files {
		if strings.Contains(f.Path, "tailscale") {
			t.Errorf("disabled Tailscale unexpectedly rendered %s", f.Path)
		}
		if f.Path == nftPath && strings.Contains(f.Content, `"tailscale0"`) {
			t.Errorf("disabled Tailscale leaked tailscale0 into nftables:\n%s", f.Content)
		}
	}
}

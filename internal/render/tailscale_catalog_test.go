package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/host"
)

// The shipped Tailscale catalog manifest loads cleanly, validates under default and customized settings,
// renders a confined host-network Quadlet with --netfilter-mode=off and all four safe-mode conditions,
// binds tailscale0 into its declared Andaru firewall zone set, and fails closed on hostile inputs.
func TestShippedTailscaleManifestLoadsAndRenders(t *testing.T) {
	const (
		defaultRouting = "--netfilter-mode=off --accept-routes=false --advertise-exit-node=false"
		acceptRouting  = "--netfilter-mode=off --accept-routes=true --advertise-exit-node=false"
	)

	catalogDir := filepath.Join("..", "..", "image", "files", "usr", "share", "andaru", "addons")
	if _, err := os.Stat(catalogDir); errors.Is(err, os.ErrNotExist) {
		t.Skip("no source tree:", err)
	}
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

	// Validate default materialization ensures TS_EXTRA_ARGS always includes --netfilter-mode=off and explicit =false flags.
	materialized, errs := addon.ValidateSettings(m, nil, nil, nil)
	if len(errs) != 0 {
		t.Fatalf("ValidateSettings(nil): %+v", errs)
	}
	rm, ok := addon.ToStringSlice(materialized["routingMode"])
	if !ok || len(rm) != 1 || rm[0] != defaultRouting {
		t.Fatalf("default routingMode = %#v, want [%s]", materialized["routingMode"], defaultRouting)
	}
	for _, bad := range []map[string]any{
		{"routingMode": []string{}},
		{"routingMode": []string{"--netfilter-mode=on"}},
		{"routingMode": []string{defaultRouting, acceptRouting}},
	} {
		if _, badErrs := addon.ValidateSettings(m, bad, nil, nil); len(badErrs) == 0 {
			t.Fatalf("ValidateSettings(%v) unexpectedly succeeded", bad)
		}
	}

	decl := []byte(fmt.Sprintf(`{
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
        "routingMode": [%q]
      },
      "interfaces": [{"name": "tailscale0", "zone": "zone-vpn"}]
    }
  ]
}`, acceptRouting))

	facts := allFacts
	facts.PodmanTool = true
	facts.StagedImages = []string{m.Runtime.Image}

	baseIn := Input{
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
	}

	res, err := Network(baseIn)
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
		"ConditionPathExists=!/run/andaru-boot/safe-mode\n",
		"ConditionPathExists=!/var/lib/andaru/safe-mode-next\n",
		"ConditionPathExists=!/run/andaru-boot/addons-held\n",
		"ConditionKernelCommandLine=!andaru.safe=1\n",
		"Image=" + m.Runtime.Image + "\n",
		"Pull=never\n",
		"Network=host\n",
		"NoNewPrivileges=true\n",
		"DropCapability=ALL\n",
		"AddCapability=NET_ADMIN\n",
		"AddDevice=/dev/net/tun\n",
		"Volume=/var/lib/andaru/addons/tailscale/state:/var/lib/tailscale:Z,U\n",
		"Volume=/run/andaru-addons/tailscale/secrets:/run/andaru/secrets:ro,Z,U\n",
		"EnvironmentFile=/run/andaru-addons/tailscale/andaru-addon-tailscale.env\n",
		"StopTimeout=10\n",
		"TimeoutStopSec=20\n",
	} {
		if !strings.Contains(quadlet.Content, want) {
			t.Errorf("Quadlet missing %q:\n%s", want, quadlet.Content)
		}
	}
	if strings.Contains(quadlet.Content, "NET_RAW") || strings.Contains(quadlet.Content, "Privileged=") {
		t.Errorf("Quadlet over-privileged:\n%s", quadlet.Content)
	}
	if strings.Contains(quadlet.Content, "tskey-auth-test-value") {
		t.Errorf("Quadlet leaked raw authKey:\n%s", quadlet.Content)
	}

	envFile, ok := files["/run/andaru-addons/tailscale/andaru-addon-tailscale.env"]
	if !ok {
		t.Fatal("missing rendered Tailscale env file")
	}
	for _, want := range []string{
		"TS_USERSPACE=false\n",
		"TS_STATE_DIR=/var/lib/tailscale\n",
		"TS_ACCEPT_DNS=false\n",
		"TS_HOSTNAME=andaru-edge\n",
		"TS_ROUTES=192.168.10.0/24\n",
		"TS_EXTRA_ARGS=" + acceptRouting + "\n",
		"TS_AUTHKEY=file:/run/andaru/secrets/andaru-addon-tailscale-auth_key.secret\n",
	} {
		if !strings.Contains(envFile.Content, want) {
			t.Errorf("env file missing %q:\n%s", want, envFile.Content)
		}
	}
	if strings.Contains(envFile.Content, "TS_AUTH_ONCE=") {
		t.Errorf("env file should not set TS_AUTH_ONCE (prevents routingMode updates on recreate):\n%s", envFile.Content)
	}
	if strings.Contains(envFile.Content, "tskey-auth-test-value") {
		t.Errorf("env file leaked raw authKey:\n%s", envFile.Content)
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

	// Interactive mode (omitted authKey + omitted routingMode) must materialize default TS_EXTRA_ARGS and omit secrets mount.
	inInteractive := baseIn
	inInteractive.AddonSecrets = nil
	inInteractive.Declaration = []byte(`{
  "schemaVersion": 4,
  "links": [{"id": "link-lan", "name": "lan0", "kind": "ethernet", "permanentMac": "02:00:00:00:00:01", "mtu": 1500, "enabled": true}],
  "zones": [{"id": "zone-lan", "name": "LAN"}, {"id": "zone-vpn", "name": "VPN"}],
  "segments": [{"id": "seg-lan", "name": "LAN", "link": "link-lan", "zone": "zone-lan", "addressing": {"ipv4": {"mode": "static", "addresses": ["192.168.10.1/24"]}}}],
  "management": {"segment": "seg-lan"},
  "addons": [{"id": "org.andaru.tailscale", "enabled": true, "interfaces": [{"name": "tailscale0", "zone": "zone-vpn"}]}]
}`)
	resInt, err := Network(inInteractive)
	if err != nil || !resInt.Realizable {
		t.Fatalf("interactive render err=%v realizable=%v findings=%+v", err, resInt.Realizable, resInt.Findings)
	}
	intFiles := map[string]File{}
	for _, f := range resInt.Files {
		intFiles[f.Path] = f
	}
	if !strings.Contains(intFiles["/run/andaru-addons/tailscale/andaru-addon-tailscale.env"].Content, "TS_EXTRA_ARGS="+defaultRouting+"\n") {
		t.Fatalf("interactive env missing default TS_EXTRA_ARGS:\n%s", intFiles["/run/andaru-addons/tailscale/andaru-addon-tailscale.env"].Content)
	}
	if strings.Contains(intFiles["/run/containers/systemd/andaru-addon-tailscale.container"].Content, "/run/andaru/secrets") {
		t.Fatal("interactive Tailscale should not mount secrets directory")
	}

	// Hostile routingMode, missing tailscale0 zone, and missing management.segment must fail closed with zero rendered Tailscale files.
	for name, badDecl := range map[string]string{
		"empty routingMode": `{
  "schemaVersion": 4,
  "links": [{"id": "link-lan", "name": "lan0", "kind": "ethernet", "permanentMac": "02:00:00:00:00:01", "mtu": 1500, "enabled": true}],
  "zones": [{"id": "zone-lan", "name": "LAN"}, {"id": "zone-vpn", "name": "VPN"}],
  "segments": [{"id": "seg-lan", "name": "LAN", "link": "link-lan", "zone": "zone-lan", "addressing": {"ipv4": {"mode": "static", "addresses": ["192.168.10.1/24"]}}}],
  "management": {"segment": "seg-lan"},
  "addons": [{"id": "org.andaru.tailscale", "enabled": true, "settings": {"routingMode": []}, "interfaces": [{"name": "tailscale0", "zone": "zone-vpn"}]}]
}`,
		"injected netfilter-mode=on array": `{
  "schemaVersion": 4,
  "links": [{"id": "link-lan", "name": "lan0", "kind": "ethernet", "permanentMac": "02:00:00:00:00:01", "mtu": 1500, "enabled": true}],
  "zones": [{"id": "zone-lan", "name": "LAN"}, {"id": "zone-vpn", "name": "VPN"}],
  "segments": [{"id": "seg-lan", "name": "LAN", "link": "link-lan", "zone": "zone-lan", "addressing": {"ipv4": {"mode": "static", "addresses": ["192.168.10.1/24"]}}}],
  "management": {"segment": "seg-lan"},
  "addons": [{"id": "org.andaru.tailscale", "enabled": true, "settings": {"routingMode": ["--netfilter-mode=on"]}, "interfaces": [{"name": "tailscale0", "zone": "zone-vpn"}]}]
}`,
		"injected netfilter-mode=on string": `{
  "schemaVersion": 4,
  "links": [{"id": "link-lan", "name": "lan0", "kind": "ethernet", "permanentMac": "02:00:00:00:00:01", "mtu": 1500, "enabled": true}],
  "zones": [{"id": "zone-lan", "name": "LAN"}, {"id": "zone-vpn", "name": "VPN"}],
  "segments": [{"id": "seg-lan", "name": "LAN", "link": "link-lan", "zone": "zone-lan", "addressing": {"ipv4": {"mode": "static", "addresses": ["192.168.10.1/24"]}}}],
  "management": {"segment": "seg-lan"},
  "addons": [{"id": "org.andaru.tailscale", "enabled": true, "settings": {"routingMode": "--netfilter-mode=on"}, "interfaces": [{"name": "tailscale0", "zone": "zone-vpn"}]}]
}`,
		"missing zone binding": `{
  "schemaVersion": 4,
  "links": [{"id": "link-lan", "name": "lan0", "kind": "ethernet", "permanentMac": "02:00:00:00:00:01", "mtu": 1500, "enabled": true}],
  "zones": [{"id": "zone-lan", "name": "LAN"}],
  "segments": [{"id": "seg-lan", "name": "LAN", "link": "link-lan", "zone": "zone-lan", "addressing": {"ipv4": {"mode": "static", "addresses": ["192.168.10.1/24"]}}}],
  "management": {"segment": "seg-lan"},
  "addons": [{"id": "org.andaru.tailscale", "enabled": true}]
}`,
		"missing management packet filter": `{
  "schemaVersion": 4,
  "links": [{"id": "link-lan", "name": "lan0", "kind": "ethernet", "permanentMac": "02:00:00:00:00:01", "mtu": 1500, "enabled": true}],
  "zones": [{"id": "zone-lan", "name": "LAN"}, {"id": "zone-vpn", "name": "VPN"}],
  "segments": [{"id": "seg-lan", "name": "LAN", "link": "link-lan", "zone": "zone-lan", "addressing": {"ipv4": {"mode": "static", "addresses": ["192.168.10.1/24"]}}}],
  "addons": [{"id": "org.andaru.tailscale", "enabled": true, "interfaces": [{"name": "tailscale0", "zone": "zone-vpn"}]}]
}`,
	} {
		t.Run(name, func(t *testing.T) {
			inBad := baseIn
			inBad.AddonSecrets = nil
			inBad.Declaration = []byte(badDecl)
			resBad, err := Network(inBad)
			if err != nil {
				t.Fatalf("render.Network(%s): %v", name, err)
			}
			if resBad.Realizable {
				t.Fatalf("case %q unexpectedly realizable (findings=%+v)", name, resBad.Findings)
			}
			for _, f := range resBad.Files {
				if strings.Contains(f.Path, "tailscale") {
					t.Errorf("case %q rendered Tailscale file %s", name, f.Path)
				}
			}
		})
	}

	// Disabled Tailscale must emit no Quadlet and omit tailscale0 from nftables.
	inDisabled := baseIn
	inDisabled.AddonSecrets = nil
	inDisabled.Declaration = []byte(strings.Replace(
		string(decl),
		"\"id\": \"org.andaru.tailscale\",\n      \"enabled\": true",
		"\"id\": \"org.andaru.tailscale\",\n      \"enabled\": false",
		1,
	))
	resDisabled, err := Network(inDisabled)
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

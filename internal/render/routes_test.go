package render

import (
	"strings"
	"testing"
)

// Enabled routes render into their segment's .network in destination, metric, gateway order; disabled ones and
// routes through targets networkd does not own render nowhere.
func TestStaticRoutes(t *testing.T) {
	result := render(t, change(t, fixture(t, "routes"), func(d map[string]any) {
		d["routes"] = append(d["routes"].([]any), map[string]any{"id": "rte_00000009-0000-4000-8000-000000000009", "name": "ovpn", "destination": "10.77.0.0/16",
			"target": map[string]any{"kind": "openvpn", "id": "ovp_00000001-0000-4000-8000-000000000001"}, "enabled": true})
	}))
	lan := networkFile(t, result, "ens19")
	want := "[Route]\nDestination=10.10.0.0/16\nGateway=192.0.2.254\n\n[Route]\nDestination=10.20.0.0/24\nGateway=192.0.2.254\nMetric=100\n\n" +
		"[Route]\nDestination=10.20.0.0/24\nGateway=192.0.2.253\nMetric=200\n\n[Route]\nDestination=2001:db8:100::/48\nGateway=fe80::1\n"
	if !strings.HasSuffix(lan, want) {
		t.Fatalf("lan routes:\n%s", lan)
	}
	if wan := networkFile(t, result, "ens18"); !strings.HasSuffix(wan, "\n[Route]\nDestination=198.51.100.0/24\n") {
		t.Fatalf("an interface route names neither gateway nor metric:\n%s", wan)
	}
	if g := golden(result); strings.Contains(g, "10.99.0.0/16") || strings.Contains(g, "10.77.0.0/16") {
		t.Fatalf("a disabled or VPN route rendered:\n%s", g)
	}
	// A route targeting a PPPoE segment renders into its .network file and records "/routes" in File.Sources.
	pppRes := render(t, change(t, fixture(t, "pppoe-ethernet"), func(d map[string]any) {
		segs := d["segments"].([]any)
		var wanSegID string
		for _, raw := range segs {
			m := raw.(map[string]any)
			if m["name"] == "wan" {
				wanSegID = m["id"].(string)
			}
		}
		d["routes"] = []any{map[string]any{"id": "rte_0000000a-0000-4000-8000-00000000000a", "name": "isp", "destination": "198.51.100.0/24",
			"target": map[string]any{"kind": "segment", "id": wanSegID}, "enabled": true}}
	}))
	var foundPPP bool
	for _, f := range pppRes.Files {
		if strings.HasSuffix(f.Path, ".network") && strings.Contains(f.Content, "Type=ppp") {
			foundPPP = true
			if !strings.Contains(f.Content, "Destination=198.51.100.0/24") {
				t.Fatalf("pppoe route missing:\n%s", f.Content)
			}
			hasRoutesSrc := false
			for _, src := range f.Sources {
				if src == "/routes" {
					hasRoutesSrc = true
				}
			}
			if !hasRoutesSrc {
				t.Fatalf("pppoe .network sources missing /routes: %v", f.Sources)
			}
		}
	}
	if !foundPPP {
		t.Fatal("ppp0 .network file not rendered")
	}
}

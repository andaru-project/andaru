package render

import (
	"strings"
	"testing"
)

// Key files hold the real key only in Real; preview content, findings and other files never show it.
func TestWireGuardKeysRedacted(t *testing.T) {
	in := fixture(t, "wireguard")
	result := render(t, in)
	for _, f := range result.Files {
		for _, name := range []string{"WG0_KEY", "WG1_KEY", "WG2_KEY", "WG0_LAPTOP_PSK"} {
			if strings.Contains(f.Content, in.Secrets[name]) {
				t.Fatalf("%s shows %s:\n%s", f.Path, name, f.Content)
			}
		}
		if strings.HasSuffix(f.Path, ".key") || strings.HasSuffix(f.Path, ".psk") {
			if !f.Secret || f.Mode != "0640" || f.Owner != "root:systemd-network" || f.Content != redacted+"\n" {
				t.Fatalf("%s: %+v", f.Path, f)
			}
		}
	}
	if f := fileEnding(t, result, "50-andaru-wg0.key"); f.Real() != in.Secrets["WG0_KEY"]+"\n" {
		t.Fatalf("real key: %q", f.Real())
	}
	if hasFile(result, "50-andaru-wg3.netdev") || hasFile(result, "50-andaru-wg3.key") {
		t.Fatal("a disabled interface rendered")
	}
	if g := golden(result); strings.Contains(g, "wgp_00000003") || strings.Contains(g, "10.60.0.0/16") || strings.Contains(g, "10.90.0.0/16") {
		t.Fatalf("a disabled peer or route rendered:\n%s", g)
	}
}

func TestWireGuardFindings(t *testing.T) {
	missing := fixture(t, "wireguard")
	delete(missing.Secrets, "WG1_KEY")
	if r := render(t, missing); r.Realizable || !hasFinding(r, "error", "SECRET_UNAVAILABLE", "/vpn/wireguard/1/privateKeyRef") {
		t.Fatalf("missing key: %+v", r.Findings)
	}
	taken := fixture(t, "wireguard")
	taken.Links = append(taken.Links, links(observed{Name: "wg0", Kind: "dummy"})...)
	r := render(t, taken)
	if r.Realizable || !hasFinding(r, "error", "NAME_COLLISION", "/vpn/wireguard/0/ifname") || hasFile(r, "50-andaru-wg0.netdev") {
		t.Fatalf("name taken: %+v", r.Findings)
	}
	if strings.Contains(fileEnding(t, r, nftPath).Content, `"wg0"`) {
		t.Fatal("an interface not rendered joined its zone")
	}
	// An interface of its own from an earlier apply is no collision.
	own := fixture(t, "wireguard")
	own.Links = append(own.Links, links(observed{Name: "wg0", Kind: "wireguard"})...)
	if r := render(t, own); !r.Realizable {
		t.Fatalf("own interface: %+v", r.Findings)
	}
	unfiltered := change(t, fixture(t, "wireguard"), func(d map[string]any) { delete(d, "management"); d["nat"] = map[string]any{} })
	if r := render(t, unfiltered); r.Realizable || !hasFinding(r, "error", "MANAGEMENT_REQUIRED", "/management") {
		t.Fatalf("without the filter: %+v", r.Findings)
	}
}

// Without uplinks the listen rule still loads: an empty set matches nothing.
func TestWireGuardListenWithoutUplinks(t *testing.T) {
	r := render(t, change(t, fixture(t, "wireguard"), func(d map[string]any) {
		addr := item(d, "segments", 0)["addressing"].(map[string]any)
		v4 := addr["ipv4"].(map[string]any)
		v4["mode"], v4["addresses"] = "static", []any{"198.51.100.2/24"}
		addr["ipv6"] = map[string]any{"mode": "none"}
	}))
	script := fileEnding(t, r, nftPath).Content
	if !strings.Contains(script, "set uplinks { type ifname; }") || !strings.Contains(script, "iifname @uplinks udp dport 51820 accept") {
		t.Fatalf("listen rule:\n%s", script)
	}
	// An IPv6-only uplink (IPv4 none, IPv6 gateway) joins @uplinks for default WireGuard listener rules.
	v6only := render(t, change(t, fixture(t, "wireguard"), func(d map[string]any) {
		addr := item(d, "segments", 0)["addressing"].(map[string]any)
		addr["ipv4"] = map[string]any{"mode": "none"}
		addr["ipv6"] = map[string]any{"mode": "static", "addresses": []any{"2001:db8:1::2/64"}, "gateway": "2001:db8:1::1"}
	}))
	if script := fileEnding(t, v6only, nftPath).Content; !strings.Contains(script, `set uplinks { type ifname; elements = { "ens18" }; }`) {
		t.Fatalf("ipv6-only uplink missing from @uplinks:\n%s", script)
	}
}

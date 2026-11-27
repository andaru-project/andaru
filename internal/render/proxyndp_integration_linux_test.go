//go:build integration

package render

import (
	"strings"
	"testing"
	"time"
)

// TestProxyNDPInNamespace is the design's probe with the rendered files: the provider's router 2001:db8:1::1 on the
// uplink's /64 reaches an inside host that holds 2001:db8:1::50/128 on the LAN, through the firewall's proxy entry and
// its /128 route, and gets no answer for an unrelated address of the /64. networkd is not in the lab, so the test does
// what it would with the rendered IPv6ProxyNDPAddress= and [Route] sections.
func TestProxyNDPInNamespace(t *testing.T) {
	l := newLab(t)
	wan, lan := "zon_00000001-0000-4000-8000-000000000001", "zon_00000002-0000-4000-8000-000000000002"
	in := change(t, fixture(t, "proxy-ndp"), func(d map[string]any) {
		segments := d["segments"].([]any)
		segments[0].(map[string]any)["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "static", "addresses": []any{"10.1.0.1/24"}, "gateway": "10.1.0.2"}
		segments[1].(map[string]any)["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "static", "addresses": []any{"10.2.0.1/24"}}
		d["virtualAddresses"] = d["virtualAddresses"].([]any)[1:2]
		d["rules"] = append(d["rules"].([]any), map[string]any{"id": "rul_00000009-0000-4000-8000-000000000009", "name": "to the host",
			"enabled": true, "action": "accept", "from": map[string]any{"zones": []any{wan}, "addresses": []any{}}, "to": map[string]any{"zones": []any{lan}, "addresses": []any{}}, "ports": []any{}, "log": false})
	})
	r := render(t, in)

	for ns, devs := range map[string][]string{l.fw: {"ens18", "ens19"}, l.wan: {"w0"}, l.lan: {"l0"}} {
		for _, dev := range append([]string{"all", "default"}, devs...) {
			l.must(ns, "sysctl", "-qw", "net.ipv6.conf."+dev+".accept_dad=0")
		}
	}
	l.must(l.fw, "sysctl", "-qw", "net.ipv6.conf.all.forwarding=1")
	l.must(l.fw, "ip", "addr", "add", "2001:db8:1::2/64", "dev", "ens18", "nodad")
	l.must(l.wan, "ip", "addr", "add", "2001:db8:1::1/64", "dev", "w0", "nodad")
	// The inside host: the address as a /128, its default route via the firewall's LAN link-local address.
	l.must(l.fw, "ip", "addr", "add", "fe80::1/64", "dev", "ens19", "nodad")
	l.must(l.lan, "ip", "addr", "add", "2001:db8:1::50/128", "dev", "l0", "nodad")
	l.must(l.lan, "ip", "-6", "route", "add", "default", "via", "fe80::1", "dev", "l0")

	proxies := 0
	for _, line := range strings.Split(fileEnding(t, r, "ens18.network").Content, "\n") {
		if a, ok := strings.CutPrefix(line, "IPv6ProxyNDPAddress="); ok {
			l.must(l.fw, "ip", "-6", "neigh", "add", "proxy", a, "dev", "ens18")
			proxies++
		}
	}
	routes := 0
	for _, line := range strings.Split(fileEnding(t, r, "ens19.network").Content, "\n") {
		if dst, ok := strings.CutPrefix(line, "Destination="); ok {
			l.must(l.fw, "ip", "-6", "route", "add", dst, "dev", "ens19")
			routes++
		}
	}
	if proxies != 1 || routes != 1 {
		t.Fatalf("%d proxy entries and %d routes rendered, want 1 each:\n%s", proxies, routes, golden(r))
	}
	// IPv6ProxyNDPAddress= implies IPv6ProxyNDP=yes.
	l.must(l.fw, "sysctl", "-qw", "net.ipv6.conf.ens18.proxy_ndp=1")
	l.load(in)

	// The kernel delays a proxied neighbour advertisement by up to proxy_delay, 0.8 s by default.
	reaches := func(ns, addr string) bool {
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); {
			if _, err := l.run(ns, "ping", "-c1", "-W2", addr); err == nil {
				return true
			}
		}
		return false
	}
	if !reaches(l.wan, "2001:db8:1::50") {
		out, _ := l.run(l.wan, "ip", "-6", "neigh")
		t.Errorf("the router did not reach the inside host: %s", out)
	}
	if !reaches(l.lan, "2001:db8:1::1") {
		t.Error("the inside host did not reach the router")
	}
	if l.pings(l.wan, "2001:db8:1::99") {
		t.Error("the router reached an unrelated address")
	}
	time.Sleep(time.Second)
	if out, _ := l.run(l.wan, "ip", "-6", "neigh", "show", "2001:db8:1::99", "dev", "w0"); strings.Contains(out, "lladdr") {
		t.Errorf("the firewall answered neighbour discovery for an unrelated address: %s", out)
	}
}

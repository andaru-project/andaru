//go:build integration

package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProxyARPInNamespace is the design's probe with the rendered files: the WAN neighbour reaches the inside hosts
// through a proxied address and a proxied subnet by 1:1 NAT, and gets no ARP answer for a LAN address it treats as
// on-link. networkd is not in the lab, so the test does what it would with the rendered andaru-vip routes and
// IPv4ProxyARP=; systemd-sysctl applies the image's medium_id file.
func TestProxyARPInNamespace(t *testing.T) {
	l := newLab(t)
	l.must(l.lan, "ip", "addr", "add", "10.2.0.10/24", "dev", "l0")
	wan, lan := "zon_00000001-0000-4000-8000-000000000001", "zon_00000002-0000-4000-8000-000000000002"
	in := change(t, fixture(t, "proxy-arp"), func(d map[string]any) {
		segments := d["segments"].([]any)
		segments[0].(map[string]any)["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "static", "addresses": []any{"10.1.0.1/24"}, "gateway": "10.1.0.2"}
		segments[1].(map[string]any)["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "static", "addresses": []any{"10.2.0.1/24"}}
		vips := d["virtualAddresses"].([]any)
		vips[0].(map[string]any)["address"], vips[1].(map[string]any)["address"] = "10.1.0.50", "10.1.0.56/29"
		nat := d["nat"].(map[string]any)
		nat["oneToOne"] = []any{
			map[string]any{"id": "oto_00000002-0000-4000-8000-000000000002", "name": "server", "enabled": true, "zone": wan, "external": "10.1.0.50", "internal": "10.2.0.2", "destination": []any{}},
			map[string]any{"id": "oto_00000003-0000-4000-8000-000000000003", "name": "block", "enabled": true, "zone": wan, "external": "10.1.0.56/29", "internal": "10.2.0.8/29", "destination": []any{}},
		}
		delete(nat, "portForwards")
		d["rules"] = append(d["rules"].([]any), map[string]any{"id": "rul_00000009-0000-4000-8000-000000000009", "name": "to the servers",
			"enabled": true, "action": "accept", "from": map[string]any{"zones": []any{wan}, "addresses": []any{}}, "to": map[string]any{"zones": []any{lan}, "addresses": []any{}}, "ports": []any{}, "log": false})
	})
	r := render(t, in)

	// The image's sysctl.d file, applied by systemd-sysctl as at boot, and to each interface as it appears as udev's
	// 99-systemd.rules does. CI runs the test binary away from the source tree and names the file.
	conf := os.Getenv("ANDARU_TEST_PROXY_ARP_SYSCTL")
	if conf == "" {
		conf = "../../packaging/sysctl.d/50-andaru-proxy-arp.conf"
	}
	// systemd-sysctl reads only absolute paths.
	conf, err := filepath.Abs(conf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(conf); err != nil {
		t.Fatal(err)
	}
	const sysctl = "/usr/lib/systemd/systemd-sysctl"
	if _, err := os.Stat(sysctl); err != nil {
		skipMissingTool(t, "medium_id is applied by "+sysctl)
	}
	l.must(l.fw, sysctl, conf)
	appeared := func(dev string) {
		l.t.Helper()
		l.must(l.fw, sysctl, "--prefix=/net/ipv4/conf/"+dev, conf)
	}
	l.must(l.fw, "ip", "link", "add", "andaru-vip", "type", "dummy")
	appeared("andaru-vip")
	// Created later, as a VPN would be, and not yet seen by udev: default alone keeps its routes from being proxied.
	l.must(l.fw, "ip", "link", "add", "later0", "type", "dummy")
	for dev, want := range map[string]string{"ens18": "1", "ens19": "1", "andaru-vip": "2", "later0": "1"} {
		if out, _ := l.run(l.fw, "sysctl", "-n", "net.ipv4.conf."+dev+".medium_id"); strings.TrimSpace(out) != want {
			t.Fatalf("%s medium_id %q, want %s", dev, out, want)
		}
	}
	l.must(l.fw, "ip", "link", "set", "andaru-vip", "up")
	l.must(l.fw, "ip", "link", "set", "later0", "up")
	l.must(l.fw, "ip", "route", "add", "10.3.0.0/24", "dev", "later0")
	for _, line := range strings.Split(fileEnding(t, r, "andaru-vip.network").Content, "\n") {
		if dst, ok := strings.CutPrefix(line, "Destination="); ok {
			l.must(l.fw, "ip", "route", "add", dst, "dev", "andaru-vip")
		}
	}
	if !strings.Contains(fileEnding(t, r, "ens18.network").Content, "IPv4ProxyARP=yes\n") {
		t.Fatalf("the WAN does not proxy ARP:\n%s", golden(r))
	}
	l.must(l.fw, "sysctl", "-qw", "net.ipv4.conf.ens18.proxy_arp=1")
	l.load(in)

	l.listen(l.lan, 80)
	if !l.connects(l.wan, "10.1.0.50", 80) {
		t.Error("the WAN did not reach the inside host through the proxied address")
	}
	if !l.connects(l.wan, "10.1.0.58", 80) {
		t.Error("the WAN did not reach the inside host through the proxied subnet")
	}
	// A neighbour that treats the LAN subnet, or one routed by a later interface, as on-link asks the WAN for a host
	// there: medium_id keeps it unanswered.
	for _, host := range []string{"10.2.0.2", "10.3.0.5"} {
		l.must(l.wan, "ip", "route", "add", host+"/32", "dev", "w0")
		l.pings(l.wan, host)
		time.Sleep(200 * time.Millisecond)
		if out, _ := l.run(l.wan, "ip", "neigh", "show", host, "dev", "w0"); strings.Contains(out, "lladdr") {
			t.Errorf("the WAN answered ARP for %s: %s", host, out)
		}
	}
}

//go:build integration

package render

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRoutedBlockInNamespace is the design's probe with the rendered files: the WAN neighbour routes the held block
// 203.0.113.0/29 to the firewall and reaches a service bound to an address inside it that no interface holds, both
// through a port forward to it and, once a rule lets it, directly. networkd is not in the lab, so the test applies the rendered andaru-vip
// addresses and local routes as networkd would.
func TestRoutedBlockInNamespace(t *testing.T) {
	l := newLab(t)
	wan := "zon_00000001-0000-4000-8000-000000000001"
	in := fixture(t, "routed-block")
	open := change(t, in, func(d map[string]any) {
		d["rules"] = append(d["rules"].([]any), map[string]any{"id": "rul_00000009-0000-4000-8000-000000000009", "name": "wan to the block",
			"enabled": true, "action": "accept", "from": map[string]any{"zones": []any{wan}, "addresses": []any{}}, "to": map[string]any{"zones": []any{"zon_firewall"}, "addresses": []any{}}, "ports": []any{}, "log": false})
	})
	r := render(t, in)
	l.must(l.fw, "ip", "link", "add", "andaru-vip", "type", "dummy")
	l.must(l.fw, "ip", "link", "set", "andaru-vip", "up")
	routes := 0
	var dst string
	for _, line := range strings.Split(fileEnding(t, r, "andaru-vip.network").Content, "\n") {
		if a, ok := strings.CutPrefix(line, "Address="); ok {
			l.must(l.fw, "ip", "addr", "add", a, "dev", "andaru-vip")
		}
		if d, ok := strings.CutPrefix(line, "Destination="); ok {
			dst = d
		}
		if line == "Type=local" {
			l.must(l.fw, "ip", "route", "add", "local", dst, "dev", "andaru-vip")
			routes++
		}
	}
	if routes != 2 {
		t.Fatalf("%d local routes rendered, want 2:\n%s", routes, golden(r))
	}
	l.load(in)

	cmd := exec.Command("ip", "netns", "exec", l.fw, "python3", "-c", `
import socket
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("203.0.113.3", 25)); s.listen()
while True: s.accept()[0].close()
`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(300 * time.Millisecond)

	l.must(l.wan, "ip", "route", "add", "203.0.113.0/29", "via", "10.1.0.1")
	// The fixture's policy lets the WAN reach no address of the firewall: only the forward's own rule, in the input
	// chain, admits it.
	if !l.connects(l.wan, "10.1.0.1", 25) {
		t.Error("the port forward did not reach the service on an address of the block")
	}
	if l.connects(l.wan, "203.0.113.3", 25) {
		t.Error("the WAN reached the block without a rule")
	}
	l.load(open)
	if !l.pings(l.wan, "203.0.113.5") {
		t.Error("the firewall did not answer a ping to an address of the block")
	}
	if !l.connects(l.wan, "203.0.113.3", 25) {
		t.Error("the WAN did not reach the service on an address of the block")
	}
	if out, _ := l.run(l.fw, "ip", "route", "get", "203.0.113.7"); !strings.HasPrefix(out, "local ") {
		t.Errorf("the block's last address is not local: %s", out)
	}
}

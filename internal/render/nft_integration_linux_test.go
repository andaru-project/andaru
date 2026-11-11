//go:build integration

package render

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lab is a firewall namespace with a WAN and a LAN neighbour, wired with veth pairs named as the fixture's links.
type lab struct {
	t            *testing.T
	fw, wan, lan string
}

func (l *lab) run(ns string, args ...string) (string, error) {
	out, err := exec.Command("ip", append([]string{"netns", "exec", ns}, args...)...).CombinedOutput()
	return string(out), err
}

func (l *lab) must(ns string, args ...string) {
	l.t.Helper()
	if out, err := l.run(ns, args...); err != nil {
		l.t.Fatalf("%s %v: %v\n%s", ns, args, err, out)
	}
}

func newLab(t *testing.T) *lab {
	if os.Geteuid() != 0 {
		t.Skip("namespaces need root")
	}
	for _, tool := range []string{"nft", "ip", "python3", "ping"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required", tool)
		}
	}
	tag := fmt.Sprint(os.Getpid())
	l := &lab{t: t, fw: "andaru-fw-" + tag, wan: "andaru-wan-" + tag, lan: "andaru-lan-" + tag}
	for _, ns := range []string{l.fw, l.wan, l.lan} {
		if out, err := exec.Command("ip", "netns", "add", ns).CombinedOutput(); err != nil {
			t.Fatalf("netns add: %v %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", ns).Run() })
		l.must(ns, "ip", "link", "set", "lo", "up")
	}
	for _, side := range []struct{ fwName, peer, ns, fwAddr, peerAddr string }{
		{"ens18", "w0", l.wan, "10.1.0.1/24", "10.1.0.2/24"},
		{"ens19", "l0", l.lan, "10.2.0.1/24", "10.2.0.2/24"},
	} {
		l.must(l.fw, "ip", "link", "add", side.fwName, "type", "veth", "peer", "name", side.peer, "netns", side.ns)
		l.must(l.fw, "ip", "addr", "add", side.fwAddr, "dev", side.fwName)
		l.must(l.fw, "ip", "link", "set", side.fwName, "up")
		l.must(side.ns, "ip", "addr", "add", side.peerAddr, "dev", side.peer)
		l.must(side.ns, "ip", "link", "set", side.peer, "up")
		l.must(side.ns, "ip", "route", "add", "default", "via", strings.Split(side.fwAddr, "/")[0])
	}
	l.must(l.fw, "sysctl", "-qw", "net.ipv4.ip_forward=1")
	return l
}

// load renders the declaration and loads its filter into the firewall namespace.
func (l *lab) load(in Input) {
	l.t.Helper()
	f, ok := nftFile(l.t, render(l.t, in))
	if !ok {
		l.t.Fatal("no filter rendered")
	}
	script := filepath.Join(l.t.TempDir(), "andaru.nft")
	if err := os.WriteFile(script, []byte(f.Content), 0o600); err != nil {
		l.t.Fatal(err)
	}
	l.must(l.fw, "nft", "-f", script)
}

// listen starts a TCP server in ns on port that writes the peer address of each connection to a file.
func (l *lab) listen(ns string, port int) string {
	l.t.Helper()
	peers := filepath.Join(l.t.TempDir(), "peers")
	cmd := exec.Command("ip", "netns", "exec", ns, "python3", "-c", fmt.Sprintf(`
import socket
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("0.0.0.0", %d)); s.listen()
while True:
    c, peer = s.accept(); open(%q, "a").write(peer[0] + "\n"); c.close()
`, port, peers))
	if err := cmd.Start(); err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(300 * time.Millisecond)
	return peers
}

// connects reports whether a TCP connection from ns reaches addr:port within a second.
func (l *lab) connects(ns, addr string, port int) bool {
	_, err := l.run(ns, "python3", "-c", fmt.Sprintf(`import socket; socket.create_connection((%q, %d), timeout=1).close()`, addr, port))
	return err == nil
}

func (l *lab) pings(ns, addr string) bool {
	_, err := l.run(ns, "ping", "-c1", "-W1", addr)
	return err == nil
}

func TestFilterInNamespace(t *testing.T) {
	l := newLab(t)
	// Another subsystem's table must survive every load.
	l.must(l.fw, "nft", "add", "table", "inet", "podman")
	l.must(l.fw, "nft", "add", "chain", "inet", "podman", "keep")

	// The starter policy, with one drop placed above the LAN's accept for port 8080.
	in := change(t, fixture(t, "filter-two-zones"), func(d map[string]any) {
		svc := map[string]any{"id": "svc_00000009-0000-4000-8000-000000000009", "name": "alt-web", "protocol": "tcp", "ports": []any{"8080"}}
		objects := d["objects"].(map[string]any)
		objects["services"] = append(objects["services"].([]any), svc)
		policy := d["policy"].(map[string]any)
		drop := map[string]any{"id": "rul_00000009-0000-4000-8000-000000000009", "name": "no alt-web", "enabled": true, "action": "drop",
			"from": []any{"zon_00000002-0000-4000-8000-000000000002"}, "to": []any{}, "services": []any{svc["id"]}, "log": false}
		policy["forward"] = append([]any{drop}, policy["forward"].([]any)...)
	})
	l.load(in)
	l.load(in) // replacing the table again is one atomic transaction

	if !l.pings(l.lan, "10.2.0.1") {
		t.Error("the LAN cannot ping the firewall, which a starter rule allows")
	}
	if l.pings(l.wan, "10.1.0.1") {
		t.Error("unsolicited WAN ping reached the firewall")
	}
	l.listen(l.fw, 22)
	if !l.connects(l.lan, "10.2.0.1", 22) {
		t.Error("SSH on the management segment was refused")
	}
	if l.connects(l.wan, "10.1.0.1", 22) {
		t.Error("SSH from the WAN reached the firewall")
	}
	peers := l.listen(l.wan, 80)
	l.listen(l.wan, 8080)
	if !l.connects(l.lan, "10.1.0.2", 80) {
		t.Fatal("the LAN cannot reach the WAN")
	}
	if seen, _ := os.ReadFile(peers); strings.TrimSpace(string(seen)) != "10.1.0.1" {
		t.Errorf("the WAN saw %q, want the masqueraded 10.1.0.1", seen)
	}
	if l.connects(l.lan, "10.1.0.2", 8080) {
		t.Error("a drop placed above the LAN's accept did not win")
	}
	l.listen(l.lan, 81)
	if l.connects(l.wan, "10.2.0.2", 81) {
		t.Error("unsolicited WAN traffic was forwarded to the LAN")
	}
	if out, err := l.run(l.fw, "nft", "list", "table", "inet", "podman"); err != nil || !strings.Contains(out, "chain keep") {
		t.Errorf("the podman table was touched: %v %s", err, out)
	}
}

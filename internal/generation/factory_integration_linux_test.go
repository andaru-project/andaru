//go:build integration

package generation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The factory filter, loaded in a namespace, lets the box take DHCP and SLAAC and keeps new connections out.
func TestFactoryFilterInNamespace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("namespaces need root")
	}
	for _, tool := range []string{"nft", "ip", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("ANDARU_TEST_REQUIRE_TOOLS") == "1" {
				t.Fatalf("%s is required", tool)
			}
			t.Skipf("%s not installed", tool)
		}
	}
	tag := fmt.Sprint(os.Getpid())
	fw, peer := "andaru-factory-"+tag, "andaru-dhcp-"+tag
	in := func(ns string, args ...string) *exec.Cmd {
		return exec.Command("ip", append([]string{"netns", "exec", ns}, args...)...)
	}
	must := func(ns string, args ...string) {
		t.Helper()
		if out, err := in(ns, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", ns, args, err, out)
		}
	}
	for _, ns := range []string{fw, peer} {
		if out, err := exec.Command("ip", "netns", "add", ns).CombinedOutput(); err != nil {
			t.Fatalf("netns add: %v %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", ns).Run() })
	}
	must(fw, "ip", "link", "add", "fv", "type", "veth", "peer", "name", "pv", "netns", peer)
	for _, side := range []struct{ ns, dev, v4, v6 string }{{fw, "fv", "10.9.0.1/24", "2001:db8:9::1/64"}, {peer, "pv", "10.9.0.2/24", "2001:db8:9::2/64"}} {
		must(side.ns, "ip", "addr", "add", side.v4, "dev", side.dev)
		must(side.ns, "ip", "addr", "add", side.v6, "dev", side.dev, "nodad")
	}
	// The router advertisement leaves from the peer's link-local address at once: no duplicate address detection.
	must(peer, "ip", "link", "set", "pv", "addrgenmode", "none")
	must(peer, "ip", "addr", "add", "fe80::2/64", "dev", "pv", "nodad")
	must(fw, "ip", "link", "set", "fv", "up")
	must(peer, "ip", "link", "set", "pv", "up")
	// listen runs a server in the firewall namespace that exits 0 once it receives anything.
	listen := func(code string) *exec.Cmd {
		t.Helper()
		cmd := in(fw, "python3", "-c", "import socket\n"+code)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		time.Sleep(300 * time.Millisecond)
		return cmd
	}
	connects := func() bool {
		return in(peer, "python3", "-c", `import socket; socket.create_connection(("10.9.0.1", 8080), timeout=1).close()`).Run() == nil
	}
	listen(`s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("0.0.0.0", 8080)); s.listen()
while True: s.accept()[0].close()`)
	if !connects() {
		t.Fatal("no connection before the filter: the check proves nothing")
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := Realizer{Root: root, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return in(fw, append([]string{name}, args...)...).CombinedOutput()
	}}
	if err := r.factoryFilter(t.Context()); err != nil {
		t.Fatal(err)
	}

	if connects() {
		t.Error("a new inbound connection passed the factory filter")
	}
	for _, reply := range []struct{ name, family, server, client string }{
		{"DHCPv4", "AF_INET", `("10.9.0.2", 67)`, `("10.9.0.1", 68)`},
		{"DHCPv6", "AF_INET6", `("2001:db8:9::2", 547)`, `("2001:db8:9::1", 546)`},
	} {
		wildcard := map[string]string{"AF_INET": `""`, "AF_INET6": `"::"`}[reply.family]
		got := listen(fmt.Sprintf(`s = socket.socket(socket.%s, socket.SOCK_DGRAM); s.bind((%s, %s[1])); s.settimeout(3); s.recv(64)`, reply.family, wildcard, reply.client))
		must(peer, "python3", "-c", fmt.Sprintf(`import socket
s = socket.socket(socket.%s, socket.SOCK_DGRAM); s.bind(%s); s.sendto(b"reply", %s)`, reply.family, reply.server, reply.client))
		if err := got.Wait(); err != nil {
			t.Errorf("the %s client reply did not pass: %v", reply.name, err)
		}
	}
	// A router advertisement with a prefix: SLAAC gives the firewall an address in it only if the filter lets it in.
	must(peer, "python3", "-c", `import socket, struct
s = socket.socket(socket.AF_INET6, socket.SOCK_RAW, socket.IPPROTO_ICMPV6)
s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_MULTICAST_HOPS, 255)
ra = struct.pack("!BBHBBHII", 134, 0, 0, 64, 0, 1800, 0, 0)
pio = struct.pack("!BBBBIII", 3, 4, 64, 0xc0, 86400, 14400, 0) + socket.inet_pton(socket.AF_INET6, "2001:db8:77::")
s.sendto(ra + pio, ("ff02::1", 0, 0, socket.if_nametoindex("pv")))`)
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, _ := in(fw, "ip", "-6", "addr", "show", "dev", "fv").CombinedOutput()
		if strings.Contains(string(out), "2001:db8:77:") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the router advertisement did not pass:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

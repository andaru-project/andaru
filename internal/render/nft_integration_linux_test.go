//go:build integration

package render

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		aliases := d["aliases"].(map[string]any)
		aliases["ports"] = append(aliases["ports"].([]any), svc)
		drop := map[string]any{"id": "rul_00000009-0000-4000-8000-000000000009", "name": "no alt-web", "enabled": true, "action": "drop",
			"from": map[string]any{"zones": []any{"zon_00000002-0000-4000-8000-000000000002"}, "addresses": []any{}}, "to": map[string]any{"zones": []any{}, "addresses": []any{}}, "ports": []any{svc["id"]}, "log": false}
		d["rules"] = append([]any{drop}, d["rules"].([]any)...)
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
	// The server records the peer after accept, which can land after the client returns.
	var seen []byte
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end) && strings.TrimSpace(string(seen)) != "10.1.0.1"; time.Sleep(20 * time.Millisecond) {
		seen, _ = os.ReadFile(peers)
	}
	if strings.TrimSpace(string(seen)) != "10.1.0.1" {
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

// Port forwards in a real kernel: the WAN reaches the LAN host through the forward and the firewall through a
// self target, sees nothing directly, and an excluded source is not translated.
func TestPortForwardsInNamespace(t *testing.T) {
	l := newLab(t)
	in := change(t, fixture(t, "nat-port-forwards"), func(d map[string]any) {
		for _, f := range d["nat"].(map[string]any)["portForwards"].([]any) {
			if target, ok := f.(map[string]any)["target"].(map[string]any); ok && target["address"] != nil {
				target["address"] = "10.2.0.2"
			}
		}
		// The exclusion now names the WAN neighbour, so its connections on 1000-1001 stay untranslated.
		addresses := d["aliases"].(map[string]any)["addresses"].([]any)
		addresses[len(addresses)-1].(map[string]any)["address"] = "10.1.0.2"
	})
	l.load(in)
	peers := l.listen(l.lan, 80)
	l.listen(l.lan, 2000)
	l.listen(l.fw, 22)
	if !l.connects(l.wan, "10.1.0.1", 8080) {
		t.Fatal("the forward 8080 to the LAN host did not connect")
	}
	var seen []byte
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end) && strings.TrimSpace(string(seen)) == ""; time.Sleep(20 * time.Millisecond) {
		seen, _ = os.ReadFile(peers)
	}
	if strings.TrimSpace(string(seen)) != "10.1.0.2" {
		t.Errorf("the LAN host saw %q, want the real client 10.1.0.2", seen)
	}
	if l.connects(l.wan, "10.2.0.2", 80) {
		t.Error("the LAN host was reachable directly; the associated rule must admit only translated connections")
	}
	if !l.connects(l.wan, "10.1.0.1", 2222) {
		t.Error("the forward 2222 to the firewall's SSH did not connect")
	}
	if l.connects(l.wan, "10.1.0.1", 1000) {
		t.Error("an excluded source was translated")
	}
}

// 1:1 in a real kernel: the WAN reaches the inside host on its own external address, the host leaves through it,
// and a LAN client reflected onto the external address is answered through the firewall.
func TestOneToOneInNamespace(t *testing.T) {
	l := newLab(t)
	l.must(l.fw, "ip", "addr", "add", "10.1.0.29/24", "dev", "ens18")
	in := change(t, fixture(t, "nat-one-to-one"), func(d map[string]any) {
		segments := d["segments"].([]any)
		segments[0].(map[string]any)["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "static", "addresses": []any{"10.1.0.1/24", "10.1.0.29/24"}, "gateway": "10.1.0.2"}
		segments[1].(map[string]any)["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "static", "addresses": []any{"10.2.0.1/24"}}
		nat := d["nat"].(map[string]any)
		server := nat["oneToOne"].([]any)[1].(map[string]any)
		server["external"], server["internal"] = "10.1.0.29", "10.2.0.2"
		nat["oneToOne"] = nat["oneToOne"].([]any)[:2]
		delete(nat, "portForwards")
		// 1:1 has no associated rule: the policy admits the WAN to the host, as an operator would write it.
		d["rules"] = append(d["rules"].([]any), map[string]any{"id": "rul_00000009-0000-4000-8000-000000000009", "name": "to the server",
			"enabled": true, "action": "accept", "from": map[string]any{"zones": []any{"zon_00000001-0000-4000-8000-000000000001"}, "addresses": []any{}}, "to": map[string]any{"zones": []any{"zon_00000002-0000-4000-8000-000000000002"}, "addresses": []any{}}, "ports": []any{}, "log": false})
	})
	l.load(in)
	wait := func(peers, want string) {
		t.Helper()
		var seen []byte
		for end := time.Now().Add(2 * time.Second); time.Now().Before(end) && !strings.Contains(string(seen), want); time.Sleep(20 * time.Millisecond) {
			seen, _ = os.ReadFile(peers)
		}
		if !strings.Contains(string(seen), want) {
			t.Errorf("peers %q, want %s", seen, want)
		}
	}
	inside := l.listen(l.lan, 80)
	if !l.connects(l.wan, "10.1.0.29", 80) {
		t.Fatal("the WAN did not reach the inside host on its external address")
	}
	wait(inside, "10.1.0.2")
	outside := l.listen(l.wan, 81)
	if !l.connects(l.lan, "10.1.0.2", 81) {
		t.Fatal("the inside host could not reach the WAN")
	}
	wait(outside, "10.1.0.29")
	// Reflection: the LAN host asks its own external address and is answered through the firewall.
	if !l.connects(l.lan, "10.1.0.29", 80) {
		t.Error("reflection to the external address did not connect")
	}
	wait(inside, "10.2.0.1")
}

// Codex's case: forward A (self:2222 to the firewall's SSH, auto rule) and forward B (a routed address's 2222 to the
// same SSH, filterRule none). B's connections must meet the policy, not A's rule.
func TestForwardRuleAdmitsOnlyItsOwnTranslations(t *testing.T) {
	l := newLab(t)
	in := change(t, fixture(t, "nat-port-forwards"), func(d map[string]any) {
		addresses := d["aliases"].(map[string]any)["addresses"].([]any)
		addresses = append(addresses, map[string]any{"id": "adr_00000008-0000-4000-8000-000000000008", "name": "routed", "kind": "host", "address": "10.9.9.9"})
		d["aliases"].(map[string]any)["addresses"] = addresses
		nat := d["nat"].(map[string]any)
		forwards := nat["portForwards"].([]any)
		b := map[string]any{"id": "pfw_00000009-0000-4000-8000-000000000009", "name": "routed-ssh", "enabled": true,
			"ingressZones": []any{"zon_00000001-0000-4000-8000-000000000001"}, "protocol": "tcp", "from": []any{},
			"to": map[string]any{"address": "adr_00000008-0000-4000-8000-000000000008"}, "ports": "2222",
			"target": map[string]any{"address": "10.1.0.1"}, "targetPort": 22, "filterRule": "none"}
		nat["portForwards"] = []any{forwards[3], b}
	})
	l.load(in)
	l.must(l.wan, "ip", "route", "add", "10.9.9.9/32", "via", "10.1.0.1")
	l.listen(l.fw, 22)
	if !l.connects(l.wan, "10.1.0.1", 2222) {
		t.Fatal("forward A did not connect")
	}
	if l.connects(l.wan, "10.9.9.9", 2222) {
		t.Error("forward B, which has no rule, was admitted by forward A's rule")
	}
}

// Virtual addresses in a real kernel: forwards from the WAN reach a service on the dummy device in both
// families, and the virtual address itself is not open to the WAN.
func TestVirtualAddressesInNamespace(t *testing.T) {
	l := newLab(t)
	in := change(t, fixture(t, "virtual"), func(d map[string]any) {
		for _, f := range d["nat"].(map[string]any)["portForwards"].([]any) {
			if f.(map[string]any)["protocol"] == "udp" {
				f.(map[string]any)["protocol"] = "tcp"
			}
		}
	})
	l.must(l.fw, "ip", "link", "add", "andaru-vip", "type", "dummy")
	l.must(l.fw, "ip", "addr", "add", "172.16.53.53/32", "dev", "andaru-vip")
	l.must(l.fw, "ip", "addr", "add", "fd00:53:53:53:53:53:53:53/128", "dev", "andaru-vip")
	l.must(l.fw, "ip", "link", "set", "andaru-vip", "up")
	l.must(l.fw, "ip", "-6", "addr", "add", "fd00:1::1/64", "dev", "ens18", "nodad")
	l.must(l.wan, "ip", "-6", "addr", "add", "fd00:1::2/64", "dev", "w0", "nodad")
	l.must(l.wan, "ip", "-6", "route", "add", "default", "via", "fd00:1::1")
	l.load(in)
	cmd := exec.Command("ip", "netns", "exec", l.fw, "python3", "-c", `
import socket, threading
def serve(port):
    s = socket.socket(socket.AF_INET6); s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("::", port)); s.listen()
    while True: s.accept()[0].close()
for p in (53, 22): threading.Thread(target=serve, args=(p,), daemon=True).start()
threading.Event().wait()
`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(300 * time.Millisecond)
	for _, c := range []struct {
		addr string
		port int
		want bool
		what string
	}{
		{"10.1.0.1", 53, true, "the IPv4 forward to the virtual address"},
		{"fd00:1::1", 53, true, "the IPv6 forward to the virtual address"},
		{"fd00:1::1", 2222, true, "the IPv6 forward to the firewall's own port 22"},
		{"172.16.53.53", 53, false, "the virtual address itself, from the WAN"},
		{"fd00:53:53:53:53:53:53:53", 53, false, "the IPv6 virtual address itself, from the WAN"},
	} {
		l.must(l.wan, "ip", "route", "replace", "172.16.53.53/32", "via", "10.1.0.1")
		if got := l.connects(l.wan, c.addr, c.port); got != c.want {
			t.Errorf("%s: connected %v, want %v", c.what, got, c.want)
		}
	}
}

// A side's zone and addresses must both match, "not" leaves the named addresses out, and only a rule that names
// the firewall reaches it: "any" is every network, never the box.
func TestRuleSidesInNamespace(t *testing.T) {
	l := newLab(t)
	const lanZone, peer, other = "zon_00000002-0000-4000-8000-000000000002", "adr_00000008-0000-4000-8000-000000000008", "adr_00000009-0000-4000-8000-000000000009"
	side := func(zones []any, not bool, addresses ...any) map[string]any {
		s := map[string]any{"zones": zones, "addresses": append([]any{}, addresses...)}
		if not {
			s["not"] = true
		}
		return s
	}
	with := func(from, to map[string]any) {
		t.Helper()
		l.load(change(t, fixture(t, "filter-two-zones"), func(d map[string]any) {
			aliases := d["aliases"].(map[string]any)
			aliases["addresses"] = append(aliases["addresses"].([]any),
				map[string]any{"id": peer, "name": "the LAN peer", "kind": "host", "address": "10.2.0.2"},
				map[string]any{"id": other, "name": "another host", "kind": "host", "address": "10.2.0.99"})
			d["rules"] = []any{map[string]any{"id": "rul_00000009-0000-4000-8000-000000000009", "name": "the rule", "enabled": true, "action": "accept",
				"from": from, "to": to, "ports": []any{}, "log": false}}
		}))
	}
	l.listen(l.wan, 8080)
	l.listen(l.fw, 8080)
	lan, anywhere, firewall := []any{lanZone}, side([]any{}, false), side([]any{firewallZone}, false)
	out := func() bool { return l.connects(l.lan, "10.1.0.2", 8080) }
	in := func() bool { return l.connects(l.lan, "10.2.0.1", 8080) }

	with(side(lan, false, peer), anywhere)
	if !out() {
		t.Error("the peer, arriving from its zone, was refused")
	}
	if in() {
		t.Error("a rule to any network reached the firewall itself")
	}
	with(side(lan, false, other), anywhere)
	if out() {
		t.Error("a host the rule does not name passed because its zone matched")
	}
	with(side([]any{"zon_00000001-0000-4000-8000-000000000001"}, false, peer), anywhere)
	if out() {
		t.Error("a host arriving from another zone passed because its address matched")
	}
	with(side(lan, true, peer), anywhere)
	if out() {
		t.Error("a host the rule leaves out passed")
	}
	with(side(lan, true, other), anywhere)
	if !out() {
		t.Error("a host the rule does not leave out was refused")
	}
	with(side(lan, false, peer), firewall)
	if !in() {
		t.Error("a rule to the firewall did not reach it")
	}
	if out() {
		t.Error("a rule to the firewall let traffic through it")
	}
}

// The heaviest line a forward renders, with as many left-out sources as validation allows (config.maxLeftOut),
// still loads: the kernel caps the matches on one rule.
func TestLeftOutCapLoads(t *testing.T) {
	l := newLab(t)
	const maxLeftOut = 40
	in := change(t, fixture(t, "nat-port-forwards"), func(d map[string]any) {
		aliases := d["aliases"].(map[string]any)
		var from []any
		for i := range maxLeftOut {
			id := fmt.Sprintf("adr_%08x-0000-4000-8000-%012x", 0x100+i, 0x100+i)
			aliases["addresses"] = append(aliases["addresses"].([]any), map[string]any{"id": id, "name": id[:12], "kind": "network", "prefix": fmt.Sprintf("10.%d.0.0/16", 100+i)})
			from = append(from, id)
		}
		f := d["nat"].(map[string]any)["portForwards"].([]any)[2].(map[string]any)
		f["from"], f["fromNegated"], f["protocol"], f["log"] = from, true, "tcp-udp", true
		f["to"], f["toNegated"] = map[string]any{"address": from[0]}, true
	})
	l.load(in)
}

func TestInlineLiteralsLoad(t *testing.T) {
	l := newLab(t)
	const maxLeftOut = 40
	leftOutV6 := make([]any, maxLeftOut)
	for i := range maxLeftOut {
		leftOutV6[i] = fmt.Sprintf("2001:db8:%x::/47", (i+1)*2)
	}
	in := change(t, fixture(t, "nat-port-forwards"), func(d map[string]any) {
		d["management"].(map[string]any)["allowedSources"] = []any{"10.0.0.1", "10.0.0.0/24", "10.0.1.1-10.0.1.5", "2001:db8::1"}
		d["rules"] = append(d["rules"].([]any),
			map[string]any{
				"id": "rul_00000011-0000-4000-8000-000000000011", "name": "lit", "enabled": true, "action": "accept",
				"from":  map[string]any{"zones": []any{}, "addresses": []any{"10.0.0.1", "10.0.0.10-10.0.0.20"}},
				"to":    map[string]any{"zones": []any{}, "addresses": []any{"198.51.100.0/24", "198.51.100.10-198.51.100.20"}, "not": true},
				"ports": []any{"tcp/443", "udp/53,5353", "tcp/8000-8080", "icmp", "icmp/echo-request"},
				"log":   false,
			},
			map[string]any{
				"id": "rul_00000012-0000-4000-8000-000000000012", "name": "lit-v6", "enabled": true, "action": "reject",
				"from":  map[string]any{"zones": []any{}, "addresses": []any{"2001:db8::1-2001:db8::ff"}, "not": true},
				"to":    map[string]any{"zones": []any{}, "addresses": []any{"2001:db8::/64"}},
				"ports": []any{"tcp/443", "icmpv6"},
				"log":   false,
			},
		)
		pfs := d["nat"].(map[string]any)["portForwards"].([]any)
		pf0 := pfs[0].(map[string]any)
		pf0["from"] = []any{"198.51.100.7"}
		pf0["to"] = map[string]any{"address": "203.0.113.5"}
		// Heaviest port forward line with 40 negated non-byte-aligned IPv6 CIDRs must stay under the kernel rule size cap.
		pf2 := pfs[2].(map[string]any)
		pf2["family"], pf2["from"], pf2["fromNegated"], pf2["protocol"], pf2["log"] = "ipv6", leftOutV6, true, "tcp-udp", true
		pf2["to"], pf2["toNegated"], pf2["target"] = map[string]any{"address": "2001:db8:fe::/47"}, true, map[string]any{"self": true}
		d["nat"].(map[string]any)["oneToOne"] = []any{
			map[string]any{
				"id": "oto_00000015-0000-4000-8000-000000000015", "name": "lit-o2o", "enabled": true,
				"zone": "zon_00000001-0000-4000-8000-000000000001", "external": "203.0.113.50", "internal": "192.0.2.50",
				"destination": []any{"198.51.100.9", "198.51.100.10-198.51.100.20"}, "destinationNegated": true,
			},
		}
	})
	l.load(in)
}

// The WireGuard interfaces' zone sets and listen-port rules load, scoped to uplinks or to named segments.
func TestWireGuardFilterLoads(t *testing.T) {
	l := newLab(t)
	l.load(fixture(t, "wireguard"))
	out, err := l.run(l.fw, "nft", "list", "chain", "inet", "andaru", "input")
	if err != nil || !strings.Contains(out, "iifname @uplinks udp dport 51820 accept") || !strings.Contains(out, `udp dport 51821 accept`) {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The IPsec connections' rules load: IKE, NAT-T and ESP input, XFRM interfaces in their zones, and a policy-based
// connection's zone matched by its reqid, also kept out of the masquerade.
func TestIPsecFilterLoads(t *testing.T) {
	l := newLab(t)
	l.load(fixture(t, "ipsec"))
	req := strconv.Itoa(ipsecReqid("ips_00000002-0000-4000-8000-000000000002", map[int]bool{}))
	for chain, want := range map[string][]string{
		"input":       {"iifname @uplinks udp dport { 500, 4500 } accept", "meta ipsec exists ipsec in reqid " + req, `iifname "ens18" udp dport 500 accept`},
		"forward":     {"ipsec out reqid " + req},
		"postrouting": {"ipsec out reqid " + req + " accept"},
	} {
		out, err := l.run(l.fw, "nft", "list", "chain", "inet", "andaru", chain)
		for _, line := range want {
			if err != nil || !strings.Contains(out, line) {
				t.Fatalf("%s lacks %q: %v\n%s", chain, line, err, out)
			}
		}
	}
}

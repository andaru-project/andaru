package generation

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// kea6Config serves ens19 with fd00:1::/64 and delegates /56s from fd00:beef:1::/48.
const kea6Served = `{"Dhcp6":{"subnet6":[{"id":1,"subnet":"fd00:1::/64","interface":"ens19",
	"pd-pools":[{"prefix":"fd00:beef:1::","prefix-len":48,"delegated-len":56}],
	"reservations":[{"duid":"00:01:00:01:aa:bb:cc:dd:ee:ff","ip-addresses":["fd00:1::10"],"hostname":"printer"}],
	"user-context":{"andaru":{"domain":"home.arpa","register":true}}}]}}`

// keaUser stands for _kea: the owner of Kea's runtime directory and of what its hook writes there.
const keaUser = 4242

type lease6 struct {
	Address   string `json:"ip-address"`
	PrefixLen int    `json:"prefix-len,omitempty"`
	Type      string `json:"type"`
	Hostname  string `json:"hostname"`
	State     int    `json:"state"`
	Subnet    int    `json:"subnet-id"`
	Valid     int    `json:"valid-lft"`
	Cltt      int    `json:"cltt"`
}

func pd(prefix string) lease6 {
	addr, _, _ := strings.Cut(prefix, "/")
	return lease6{Address: addr, PrefixLen: 56, Type: "IA_PD", Subnet: 1, Valid: 4000, Cltt: 100}
}

// kea6 answers lease6-get-all on the bench's DHCPv6 socket, with Kea's runtime directory owned by keaUser.
func (b *bench) kea6(leases ...lease6) {
	b.t.Helper()
	writeFile(b.t, filepath.Join(b.net.root, kea6File), kea6Served)
	dir := filepath.Join(b.net.root, "run/kea")
	if err := os.MkdirAll(dir, 0o750); err != nil || os.Chown(dir, keaUser, keaUser) != nil {
		b.t.Fatal("Kea's directory")
	}
	l, err := net.Listen("unix", filepath.Join(dir, "kea6-ctrl-socket"))
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(c).ReadString('}')
			reply, _ := json.Marshal(map[string]any{"result": 0, "arguments": map[string]any{"leases": leases}})
			c.Write(reply)
			c.Close()
		}
	}()
}

// events writes what Kea's hook would append, owned by Kea.
func (b *bench) events(lines ...string) string {
	b.t.Helper()
	path := filepath.Join(b.net.root, pdEvents)
	writeFile(b.t, path, strings.Join(lines, "\n")+"\n")
	if err := os.Chown(path, keaUser, keaUser); err != nil {
		b.t.Fatal(err)
	}
	return path
}

func (b *bench) table() map[string]nextHop {
	var t map[string]nextHop
	data, _ := os.ReadFile(filepath.Join(b.net.root, pdTable))
	_ = json.Unmarshal(data, &t)
	return t
}

func TestPDEventInstallsRoute(t *testing.T) {
	b := newBench(t)
	b.kea6(pd("fd00:beef:1:100::/56"))
	b.events("add fd00:beef:1:100::/56 fe80::1 ens19")
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 188") < 0 {
		t.Fatalf("route not installed: %q", b.net.argv)
	}
	if hop := b.table()["fd00:beef:1:100::/56"]; hop.Via != "fe80::1" || hop.Dev != "ens19" {
		t.Fatalf("table %+v", b.table())
	}
	// The hook's file was moved out of Kea's directory, never read in place.
	if _, err := os.Stat(filepath.Join(b.net.root, pdEvents)); !os.IsNotExist(err) {
		t.Fatal("event file left in Kea's directory")
	}
}

// Records are believed only when the generation and Kea's own leases agree with them.
func TestPDEventRefusals(t *testing.T) {
	for name, line := range map[string]string{
		"interface not served": "add fd00:beef:1:100::/56 fe80::1 ens18",
		"global next hop":      "add fd00:beef:1:100::/56 2001:db8::1 ens19",
		"outside the pool":     "add fd00:dead:1:100::/56 fe80::1 ens19",
		"not leased":           "add fd00:beef:1:200::/56 fe80::1 ens19",
		"garbage":              "add fd00:beef:1:100::/56 fe80::1 ens19; reboot",
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			b.kea6(pd("fd00:beef:1:100::/56"))
			b.events(line)
			if err := b.r.SyncLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(b.net.argv, func(c string) bool { return strings.HasPrefix(c, "ip -6 route replace") }) {
				t.Fatalf("believed %q: %q", line, b.net.argv)
			}
		})
	}
}

// _kea owns the directory the hook writes in: a link, another owner's file or a huge file is never read.
func TestPDEventFileHostile(t *testing.T) {
	for name, plant := range map[string]func(b *bench, path string){
		"symlink": func(b *bench, path string) {
			target := filepath.Join(b.net.root, "secret")
			// The target even looks like Kea's, so only refusing to follow the link stops it.
			writeFile(b.t, target, "add fd00:beef:1:100::/56 fe80::1 ens19\n")
			_ = os.Chown(target, keaUser, keaUser)
			_ = os.Symlink(target, path)
		},
		"other owner": func(b *bench, path string) {
			writeFile(b.t, path, "add fd00:beef:1:100::/56 fe80::1 ens19\n")
			_ = os.Chown(path, 0, 0)
		},
		"oversized": func(b *bench, path string) {
			writeFile(b.t, path, "add fd00:beef:1:100::/56 fe80::1 ens19\n"+strings.Repeat("#", 2<<20))
			_ = os.Chown(path, keaUser, keaUser)
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			b.kea6(pd("fd00:beef:1:100::/56"))
			plant(b, filepath.Join(b.net.root, pdEvents))
			if err := b.r.SyncLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(b.net.argv, func(c string) bool { return strings.HasPrefix(c, "ip -6 route replace") }) {
				t.Fatalf("believed a %s: %q", name, b.net.argv)
			}
		})
	}
}

// Only protocol 188 routes nobody leases are removed.
func TestPDRouteRemoval(t *testing.T) {
	b := newBench(t)
	b.kea6()
	b.net.routes = map[string]string{"fd00:beef:1:300::/56": "fe80::3 dev ens19"}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route del fd00:beef:1:300::/56 proto 188") < 0 {
		t.Fatalf("stale route kept: %q", b.net.argv)
	}
}

// After a reboot the hook has said nothing yet; the table and Kea's leases bring the route back.
func TestPDRoutesAfterReboot(t *testing.T) {
	b := newBench(t)
	b.kea6(pd("fd00:beef:1:100::/56"))
	writeFile(t, filepath.Join(b.net.root, pdTable), `{"fd00:beef:1:100::/56":{"via":"fe80::1","dev":"ens19"}}`)
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 188") < 0 {
		t.Fatalf("route not restored: %q", b.net.argv)
	}
}

// Names are kept per family: a static DHCPv4 name does not block a DHCPv6 client's AAAA record.
func TestDHCPv6Names(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop", 0, 1, 7200, 100})
	// "nas" is a static DHCPv4 name, "printer" a static DHCPv6 one.
	b.kea6(lease6{Address: "fd00:1::1001", Type: "IA_NA", Hostname: "printer.", Subnet: 1, Valid: 4000, Cltt: 100},
		lease6{Address: "fd00:1::1002", Type: "IA_NA", Hostname: "nas", Subnet: 1, Valid: 4000, Cltt: 100})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	names := b.leaseNames()
	for _, want := range []string{`"laptop.home.arpa. 2400 IN A 192.168.40.130"`, `"nas.home.arpa. 1333 IN AAAA fd00:1::1002"`, `"fd00:1::1002 1333 nas.home.arpa."`} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing %s in:\n%s", want, names)
		}
	}
	if strings.Contains(names, "fd00:1::1001") {
		t.Fatalf("a dynamic lease took a static DHCPv6 name:\n%s", names)
	}
}

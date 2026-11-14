package generation

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// kea6Config serves ens19 with fd00:1::/64 and delegates /56s from fd00:beef:1::/48.
const kea6Served = `{"Dhcp6":{"subnet6":[{"id":1,"subnet":"fd00:1::/64","interface":"ens19",
	"pd-pools":[{"prefix":"fd00:beef:1::","prefix-len":48,"delegated-len":56}],
	"reservations":[{"duid":"00:01:00:01:aa:bb:cc:dd:ee:ff","ip-addresses":["fd00:1::10"],"hostname":"printer"},
		{"duid":"00:01:00:01:aa:bb:cc:dd:ee:01","prefixes":["fd00:beef:1:300::/56"],"hostname":"router"}],
	"user-context":{"andaru":{"domain":"home.arpa","register":true}}}]}}`

// keaUser stands for _kea: the owner of Kea's runtime directory and of what its hook writes there. Unprivileged
// tests can only give their own files away to themselves.
var keaUser = func() int {
	if os.Getuid() == 0 {
		return 4242
	}
	return os.Getuid()
}()

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
	b.kea6Seq(nil, leases)
}

// kea6Seq answers the n-th question with sets[n] (the last set after that), running onAsk(n) before answering.
func (b *bench) kea6Seq(onAsk func(n int), sets ...[]lease6) {
	b.t.Helper()
	writeFile(b.t, filepath.Join(b.net.root, kea6File), kea6Served)
	dir := filepath.Join(b.net.root, "run/kea")
	if err := os.MkdirAll(dir, 0o750); err != nil || os.Chown(dir, keaUser, -1) != nil {
		b.t.Fatal("Kea's directory")
	}
	l, err := net.Listen("unix", filepath.Join(dir, "kea6-ctrl-socket"))
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { l.Close() })
	go func() {
		for n := 0; ; n++ {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(c).ReadString('}')
			if onAsk != nil {
				onAsk(n)
			}
			leases := sets[min(n, len(sets)-1)]
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
	if err := os.Chown(path, keaUser, -1); err != nil {
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
	if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 77") < 0 {
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
			_ = os.Chown(target, keaUser, -1)
			_ = os.Symlink(target, path)
		},
		"other owner": func(b *bench, path string) {
			writeFile(b.t, path, "add fd00:beef:1:100::/56 fe80::1 ens19\n")
			// The file stays the test's own; Kea is another user, as no unprivileged test could make it.
			b.r.keaUID = func() uint32 { return uint32(os.Getuid()) + 1 }
		},
		"oversized": func(b *bench, path string) {
			writeFile(b.t, path, "add fd00:beef:1:100::/56 fe80::1 ens19\n"+strings.Repeat("#", 2<<20))
			_ = os.Chown(path, keaUser, -1)
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

// Only protocol 77 routes nobody leases are removed.
func TestPDRouteRemoval(t *testing.T) {
	b := newBench(t)
	b.kea6()
	b.net.routes = map[string]string{"fd00:beef:1:300::/56": "fe80::3 dev ens19"}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route del fd00:beef:1:300::/56 proto 77") < 0 {
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
	if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 77") < 0 {
		t.Fatalf("route not restored: %q", b.net.argv)
	}
}

// A dynamic DHCPv6 name is not published when the name is static in either family: a client cannot take over a
// DHCPv4-only device's name for AAAA.
func TestDHCPv6Names(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop", 0, 1, 7200, 100})
	// "nas" is a static DHCPv4 name, "printer" a static DHCPv6 one.
	b.kea6(lease6{Address: "fd00:1::1001", Type: "IA_NA", Hostname: "printer.", Subnet: 1, Valid: 4000, Cltt: 100},
		lease6{Address: "fd00:1::1002", Type: "IA_NA", Hostname: "nas", Subnet: 1, Valid: 4000, Cltt: 100},
		// A static lease of a prefix only publishes no name, so it must not hold back the dynamic one.
		lease6{Address: "fd00:1::1003", Type: "IA_NA", Hostname: "router", Subnet: 1, Valid: 4000, Cltt: 100})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	names := b.leaseNames()
	for _, want := range []string{`"router.home.arpa. 1333 IN AAAA fd00:1::1003"`, `"laptop.home.arpa. 2400 IN A 192.168.40.130"`} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing %s in:\n%s", want, names)
		}
	}
	if strings.Contains(names, "fd00:1::1001") || strings.Contains(names, "fd00:1::1002") {
		t.Fatalf("a dynamic lease took a static name:\n%s", names)
	}
}

// A FIFO planted where the hook writes must not hang root while it holds the generation lock.
func TestPDEventFIFO(t *testing.T) {
	b := newBench(t)
	b.kea6(pd("fd00:beef:1:100::/56"))
	path := filepath.Join(b.net.root, pdEvents)
	if err := syscall.Mkfifo(path, 0o600); err != nil || os.Chown(path, keaUser, -1) != nil {
		t.Fatal("mkfifo")
	}
	done := make(chan error, 1)
	go func() { done <- b.r.SyncLeases(t.Context()) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sync hung on a FIFO")
	}
}

// A saved next hop is believed only while the generation still serves its interface and pool.
func TestPDTableHopNotServed(t *testing.T) {
	b := newBench(t)
	b.kea6(pd("fd00:beef:1:100::/56"))
	writeFile(t, filepath.Join(b.net.root, pdTable), `{"fd00:beef:1:100::/56":{"via":"fe80::1","dev":"ens18"}}`)
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(b.net.argv, func(c string) bool { return strings.HasPrefix(c, "ip -6 route replace") }) {
		t.Fatalf("a hop on an unserved interface was installed: %q", b.net.argv)
	}
}

// A lease committed while the sync reads Kea's list keeps its next hop: events are taken before the list.
func TestPDEventBeforeLeaseList(t *testing.T) {
	b := newBench(t)
	b.kea6Seq(func(n int) {
		if n == 0 {
			// Kea answers without the lease, then commits it and runs the hook.
			b.events("add fd00:beef:1:100::/56 fe80::1 ens19")
			writeFile(t, filepath.Join(b.net.root, leaseMarker), "")
		}
	}, nil, []lease6{pd("fd00:beef:1:100::/56")})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 77") < 0 {
		t.Fatalf("next hop lost: %q", b.net.argv)
	}
}

// A released prefix is forgotten and its route removed.
func TestPDReleaseRemovesRoute(t *testing.T) {
	b := newBench(t)
	b.kea6(pd("fd00:beef:1:100::/56"))
	b.net.routes = map[string]string{"fd00:beef:1:100::/56": "fe80::1 dev ens19"}
	writeFile(t, filepath.Join(b.net.root, pdTable), `{"fd00:beef:1:100::/56":{"via":"fe80::1","dev":"ens19"}}`)
	b.events("del fd00:beef:1:100::/56 fe80::1 ens19")
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route del fd00:beef:1:100::/56 proto 77") < 0 || len(b.table()) != 0 {
		t.Fatalf("release kept the route: %q %+v", b.net.argv, b.table())
	}
}

// A DHCPv6 server that does not answer costs its own names and routes, not DHCPv4's names.
func TestDHCPv4NamesSurviveADeadDHCPv6(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop", 0, 1, 7200, 100})
	writeFile(t, filepath.Join(b.net.root, kea6File), kea6Served)
	old := "server:\n\tlocal-data: \"nas.home.arpa. 1333 IN AAAA fd00:1::1002\"\n\tlocal-data-ptr: \"fd00:1::1002 1333 nas.home.arpa.\"\n"
	writeFile(t, filepath.Join(b.net.root, leasesDir, "leases.conf"), old)
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.leaseNames(), "laptop.home.arpa.") {
		t.Fatalf("DHCPv4 names lost:\n%s", b.leaseNames())
	}
	// Its own names stay as they were, not dropped with every client's.
	if !strings.Contains(b.leaseNames(), `"nas.home.arpa. 1333 IN AAAA fd00:1::1002"`) || !strings.Contains(b.leaseNames(), `"fd00:1::1002 1333 nas.home.arpa."`) {
		t.Fatalf("DHCPv6 names dropped:\n%s", b.leaseNames())
	}
}

// A DHCPv4 server that does not answer keeps its own names and still lets DHCPv6's be published.
func TestDHCPv6NamesDuringADeadDHCPv4(t *testing.T) {
	b := newBench(t)
	writeFile(t, filepath.Join(b.net.root, keaFile), leaseKea)
	old := "server:\n\tlocal-data: \"laptop.home.arpa. 2400 IN A 192.168.40.130\"\n\tlocal-data-ptr: \"192.168.40.130 2400 laptop.home.arpa.\"\n"
	writeFile(t, filepath.Join(b.net.root, leasesDir, "leases.conf"), old)
	b.kea6(lease6{Address: "fd00:1::1002", Type: "IA_NA", Hostname: "phone", Subnet: 1, Valid: 4000, Cltt: 100})
	if err := b.r.SyncLeases(t.Context()); err == nil {
		t.Fatal("a dead DHCPv4 server went unreported")
	}
	names := b.leaseNames()
	for _, want := range []string{`"phone.home.arpa. 1333 IN AAAA fd00:1::1002"`, `"laptop.home.arpa. 2400 IN A 192.168.40.130"`, `"192.168.40.130 2400 laptop.home.arpa."`} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing %s in:\n%s", want, names)
		}
	}
}

// A DHCPv6 sync that fails keeps its own names as published and still lets DHCPv4's be updated.
func TestDHCPv4NamesSurviveAFailedDHCPv6Sync(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop", 0, 1, 7200, 100})
	writeFile(t, filepath.Join(b.net.root, kea6File), "{")
	old := "server:\n\tlocal-data: \"nas.home.arpa. 1333 IN AAAA fd00:1::1002\"\n"
	writeFile(t, filepath.Join(b.net.root, leasesDir, "leases.conf"), old)
	if err := b.r.SyncLeases(t.Context()); err == nil {
		t.Fatal("the failed DHCPv6 sync went unreported")
	}
	names := b.leaseNames()
	for _, want := range []string{`"laptop.home.arpa. 2400 IN A 192.168.40.130"`, `"nas.home.arpa. 1333 IN AAAA fd00:1::1002"`} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing %s in:\n%s", want, names)
		}
	}
}

// A route whose install failed is retried by the once-a-minute prefix run, not at the router's next event.
func TestPDRouteRetriedByPrefixRun(t *testing.T) {
	b := newBench(t)
	b.kea6(pd("fd00:beef:1:100::/56"))
	b.events("add fd00:beef:1:100::/56 fe80::1 ens19")
	b.net.fail = "ip -6 route replace fd00:beef:1:100::/56"
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.net.fail, b.net.argv = "", nil
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 77") < 0 {
		t.Fatalf("failed install not retried: %q", b.net.argv)
	}
}

// A dynamic DHCPv4 lease cannot claim a name that is static in DHCPv6 only.
func TestDHCPv4NamesBlockedByStaticDHCPv6(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.131", "printer", 0, 1, 7200, 100}, lease{"192.168.40.130", "laptop", 0, 1, 7200, 100})
	b.kea6()
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if names := b.leaseNames(); strings.Contains(names, "192.168.40.131") || !strings.Contains(names, "laptop.home.arpa.") {
		t.Fatalf("a dynamic DHCPv4 lease took a static DHCPv6 name:\n%s", names)
	}
}

// Both families failing reports both failures.
func TestBothFamiliesFailing(t *testing.T) {
	b := newBench(t)
	writeFile(t, filepath.Join(b.net.root, keaFile), "{")
	writeFile(t, filepath.Join(b.net.root, kea6File), "{")
	err := b.r.SyncLeases(t.Context())
	if err == nil || !strings.Contains(err.Error(), "DHCP server's configuration") || !strings.Contains(err.Error(), "DHCPv6 server's configuration") {
		t.Fatalf("error %v", err)
	}
}

// Runs that change nothing replace no route and rewrite no next-hop table: each replace is a netlink event that
// can wake the prefix path unit again. A missing route is still put back.
func TestPDRoutesUnchangedRunsTouchNothing(t *testing.T) {
	b := newBench(t)
	b.filled(lease6{Address: "2001:db8:aa00:82::", PrefixLen: 64, Type: "IA_PD", Subnet: 7, Valid: 4000, Cltt: 100})
	writeFile(t, filepath.Join(b.net.root, pdTable), `{"2001:db8:aa00:82::/64":{"via":"fe80::1","dev":"ens19"}}`)
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(b.net.root, pdTable))
	b.net.argv = nil
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace") >= 0 {
		t.Fatalf("replaced unchanged routes: %q", b.net.argv)
	}
	if after, _ := os.Stat(filepath.Join(b.net.root, pdTable)); !os.SameFile(before, after) {
		t.Fatal("unchanged next-hop table rewritten")
	}
	delete(b.net.routes, "2001:db8:aa00:80::/60")
	delete(b.net.routes, "2001:db8:aa00:82::/64")
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ip -6 route replace unreachable 2001:db8:aa00:80::/60 proto 77", "ip -6 route replace 2001:db8:aa00:82::/64 via fe80::1 dev ens19 proto 77"} {
		if b.index(want) < 0 {
			t.Fatalf("missing route not put back (%s): %q", want, b.net.argv)
		}
	}
}

// An installed route whose router or interface no longer matches the believed next hop is replaced.
func TestPDRouteNextHopChanged(t *testing.T) {
	for name, installed := range map[string]string{"via": "fe80::1 dev ens19", "dev": "fe80::2 dev ens18"} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			b.filled(lease6{Address: "2001:db8:aa00:82::", PrefixLen: 64, Type: "IA_PD", Subnet: 7, Valid: 4000, Cltt: 100})
			writeFile(t, filepath.Join(b.net.root, pdTable), `{"2001:db8:aa00:82::/64":{"via":"fe80::2","dev":"ens19"}}`)
			b.net.routes = map[string]string{"2001:db8:aa00:80::/60": "unreachable", "2001:db8:aa00:82::/64": installed}
			if err := b.r.SyncLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			if b.index("ip -6 route replace 2001:db8:aa00:82::/64 via fe80::2 dev ens19 proto 77") < 0 {
				t.Fatalf("stale next hop kept: %q", b.net.argv)
			}
		})
	}
}

// When the lease of a pool handing out itself ends, its router's route becomes the pool's unreachable route
// again, and is not then deleted as a route nobody holds.
func TestPoolLeaseEndedBecomesUnreachable(t *testing.T) {
	b := newBench(t)
	b.filled()
	b.net.routes = map[string]string{"2001:db8:aa00:80::/60": "fe80::1 dev ens19"}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.routes["2001:db8:aa00:80::/60"] != "unreachable" || b.index("ip -6 route del 2001:db8:aa00:80::/60") >= 0 {
		t.Fatalf("pool route %q after %q", b.net.routes["2001:db8:aa00:80::/60"], b.net.argv)
	}
}

// A line the hook appends through a descriptor it opened before the sync moved the file still arrives.
func TestPDEventAppendedLate(t *testing.T) {
	b := newBench(t)
	b.kea6(pd("fd00:beef:1:100::/56"), pd("fd00:beef:1:200::/56"))
	path := b.events("add fd00:beef:1:100::/56 fe80::1 ens19")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("add fd00:beef:1:200::/56 fe80::2 ens19\n"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.net.root, leaseMarker), "")
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace fd00:beef:1:200::/56 via fe80::2 dev ens19 proto 77") < 0 {
		t.Fatalf("late line lost: %q", b.net.argv)
	}
}

// A sync that fails before it saves what the events taught leaves them for the next one.
func TestPDEventsSurviveAFailedSync(t *testing.T) {
	for name, fail := range map[string]func(b *bench) func(){
		"bad configuration": func(b *bench) func() {
			path := filepath.Join(b.net.root, kea6File)
			writeFile(b.t, path, "{")
			return func() { writeFile(b.t, path, kea6Served) }
		},
		"table not saved": func(b *bench) func() {
			path := filepath.Join(b.net.root, pdTable)
			if err := os.MkdirAll(filepath.Join(path, "x"), 0o755); err != nil {
				b.t.Fatal(err)
			}
			return func() { os.RemoveAll(path) }
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			b.kea6(pd("fd00:beef:1:100::/56"))
			b.events("add fd00:beef:1:100::/56 fe80::1 ens19")
			repair := fail(b)
			if err := b.r.SyncLeases(t.Context()); err == nil {
				t.Fatal("the failure went unreported")
			}
			repair()
			if err := b.r.SyncLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 77") < 0 {
				t.Fatalf("next hop lost with the failed sync: %q", b.net.argv)
			}
		})
	}
}

// Events taken while the DHCPv6 server does not answer keep their next hops for when it does.
func TestPDEventsSurviveADeadDHCPv6(t *testing.T) {
	b := newBench(t)
	writeFile(t, filepath.Join(b.net.root, kea6File), kea6Served)
	if err := os.MkdirAll(filepath.Join(b.net.root, "run/kea"), 0o750); err != nil || os.Chown(filepath.Join(b.net.root, "run/kea"), keaUser, -1) != nil {
		t.Fatal("Kea's directory")
	}
	b.events("add fd00:beef:1:100::/56 fe80::1 ens19")
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.kea6(pd("fd00:beef:1:100::/56"))
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 77") < 0 {
		t.Fatalf("next hop lost while the server was down: %q", b.net.argv)
	}
}

// A DHCPv4 server that does not answer still lets the sync remove the routes of a dropped DHCPv6.
func TestPDRoutesRemovedWithADeadDHCPv4(t *testing.T) {
	b := newBench(t)
	writeFile(t, filepath.Join(b.net.root, keaFile), leaseKea)
	b.net.routes = map[string]string{"fd00:beef:1:100::/56": "fe80::1 dev ens19"}
	if err := b.r.SyncLeases(t.Context()); err == nil {
		t.Fatal("a dead DHCPv4 server went unreported")
	}
	if b.index("ip -6 route del fd00:beef:1:100::/56 proto 77") < 0 {
		t.Fatalf("route of a dropped DHCPv6 kept: %q", b.net.argv)
	}
}

// Events taken in a sync that fails to list the routes keep their next hops for the next one.
func TestPDEventsSurviveAFailedRouteList(t *testing.T) {
	b := newBench(t)
	b.kea6(pd("fd00:beef:1:100::/56"))
	b.events("add fd00:beef:1:100::/56 fe80::1 ens19")
	b.net.fail = "ip -6 route show"
	if err := b.r.SyncLeases(t.Context()); err == nil {
		t.Fatal("a failed route list went unreported")
	}
	b.net.fail, b.net.argv = "", nil
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace fd00:beef:1:100::/56 via fe80::1 dev ens19 proto 77") < 0 {
		t.Fatalf("next hop lost with the failed sync: %q", b.net.argv)
	}
}

// A pool that hands out itself (delegated length = pool length) is routed to its router, not blackholed.
func TestPoolOfOnePrefixNotBlackholed(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	writeFile(t, filepath.Join(b.net.root, kea6Template), strings.Replace(templateBody, `"delegatedLength":64`, `"delegatedLength":60`, 1))
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	conf := b.keaFile()
	b.kea6(lease6{Address: "2001:db8:aa00:80::", PrefixLen: 60, Type: "IA_PD", Subnet: 7, Valid: 4000, Cltt: 100})
	writeFile(t, filepath.Join(b.net.root, kea6File), conf)
	b.events("add 2001:db8:aa00:80::/60 fe80::1 ens19")
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace unreachable 2001:db8:aa00:80::/60 proto 77") >= 0 {
		t.Fatalf("the leased prefix was blackholed: %q", b.net.argv)
	}
}

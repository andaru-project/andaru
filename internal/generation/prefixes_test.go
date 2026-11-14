package generation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/delegation"
)

const (
	wanNetwork = "[Network]\nDHCP=yes\n\n[DHCPv6]\nPrefixDelegationHint=::/48\n"
	lanNetwork = "[Network]\nAddress=fd00:a5d:1::1/64\nDHCPPrefixDelegation=yes\nIPv6SendRA=yes\n\n[DHCPPrefixDelegation]\nUplinkInterface=ens18\nSubnetId=0x1\nAssign=yes\nAnnounce=yes\n\n" +
		"[IPv6SendRA]\nRouterPreference=medium\nRouterLifetimeSec=1800\nManaged=yes\nOtherInformation=no\n"
	kea6Base     = `{"Dhcp6":{"interfaces-config":{"interfaces":["ens19"]},"subnet6":[]}}`
	templateBody = `{"subnets":[{"id":7,"interface":"ens19","uplink":"ens18","subnetId":"1",
	"ranges":[{"first":"::1000","last":"::1fff"}],
	"reservations":[{"duid":"00:01:00:01:aa:bb:cc:dd:ee:ff","address":"::10","prefixId":"1","hostname":"router"}],
	"pool":{"subnetId":"8","length":60,"delegatedLength":64},
	"valid-lifetime":7200,"preferred-lifetime":4500,"option-data":[],"user-context":{"andaru":{"domain":"home.arpa","register":true,"names":true}}}]}`
)

// keaSetter answers config-set on the DHCPv6 socket with result, recording the configurations it was sent.
type keaSetter struct {
	mu   sync.Mutex
	sent []string
}

func (k *keaSetter) count() int { k.mu.Lock(); defer k.mu.Unlock(); return len(k.sent) }
func (k *keaSetter) last() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.sent[len(k.sent)-1]
}

// delegatedLAN is a host whose generation has a delegated LAN (subnet 1 of ens18's prefix) served by DHCPv6,
// whose WAN holds prefix (none when empty), and whose Kea answers config-set with result (not running when negative).
func (b *bench) delegatedLAN(prefix string, result int) *keaSetter {
	b.t.Helper()
	writeFile(b.t, filepath.Join(b.net.root, netDir, "50-andaru-ens18.network"), wanNetwork)
	writeFile(b.t, filepath.Join(b.net.root, netDir, "50-andaru-ens19.network"), lanNetwork)
	writeFile(b.t, filepath.Join(b.net.root, kea6File), kea6Base)
	writeFile(b.t, filepath.Join(b.net.root, kea6Template), templateBody)
	b.prefix(prefix, 3600, 7200)
	k := &keaSetter{}
	if result < 0 {
		return k
	}
	dir := filepath.Join(b.net.root, "run/kea")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		b.t.Fatal(err)
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
			var req map[string]json.RawMessage
			_ = json.NewDecoder(bufio.NewReader(c)).Decode(&req)
			// The prefix run also asks for leases to reconcile routes; only config-set is counted.
			if string(req["command"]) == `"config-set"` {
				k.mu.Lock()
				k.sent = append(k.sent, string(req["arguments"]))
				k.mu.Unlock()
			}
			answer := result
			if b.keaRefuse != nil && b.keaRefuse.Load() {
				answer = 1
			}
			reply, _ := json.Marshal(map[string]any{"result": answer, "text": "refused"})
			c.Write(reply)
			c.Close()
		}
	}()
	return k
}

// prefix is networkd holding a delegated prefix: its unreachable route and the LAN's address in subnet 1.
func (b *bench) prefix(prefix string, preferred, valid int) {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	b.net.pd, b.net.addr6 = nil, nil
	if prefix == "" {
		return
	}
	b.addPrefix(prefix, preferred, valid)
}

func (b *bench) addPrefix(prefix string, preferred, valid int) {
	p, _ := delegation.Place(mustPrefix(prefix), 1, 64)
	b.net.pd = append(b.net.pd, prefix)
	b.net.addr6 = append(b.net.addr6, map[string]any{"local": delegation.Join(p, mustAddr("::1")).String(), "prefixlen": 64, "scope": "global",
		"preferred_life_time": preferred, "valid_life_time": valid})
}

func (b *bench) keaFile() string {
	data, _ := os.ReadFile(filepath.Join(b.net.root, kea6File))
	return string(data)
}

func (b *bench) prefixState() delegation.State {
	s, err := delegation.ReadState(filepath.Join(b.net.root, delegatedState))
	if err != nil {
		b.t.Fatal(err)
	}
	return s
}

func TestPrefixesFirstFill(t *testing.T) {
	b := newBench(t)
	k := b.delegatedLAN("2001:db8:aa00::/48", 0)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"subnet": "2001:db8:aa00:1::/64"`, `"pool": "2001:db8:aa00:1::1000 - 2001:db8:aa00:1::1fff"`, `"interfaces-config"`} {
		if !strings.Contains(b.keaFile(), want) {
			t.Fatalf("missing %s in:\n%s", want, b.keaFile())
		}
	}
	if k.count() != 1 || !strings.Contains(k.last(), `"2001:db8:aa00:1::/64"`) {
		t.Fatalf("config-set %d: %v", k.count(), k.sent)
	}
	w := b.prefixState().WANs["ens18"]
	if w.Requested != 48 || w.Current == nil || w.Current.Prefix != mustPrefix("2001:db8:aa00::/48") || !w.Current.Valid.Equal(b.now.Add(7200*time.Second)) || !w.Current.Preferred.Equal(b.now.Add(3600*time.Second)) {
		t.Fatalf("state %+v", w)
	}
}

func TestPrefixesUnchangedSendsNothing(t *testing.T) {
	b := newBench(t)
	k := b.delegatedLAN("2001:db8:aa00::/48", 0)
	for range 2 {
		if err := b.r.SyncPrefixes(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if k.count() != 1 {
		t.Fatalf("config-set sent %d times", k.count())
	}
}

// A healthy WAN renewing every 15 minutes (valid 3600 s, preferred 1800 s) reconfigures Kea once in an hour: the
// lease lifetimes do not follow the delegation's remaining time.
func TestPrefixesRenewingSendsNothing(t *testing.T) {
	b := newBench(t)
	k := b.delegatedLAN("2001:db8:aa00::/48", 0)
	for minute := range 60 {
		since := minute % 15 * 60
		b.prefix("2001:db8:aa00::/48", 1800-since, 3600-since)
		if err := b.r.SyncPrefixes(t.Context()); err != nil {
			t.Fatal(err)
		}
		b.now = b.now.Add(time.Minute)
	}
	if k.count() != 1 {
		t.Fatalf("config-set sent %d times", k.count())
	}
}

func TestPrefixesChange(t *testing.T) {
	b := newBench(t)
	k := b.delegatedLAN("2001:db8:aa00::/48", 0)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("2001:db8:bb00::/48", 3600, 7200)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if k.count() != 2 || !strings.Contains(k.last(), `"2001:db8:bb00:1::/64"`) || !strings.Contains(k.last(), `"id": 7`) {
		t.Fatalf("config-set %v", k.sent)
	}
	if w := b.prefixState().WANs["ens18"]; len(w.Replaced) != 1 || w.Replaced[0].Prefix != mustPrefix("2001:db8:aa00::/48") {
		t.Fatalf("state %+v", w)
	}
}

// Graceful renumbering: the old prefix stays valid but no longer preferred; the preferred one is served.
func TestPrefixesGracefulRenumbering(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", 0)
	b.addPrefix("2001:db8:bb00::/48", 3600, 7200)
	b.net.addr6[0]["preferred_life_time"] = 0
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.keaFile(), "2001:db8:bb00:1::/64") || strings.Contains(b.keaFile(), "2001:db8:aa00:1::/64") {
		t.Fatalf("served:\n%s", b.keaFile())
	}
}

// No prefix seen (boot, an outage): the saved one is served while valid, then the subnet is left out.
func TestPrefixesNoneSeenKeepsSaved(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", 0)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("", 0, 0)
	b.now = b.now.Add(time.Hour)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.keaFile(), "2001:db8:aa00:1::/64") {
		t.Fatalf("saved prefix dropped while valid:\n%s", b.keaFile())
	}
	b.now = b.now.Add(2 * time.Hour)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.keaFile(), "2001:db8:aa00:1::/64") {
		t.Fatalf("expired prefix still served:\n%s", b.keaFile())
	}
}

func TestPrefixesTooShort(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/56", 0)
	writeFile(t, filepath.Join(b.net.root, kea6Template), strings.Replace(templateBody, `"subnetId":"1",`, `"subnetId":"100",`, 1))
	logs := captureLogs(t)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.keaFile(), "2001:db8:aa00") || !strings.Contains(logs.String(), "ens19") {
		t.Fatalf("served:\n%s\nlogs:\n%s", b.keaFile(), logs)
	}
}

// A configuration Kea refuses fails the step and leaves Kea's file as it was.
func TestPrefixesRefusedConfigSet(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", 1)
	if err := b.r.SyncPrefixes(t.Context()); err == nil {
		t.Fatal("a refused configuration went unreported")
	}
	if b.keaFile() != kea6Base {
		t.Fatalf("file changed:\n%s", b.keaFile())
	}
}

// Kea not running yet: the file is written, and Kea's start reads it.
func TestPrefixesKeaNotRunning(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.keaFile(), "2001:db8:aa00:1::/64") {
		t.Fatalf("not filled:\n%s", b.keaFile())
	}
}

// The apply fills Kea's file from what networkd holds before Kea checks and restarts on it.
func TestActivateFillsDelegatedSubnets(t *testing.T) {
	b := newBench(t)
	b.prefix("2001:db8:aa00::/48", 3600, 7200)
	var filled bool
	b.net.onRun = func(command string) {
		if command == "kea-dhcp6 -t "+kea6File {
			data, _ := os.ReadFile(filepath.Join(b.net.root, kea6File))
			filled = strings.Contains(string(data), "2001:db8:aa00:1::/64")
		}
	}
	writeFile(t, filepath.Join(b.net.root, netDir, "50-andaru-ens18.network"), wanNetwork)
	writeFile(t, filepath.Join(b.net.root, netDir, "50-andaru-ens19.network"), lanNetwork)
	id := b.gen(false, map[string]string{kea6File: kea6Base, kea6Template: templateBody})
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if !filled {
		t.Fatalf("Kea checked an unfilled file: %q", b.net.argv)
	}
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }
func mustAddr(s string) netip.Addr     { return netip.MustParseAddr(s) }

// captureLogs sends the default logger to a buffer for the test.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// filled is a delegated LAN filled with 2001:db8:aa00::/48 and Kea answering lease6-get-all with leases.
func (b *bench) filled(leases ...lease6) {
	b.t.Helper()
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(b.t.Context()); err != nil {
		b.t.Fatal(err)
	}
	conf := b.keaFile()
	b.kea6(leases...)
	writeFile(b.t, filepath.Join(b.net.root, kea6File), conf) // kea6 writes its own static configuration
}

func TestPoolRouteInstalled(t *testing.T) {
	b := newBench(t)
	b.filled()
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace unreachable 2001:db8:aa00:80::/60 proto 77") < 0 {
		t.Fatalf("pool route not installed: %q", b.net.argv)
	}
}

// The sweep of protocol 77 keeps the pool's unreachable route and the leased prefixes' routes, and parses both.
func TestLeaseSyncKeepsPoolRoute(t *testing.T) {
	b := newBench(t)
	b.filled(lease6{Address: "2001:db8:aa00:82::", PrefixLen: 64, Type: "IA_PD", Subnet: 7, Valid: 4000, Cltt: 100})
	writeFile(t, filepath.Join(b.net.root, pdTable), `{"2001:db8:aa00:82::/64":{"via":"fe80::1","dev":"ens19"}}`)
	b.net.routes = map[string]string{"2001:db8:aa00:80::/60": "unreachable", "2001:db8:aa00:82::/64": "fe80::1 dev ens19", "2001:db8:dead:80::/60": "unreachable"}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"2001:db8:aa00:80::/60", "2001:db8:aa00:82::/64"} {
		if b.index("ip -6 route del "+kept+" proto 77") >= 0 || b.index("ip -6 route del unreachable "+kept+" proto 77") >= 0 {
			t.Fatalf("%s removed: %q", kept, b.net.argv)
		}
	}
	if b.index("ip -6 route del unreachable 2001:db8:dead:80::/60 proto 77") < 0 {
		t.Fatalf("a pool no longer filled kept its route: %q", b.net.argv)
	}
}

// A fixed prefix (prefixId) is routed like a dynamic one once leased.
func TestFixedPrefixRouted(t *testing.T) {
	b := newBench(t)
	b.filled(lease6{Address: "2001:db8:aa00:81::", PrefixLen: 64, Type: "IA_PD", Subnet: 7, Valid: 4000, Cltt: 100})
	b.events("add 2001:db8:aa00:81::/64 fe80::1 ens19")
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("ip -6 route replace 2001:db8:aa00:81::/64 via fe80::1 dev ens19 proto 77") < 0 {
		t.Fatalf("fixed prefix not routed: %q", b.net.argv)
	}
	if !strings.Contains(b.keaFile(), `"2001:db8:aa00:81::/64"`) {
		t.Fatalf("fixed prefix not reserved:\n%s", b.keaFile())
	}
}

// A static name on a delegated segment is published at its current address, and moves with the prefix.
func TestStaticNameOnDelegated(t *testing.T) {
	b := newBench(t)
	b.filled()
	// One device, its static name in both families: the DHCPv4 one does not hold back the DHCPv6 one.
	b.kea()
	writeFile(t, filepath.Join(b.net.root, keaFile), strings.Replace(leaseKea, `"nas"`, `"router"`, 1))
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"router.home.arpa. 3600 IN AAAA 2001:db8:aa00:1::10"`, `"2001:db8:aa00:1::10 3600 router.home.arpa."`} {
		if !strings.Contains(b.leaseNames(), want) {
			t.Fatalf("missing %s in:\n%s", want, b.leaseNames())
		}
	}
	b.prefix("2001:db8:bb00::/48", 3600, 7200)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.leaseNames(), "AAAA 2001:db8:bb00:1::10") || strings.Contains(b.leaseNames(), "2001:db8:aa00") {
		t.Fatalf("name did not move:\n%s", b.leaseNames())
	}
}

// With the prefix expired, the delegated subnet is left out of Kea's file, but its routers' next hops stay for
// when it returns.
func TestNextHopsSurviveExpiredPrefix(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("", -1)
	writeFile(t, filepath.Join(b.net.root, pdTable), `{"2001:db8:aa00:82::/64":{"via":"fe80::1","dev":"ens19"}}`)
	b.kea6()
	writeFile(t, filepath.Join(b.net.root, kea6File), kea6Base)
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hop := b.table()["2001:db8:aa00:82::/64"]; hop.Via != "fe80::1" {
		t.Fatalf("next hop dropped: %+v", b.table())
	}
}

// A changed Kea configuration starts the lease sync, which moves names and routes.
func TestPrefixChangeStartsLeaseSync(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", 0)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("systemctl start --no-block andaru-leases.service") < 0 {
		t.Fatalf("lease sync not started: %q", b.net.argv)
	}
}

// A route networkd holds for another DHCPv6 WAN's prefix is never served on this WAN's LAN.
func TestPrefixesForeignRouteIgnored(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	writeFile(t, filepath.Join(b.net.root, netDir, "50-andaru-ens21.network"), "[Network]\nDHCP=yes\n\n[DHCPv6]\nUseDNS=no\n")
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.net.addr6[0]["preferred_life_time"] = 0 // an outage on this WAN
	b.net.pd = append(b.net.pd, "2001:db8:ffff::/48")
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.keaFile(), "2001:db8:ffff") || b.prefixState().WANs["ens18"].Current.Prefix != mustPrefix("2001:db8:aa00::/48") {
		t.Fatalf("another WAN's prefix served:\n%s", b.keaFile())
	}
}

// A refused configuration is offered again on the next run; the change it carries is deprecated and saved even
// while Kea refuses.
func TestPrefixesRefusedThenRetried(t *testing.T) {
	b := newBench(t)
	s := &sent{}
	b.r.SendRA = s.record
	k := b.delegatedLAN("2001:db8:aa00::/48", 0)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("2001:db8:bb00::/48", 3600, 7200)
	b.keaRefuse = &atomic.Bool{}
	b.keaRefuse.Store(true)
	if err := b.r.SyncPrefixes(t.Context()); err == nil {
		t.Fatal("refusal went unreported")
	}
	if got := s.list(); len(got) != 1 || !strings.HasPrefix(got[0], "ens19 2001:db8:aa00:1::/64") {
		t.Fatalf("not deprecated while Kea refuses: %v", got)
	}
	if c := b.prefixState().WANs["ens18"].Current; c == nil || c.Prefix != mustPrefix("2001:db8:bb00::/48") {
		t.Fatalf("state not saved while Kea refuses: %+v", c)
	}
	b.keaRefuse.Store(false)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.keaFile(), "2001:db8:bb00:1::/64") || k.count() != 3 {
		t.Fatalf("not retried: %d config-sets\n%s", k.count(), b.keaFile())
	}
}

// A change first seen by an apply is deprecated by the next prefix sync.
func TestChangeSeenByApplyIsDeprecated(t *testing.T) {
	b := newBench(t)
	s := &sent{}
	b.r.SendRA = s.record
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("2001:db8:bb00::/48", 3600, 7200)
	if err := b.r.syncPrefixes(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := s.list(); len(got) != 1 || !strings.HasPrefix(got[0], "ens19 2001:db8:aa00:1::/64") {
		t.Fatalf("sent %v", got)
	}
}

// The deprecation never extends an old address: at most two hours, as RFC 4862 lets a router shorten it.
func TestDeprecationValidCapped(t *testing.T) {
	b := newBench(t)
	s := &sent{}
	b.r.SendRA = s.record
	b.delegatedLAN("", -1)
	b.prefix("2001:db8:aa00::/48", 3600, 86400)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("2001:db8:bb00::/48", 3600, 86400)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := s.list(); len(got) != 1 || got[0] != "ens19 2001:db8:aa00:1::/64 valid 7200" {
		t.Fatalf("sent %v", got)
	}
}

// Boot fills Kea's file from the saved prefix before networkd and Kea start.
func TestBootFillsDelegatedSubnets(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.live(map[string]string{"50-andaru-ens18.network": wanNetwork, "50-andaru-ens19.network": lanNetwork, kea6File: kea6Base, kea6Template: templateBody})
	b.prefix("", 0, 0)
	if err := os.Remove(filepath.Join(b.net.root, kea6File)); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.keaFile(), "2001:db8:aa00:1::/64") {
		t.Fatalf("boot left Kea without the delegated subnet:\n%s", b.keaFile())
	}
}

// The apply checks the delegated subnets with Kea's checker, filled with a documentation prefix.
func TestActivateChecksTemplate(t *testing.T) {
	b := newBench(t)
	writeFile(t, filepath.Join(b.net.root, netDir, "50-andaru-ens18.network"), wanNetwork)
	writeFile(t, filepath.Join(b.net.root, netDir, "50-andaru-ens19.network"), lanNetwork)
	var checked string
	b.net.onRun = func(command string) {
		if command == "kea-dhcp6 -t "+kea6Check {
			data, _ := os.ReadFile(filepath.Join(b.net.root, kea6Check))
			checked = string(data)
		}
	}
	if err := b.r.Activate(t.Context(), b.gen(false, map[string]string{kea6File: kea6Base, kea6Template: templateBody})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(checked, `"subnet": "2001:db8:0:1::/64"`) {
		t.Fatalf("template not checked: %q", checked)
	}
	b.net.fail, b.net.failWith = "kea-dhcp6 -t "+kea6Check, "Dhcp6 bad pool"
	if err := b.r.Activate(t.Context(), b.gen(false, map[string]string{kea6File: kea6Base, kea6Template: templateBody})); err == nil {
		t.Fatal("a template Kea refuses was applied")
	}
}

// No delegated segment: the timer's run touches nothing.
func TestPrefixesIdle(t *testing.T) {
	b := newBench(t)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.net.argv) != 0 {
		t.Fatalf("ran %q", b.net.argv)
	}
	if _, err := os.Stat(filepath.Join(b.net.root, delegatedState)); !os.IsNotExist(err) {
		t.Fatal("state written with nothing delegated")
	}
}

// After a prefix change the old pool's leases are Kea's until their routers renew: their routes stay, and the old
// pool stays unreachable, while the replaced prefix is valid.
func TestOldPoolRoutedWhileValid(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("2001:db8:bb00::/48", 3600, 7200)
	b.r.SendRA = (&sent{}).record
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	conf := b.keaFile()
	b.kea6(lease6{Address: "2001:db8:aa00:82::", PrefixLen: 64, Type: "IA_PD", Subnet: 7, Valid: 4000, Cltt: 100})
	writeFile(t, filepath.Join(b.net.root, kea6File), conf)
	writeFile(t, filepath.Join(b.net.root, pdTable), `{"2001:db8:aa00:82::/64":{"via":"fe80::1","dev":"ens19"}}`)
	b.net.argv = nil
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ip -6 route replace 2001:db8:aa00:82::/64 via fe80::1 dev ens19 proto 77", "ip -6 route replace unreachable 2001:db8:aa00:80::/60 proto 77"} {
		if b.index(want) < 0 {
			t.Fatalf("missing %q: %q", want, b.net.argv)
		}
	}
}

// Back to the earlier prefix within the window: its /64 is current again and is not advertised deprecated.
func TestPrefixBackIsNotDeprecated(t *testing.T) {
	b := newBench(t)
	s := &sent{}
	b.r.SendRA = s.record
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	for _, p := range []string{"", "2001:db8:bb00::/48", "2001:db8:aa00::/48"} {
		if p != "" {
			b.prefix(p, 3600, 7200)
		}
		if err := b.r.SyncPrefixes(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	got := s.list()
	if len(got) == 0 || strings.HasPrefix(got[len(got)-1], "ens19 2001:db8:aa00:1::/64") && len(got) > 1 && strings.HasPrefix(got[len(got)-2], "ens19 2001:db8:aa00:1::/64") {
		t.Fatalf("sent %v", got)
	}
	for _, line := range got[1:] {
		if strings.HasPrefix(line, "ens19 2001:db8:aa00:1::/64") {
			t.Fatalf("the current /64 was deprecated: %v", got)
		}
	}
}

// A fresh change waits for networkd's own advertisement even when the clock moves during the run.
func TestDeprecationWaitsWithMovingClock(t *testing.T) {
	b := newBench(t)
	b.r.SendRA = (&sent{}).record
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("2001:db8:bb00::/48", 3600, 7200)
	start := b.now
	b.r.Now = func() time.Time { start = start.Add(time.Millisecond); return start }
	b.r.DeprecateAfter = 50 * time.Millisecond
	began := time.Now()
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if time.Since(began) < 50*time.Millisecond {
		t.Fatal("sent without waiting for networkd's advertisement")
	}
}

// The current prefix's lifetimes follow what networkd reports, also when it stops being preferred.
func TestCurrentPrefixLifetimesRefreshed(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("2001:db8:aa00::/48", 0, 300)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.prefix("", 0, 0)
	b.now = b.now.Add(10 * time.Minute)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.keaFile(), "2001:db8:aa00:1::/64") {
		t.Fatalf("an expired prefix is still served:\n%s", b.keaFile())
	}
}

// Every replaced prefix still valid keeps its pool routed, not only the last one.
func TestTwoChangesKeepBothOldPools(t *testing.T) {
	b := newBench(t)
	b.r.SendRA = (&sent{}).record
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	for _, p := range []string{"", "2001:db8:bb00::/48", "2001:db8:cc00::/48"} {
		if p != "" {
			b.prefix(p, 3600, 7200)
		}
		if err := b.r.SyncPrefixes(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	conf := b.keaFile()
	b.kea6()
	writeFile(t, filepath.Join(b.net.root, kea6File), conf)
	b.net.argv = nil
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, pool := range []string{"2001:db8:aa00:80::/60", "2001:db8:bb00:80::/60"} {
		if b.index("ip -6 route replace unreachable "+pool+" proto 77") < 0 {
			t.Fatalf("%s dropped: %q", pool, b.net.argv)
		}
	}
}

// A delegation longer than /64 can number nothing; it is not taken as the WAN's prefix.
func TestPrefixLongerThan64Ignored(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("", -1)
	b.net.pd = []string{"2001:db8:aa00::/80"}
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w := b.prefixState().WANs["ens18"]; w.Current != nil {
		t.Fatalf("took %v", w.Current.Prefix)
	}
	if _, err := ReadPrefixView(b.net.root); err != nil {
		t.Fatal(err)
	}
}

// A damaged template makes the view fail instead of omitting the pools.
func TestPrefixViewDamagedTemplate(t *testing.T) {
	b := newBench(t)
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	writeFile(t, filepath.Join(b.net.root, kea6Template), "{")
	if _, err := ReadPrefixView(b.net.root); err == nil {
		t.Fatal("a damaged template went unreported")
	}
}

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
	"valid-lifetime":7200,"preferred-lifetime":4500,"option-data":[],"user-context":{"andaru":{"domain":"home.arpa","register":true}}}]}`
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
			k.mu.Lock()
			k.sent = append(k.sent, string(req["arguments"]))
			k.mu.Unlock()
			reply, _ := json.Marshal(map[string]any{"result": result, "text": "refused"})
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
	if w := b.prefixState().WANs["ens18"]; w.Last == nil || w.Last.Prefix != mustPrefix("2001:db8:aa00::/48") {
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

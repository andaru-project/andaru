package generation

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdvertisementBytes(t *testing.T) {
	old := netip.MustParsePrefix("2001:db8:aa00:1::/64")
	b := advertisement(old, 5400, 1800, true, false, "high")
	if len(b) != 48 || b[0] != 134 || b[1] != 0 || b[4] != 64 {
		t.Fatalf("header % x", b)
	}
	// M set, O clear, preference high (01) in bits 4-3.
	if b[5] != 0x80|0x08 || binary.BigEndian.Uint16(b[6:]) != 1800 {
		t.Fatalf("flags %08b lifetime %d", b[5], binary.BigEndian.Uint16(b[6:]))
	}
	opt := b[16:]
	if opt[0] != 3 || opt[1] != 4 || opt[2] != 64 || opt[3] != 0xc0 {
		t.Fatalf("prefix option % x", opt[:4])
	}
	if binary.BigEndian.Uint32(opt[4:]) != 5400 || binary.BigEndian.Uint32(opt[8:]) != 0 || netip.AddrFrom16([16]byte(opt[16:32])) != old.Addr() {
		t.Fatalf("prefix option % x", opt)
	}
	if b := advertisement(old, 7200, 0, false, true, "low"); b[5] != 0x40|0x18 {
		t.Fatalf("flags %08b", b[5])
	}
}

// sent records the advertisements a bench would send, by interface.
type sent struct {
	mu  sync.Mutex
	got []string
}

func (s *sent) record(ifname string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	opt := body[16:]
	s.got = append(s.got, ifname+" "+netip.PrefixFrom(netip.AddrFrom16([16]byte(opt[16:32])), int(opt[2])).String()+" valid "+itoa(int(binary.BigEndian.Uint32(opt[4:]))))
	return nil
}

func (s *sent) list() []string { s.mu.Lock(); defer s.mu.Unlock(); return append([]string{}, s.got...) }

// A replaced /64 is advertised deprecated at each run for ten minutes, then no more.
func TestDeprecationWindow(t *testing.T) {
	b := newBench(t)
	s := &sent{}
	b.r.SendRA = s.record
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.list()) != 0 {
		t.Fatalf("sent with nothing replaced: %v", s.list())
	}
	b.prefix("2001:db8:bb00::/48", 3600, 7200)
	b.now = b.now.Add(time.Minute) // the old prefix has 7140 s of its 7200 left
	for range 3 {
		if err := b.r.SyncPrefixes(t.Context()); err != nil {
			t.Fatal(err)
		}
		b.now = b.now.Add(time.Minute)
	}
	if got := s.list(); len(got) != 3 || got[0] != "ens19 2001:db8:aa00:1::/64 valid 7140" {
		t.Fatalf("sent %v", got)
	}
	b.now = b.now.Add(10 * time.Minute)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.list()) != 3 {
		t.Fatalf("sent after the window: %v", s.list())
	}
}

// A segment whose advertisements announce no prefix (mode router, or none) sends nothing.
func TestDeprecationOnlyAnnouncing(t *testing.T) {
	for name, edit := range map[string]func(string) string{
		"router":   func(f string) string { return strings.Replace(f, "Announce=yes", "Announce=no", 1) },
		"disabled": func(f string) string { return strings.Replace(f, "IPv6SendRA=yes", "IPv6SendRA=no", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			s := &sent{}
			b.r.SendRA = s.record
			b.delegatedLAN("2001:db8:aa00::/48", -1)
			writeFile(t, b.net.root+netDir+"50-andaru-ens19.network", edit(lanNetwork))
			if err := b.r.SyncPrefixes(t.Context()); err != nil {
				t.Fatal(err)
			}
			b.prefix("2001:db8:bb00::/48", 3600, 7200)
			if err := b.r.SyncPrefixes(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(s.list()) != 0 {
				t.Fatalf("sent %v", s.list())
			}
		})
	}
}

// Graceful renumbering: the old prefix, still valid but no longer preferred, is deprecated.
func TestDeprecationAfterGracefulRenumbering(t *testing.T) {
	b := newBench(t)
	s := &sent{}
	b.r.SendRA = s.record
	b.delegatedLAN("2001:db8:aa00::/48", -1)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.net.addr6[0]["preferred_life_time"] = 0
	b.addPrefix("2001:db8:bb00::/48", 3600, 7200)
	if err := b.r.SyncPrefixes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := s.list(); len(got) != 1 || !strings.HasPrefix(got[0], "ens19 2001:db8:aa00:1::/64") {
		t.Fatalf("sent %v", got)
	}
}

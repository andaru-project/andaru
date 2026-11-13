package generation

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/andaru-project/andaru/internal/delegation"
)

const (
	// replacedPrefixes are the /64s delegated segments lost, advertised deprecated for deprecationWindow.
	replacedPrefixes  = "/var/lib/andaru/replaced-prefixes.json"
	deprecationWindow = 10 * time.Minute
)

// replaced is a /64 a delegated segment had and no longer has.
type replaced struct {
	Interface string       `json:"interface"`
	Prefix    netip.Prefix `json:"prefix"`
	Seen      time.Time    `json:"seen"`
	Valid     time.Time    `json:"valid"`
}

// advertisement is a router advertisement listing old with preferred lifetime 0: clients stop choosing their
// addresses from it. Its router fields repeat networkd's, so clients keep their default route as they were.
// The kernel fills the checksum on a raw ICMPv6 socket.
func advertisement(old netip.Prefix, valid uint32, routerLifetime uint16, managed, other bool, preference string) []byte {
	b := make([]byte, 48)
	b[0], b[4] = 134, 64
	if managed {
		b[5] |= 0x80
	}
	if other {
		b[5] |= 0x40
	}
	b[5] |= map[string]byte{"high": 0x08, "low": 0x18}[preference]
	binary.BigEndian.PutUint16(b[6:], routerLifetime)
	opt := b[16:]
	opt[0], opt[1], opt[2], opt[3] = 3, 4, byte(old.Bits()), 0xc0 // on-link and autonomous
	binary.BigEndian.PutUint32(opt[4:], valid)
	a := old.Masked().Addr().As16()
	copy(opt[16:], a[:])
	return b
}

// deprecate records the /64s the delegated segments lost since the last run and advertises every recorded one
// still in its window, on segments that announce their delegated prefix.
func (r Realizer) deprecate(links []delegatedLink, before, after delegation.State) error {
	var list []replaced
	if data, err := os.ReadFile(r.Root + replacedPrefixes); err == nil {
		_ = json.Unmarshal(data, &list) // a damaged list only costs deprecations already under way
	}
	now := r.Now()
	for _, l := range links {
		old, new := before.WANs[l.Uplink].Current, after.WANs[l.Uplink].Current
		if old == nil || new != nil && new.Prefix == old.Prefix || !old.Valid.After(now) {
			continue
		}
		if p, ok := delegation.Place(old.Prefix, l.SubnetID, 64); ok {
			list = append(list, replaced{l.Interface, p, now, old.Valid})
		}
	}
	send := r.SendRA
	if send == nil {
		send = sendRA
	}
	var kept []replaced
	for _, d := range list {
		if now.Sub(d.Seen) > deprecationWindow || !d.Valid.After(now) {
			continue
		}
		kept = append(kept, d)
		for _, l := range links {
			if l.Interface != d.Interface || !l.Announce {
				continue
			}
			valid := min(7200, uint32(d.Valid.Sub(now)/time.Second))
			if err := send(l.Interface, advertisement(d.Prefix, valid, l.RouterLifetime, l.Managed, l.Other, l.Preference)); err != nil {
				slog.Warn("deprecate a replaced prefix", "interface", l.Interface, "prefix", d.Prefix, "error", err)
			}
		}
	}
	data, _ := json.Marshal(kept)
	return writeAtomic(r.Root+replacedPrefixes, data, 0o644)
}

// sendRA sends an ICMPv6 message to all nodes on ifname from its link-local address.
func sendRA(ifname string, body []byte) error {
	ifc, err := net.InterfaceByName(ifname)
	if err != nil {
		return err
	}
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.IPPROTO_ICMPV6)
	if err != nil {
		return fmt.Errorf("open an ICMPv6 socket: %w", err)
	}
	defer unix.Close(fd)
	// RFC 4861: hosts ignore an advertisement whose hop limit is not 255.
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_HOPS, 255); err != nil {
		return err
	}
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_IF, ifc.Index); err != nil {
		return err
	}
	if err := unix.BindToDevice(fd, ifname); err != nil {
		return err
	}
	to := &unix.SockaddrInet6{ZoneId: uint32(ifc.Index), Addr: netip.MustParseAddr("ff02::1").As16()}
	return unix.Sendto(fd, body, 0, to)
}

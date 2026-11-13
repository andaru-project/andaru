//go:build integration

package generation

import (
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The real sender puts a router advertisement on the link with hop limit 255, from the link-local address,
// as hosts require (RFC 4861).
func TestSendRAOnTheWire(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root for a veth pair and raw sockets")
	}
	for _, args := range [][]string{
		{"link", "add", "rav0", "type", "veth", "peer", "name", "rav1"},
		{"-6", "addr", "add", "fe80::a:1/64", "dev", "rav0", "nodad"},
		{"link", "set", "rav0", "up"}, {"link", "set", "rav1", "up"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %s", args, out)
		}
	}
	t.Cleanup(func() { exec.Command("ip", "link", "del", "rav0").Run() })
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_RAW, unix.IPPROTO_ICMPV6)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.BindToDevice(fd, "rav1"); err != nil {
		t.Fatal(err)
	}
	_ = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_RECVHOPLIMIT, 1)
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5})
	time.Sleep(500 * time.Millisecond) // the peer's link-local address and multicast membership
	old := netip.MustParsePrefix("2001:db8:aa00:1::/64")
	if err := sendRA("rav0", advertisement(old, 3600, 1800, false, false, "medium")); err != nil {
		t.Fatal(err)
	}
	buf, oob := make([]byte, 1500), make([]byte, 128)
	for {
		n, oobn, _, from, err := unix.Recvmsg(fd, buf, oob, 0)
		if err != nil {
			t.Fatalf("no advertisement arrived: %v", err)
		}
		if n < 48 || buf[0] != 134 {
			continue // the kernel's own neighbour discovery
		}
		src := netip.AddrFrom16(from.(*unix.SockaddrInet6).Addr)
		msgs, _ := unix.ParseSocketControlMessage(oob[:oobn])
		hops := -1
		for _, m := range msgs {
			if m.Header.Level == unix.IPPROTO_IPV6 && m.Header.Type == unix.IPV6_HOPLIMIT {
				hops = int(m.Data[0])
			}
		}
		if hops != 255 || src != netip.MustParseAddr("fe80::a:1") || netip.AddrFrom16([16]byte(buf[32:48])) != old.Addr() {
			t.Fatalf("hop limit %d from %s: % x", hops, src, buf[:n])
		}
		return
	}
}

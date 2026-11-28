package generation

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

const wgPeerPSK = "50-andaru-wg0-wgp_00000001-0000-4000-8000-000000000001.psk"

// An idle interface: no peer has a handshake, and networkd still reports it configured with its address.
var withWireGuard = map[string]string{
	"50-andaru-eth0.network": uplinkStatic,
	"50-andaru-wg0.netdev":   "[NetDev]\nName=wg0\nKind=wireguard\nMTUBytes=1420\n\n[WireGuard]\nListenPort=51820\nPrivateKeyFile=/run/systemd/network/50-andaru-wg0.key\n",
	"50-andaru-wg0.network":  "[Match]\nName=wg0\n\n[Network]\nAddress=10.200.0.1/24\nIPv4Forwarding=yes\nIPv6Forwarding=yes\n",
	"50-andaru-wg0.key":      "KEY\n",
	wgPeerPSK:                "PSK\n",
}

func TestWireGuardKeyPaths(t *testing.T) {
	for path, mode := range map[string]string{
		"/run/systemd/network/50-andaru-wg0.key":    "0640",
		"/run/systemd/network/50-andaru-wg99.key":   "0640",
		"/run/systemd/network/" + wgPeerPSK:         "0640",
		"/run/systemd/network/50-andaru-wg0.netdev": "0644",
	} {
		if !allowed(path) || modeFor(path) != mode {
			t.Errorf("%s: allowed %v mode %s", path, allowed(path), modeFor(path))
		}
	}
	for _, path := range []string{"/run/systemd/network/50-andaru-eth0.key", "/run/systemd/network/50-andaru-wg100.key", "/run/systemd/network/50-andaru-wg0-x.psk", "/run/systemd/network/50-andaru-wg0.psk"} {
		if allowed(path) {
			t.Errorf("%s allowed", path)
		}
	}
}

func TestWireGuardKeysPlacedForNetworkd(t *testing.T) {
	b := newBench(t)
	b.r.networkGID = func() (int, error) { return 998, nil }
	b.live(one)
	id := b.gen(withWireGuard)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(withWireGuard)
	for _, name := range []string{"50-andaru-wg0.key", wgPeerPSK} {
		info, err := os.Stat(filepath.Join(b.net.root, netDir, name))
		if err != nil || info.Mode().Perm() != fs.FileMode(0o640) {
			t.Fatalf("%s: %v %v", name, info.Mode(), err)
		}
		if !slices.Contains(b.net.chowns, netDir+name+" 0:998") {
			t.Fatalf("%s not given to systemd-network: %q", name, b.net.chowns)
		}
	}
	if info, _ := os.Stat(filepath.Join(b.net.root, netDir, "50-andaru-wg0.netdev")); info.Mode().Perm() != 0o644 {
		t.Fatalf("netdev mode %v", info.Mode())
	}
	// Rolled back, the keys go and the interface is deleted.
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	if b.net.ran("networkctl delete wg0") != 1 {
		t.Fatalf("argv = %q", b.net.argv)
	}
}

func TestWireGuardGroupFromNetworkdRuntime(t *testing.T) {
	b := newBench(t)
	dir := filepath.Join(b.net.root, "run/systemd/netif")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	gid, err := b.r.networkGroup()
	if err != nil || uint32(gid) != info.Sys().(*syscall.Stat_t).Gid {
		t.Fatalf("gid %d %v", gid, err)
	}
}

// A factory reset's teardown removes the interface's files, keys included, and the interface.
func TestTeardownRemovesWireGuard(t *testing.T) {
	b := newBench(t)
	b.r.networkGID = func() (int, error) { return 998, nil }
	id := b.gen(maps.Clone(withWireGuard))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name := range b.net.netFiles() {
		if strings.HasPrefix(name, "50-andaru-wg") {
			t.Fatalf("%s left in /run", name)
		}
	}
	if b.index("networkctl delete wg0") < 0 {
		t.Fatalf("wg0 not deleted: %q", b.net.argv)
	}
}

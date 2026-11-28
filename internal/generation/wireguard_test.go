package generation

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
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
	}
	chowned := 0
	for _, entry := range b.net.chowns {
		if strings.HasPrefix(entry, netDir+".tmp-") && strings.HasSuffix(entry, " 0:998") {
			chowned++
		}
	}
	if chowned != 2 {
		t.Fatalf("expected 2 pre-rename chowns to 0:998, got %q", b.net.chowns)
	}
	if info, _ := os.Stat(filepath.Join(b.net.root, netDir, "50-andaru-wg0.netdev")); info.Mode().Perm() != 0o644 {
		t.Fatalf("netdev mode %v", info.Mode())
	}
	// Editing a live WireGuard interface's .netdev/.key/.psk deletes it before networkctl reload so stale peers/keys are dropped,
	// while an untouched WireGuard interface is not deleted.
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	withTwo := maps.Clone(withWireGuard)
	withTwo["50-andaru-wg1.netdev"] = "[NetDev]\nName=wg1\nKind=wireguard\nMTUBytes=1420\n\n[WireGuard]\nListenPort=51821\nPrivateKeyFile=/run/systemd/network/50-andaru-wg1.key\n"
	withTwo["50-andaru-wg1.network"] = "[Match]\nName=wg1\n\n[Network]\nAddress=10.201.0.1/24\nIPv4Forwarding=yes\nIPv6Forwarding=yes\n"
	withTwo["50-andaru-wg1.key"] = "KEY1\n"
	idTwo := b.gen(withTwo)
	if err := b.r.Activate(t.Context(), idTwo); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), idTwo); err != nil {
		t.Fatal(err)
	}
	updated := maps.Clone(withTwo)
	updated["50-andaru-wg0.key"] = "ROTATED_KEY\n"
	b.net.argv = nil
	idUpdated := b.gen(updated)
	if err := b.r.Activate(t.Context(), idUpdated); err != nil {
		t.Fatal(err)
	}
	delIdx := b.index("networkctl delete wg0")
	reloadIdx := b.index("networkctl reload")
	if delIdx < 0 || reloadIdx < 0 || delIdx > reloadIdx {
		t.Fatalf("expected networkctl delete wg0 before reload, argv = %q", b.net.argv)
	}
	if b.net.ran("networkctl delete wg1") != 0 {
		t.Fatalf("untouched wg1 must not be deleted, argv = %q", b.net.argv)
	}
	// Rolled back, wg0 is recreated back to the previous key.
	b.net.argv = nil
	if err := b.timerFires(idUpdated); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(withTwo)
	if b.net.ran("networkctl delete wg0") != 1 {
		t.Fatalf("rollback did not recreate wg0: argv = %q", b.net.argv)
	}
	// Rolling back from a single-WG generation to none removes the files and deletes wg0.
	b2 := newBench(t)
	b2.r.networkGID = func() (int, error) { return 998, nil }
	b2.live(one)
	idDel := b2.gen(withWireGuard)
	if err := b2.r.Activate(t.Context(), idDel); err != nil {
		t.Fatal(err)
	}
	if err := b2.timerFires(idDel); err != nil {
		t.Fatal(err)
	}
	b2.wantDisk(one)
	if b2.net.ran("networkctl delete wg0") != 1 {
		t.Fatalf("argv = %q", b2.net.argv)
	}
}

func TestWireGuardGroupFromNetworkdRuntime(t *testing.T) {
	b := newBench(t)
	dir := filepath.Join(b.net.root, "run/systemd/netif")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.net.root, "usr/lib/group"), "root:x:0:\nsystemd-network:x:192:\n")
	// If /run/systemd/netif has gid 0 (root:root), fall back to /usr/lib/group rather than returning 0.
	info, _ := os.Stat(dir)
	if info.Sys().(*syscall.Stat_t).Gid == 0 {
		if gid, err := b.r.networkGroup(); err != nil || gid != 192 {
			t.Fatalf("expected fallback gid 192 when netif gid is 0, got %d %v", gid, err)
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(dir, 0, 997); err != nil {
				t.Fatal(err)
			}
			if gid, err := b.r.networkGroup(); err != nil || gid != 997 {
				t.Fatalf("expected non-zero netif gid 997, got %d %v", gid, err)
			}
		}
	} else {
		gid, err := b.r.networkGroup()
		if err != nil || uint32(gid) != info.Sys().(*syscall.Stat_t).Gid {
			t.Fatalf("gid %d %v", gid, err)
		}
	}
	// Before networkd ran, the group files say; an image's system groups are in /usr/lib/group.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if gid, err := b.r.networkGroup(); err != nil || gid != 192 {
		t.Fatalf("gid %d %v", gid, err)
	}
	writeFile(t, filepath.Join(b.net.root, "etc/group"), "systemd-network:x:998:\n")
	if gid, err := b.r.networkGroup(); err != nil || gid != 998 {
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

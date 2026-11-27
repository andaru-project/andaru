//go:build integration

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
)

// A bus store that cannot be read is moved aside, and the bus starts with an empty one.
func TestUnreadableBusStoreIsMovedAside(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the bus socket requires root")
	}
	dir, err := os.MkdirTemp("/var/tmp", "andaru-bus-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "bus.db"), []byte("not a database, not even close to one"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, closeEvents, err := openBus(dir, filepath.Join(dir, "run", "bus.sock"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = events.Publish("config/head", []byte(`{}`), true); err != nil {
		t.Fatal(err)
	}
	if err = closeEvents(); err != nil {
		t.Fatal(err)
	}
	aside, _ := filepath.Glob(filepath.Join(dir, "bus.db.unreadable-*"))
	if body, err := os.ReadFile(filepath.Join(dir, "bus.db")); len(aside) != 1 || err != nil || string(body[:16]) != "SQLite format 3\x00" {
		t.Fatalf("aside %v, new store %q %v", aside, body, err)
	}

	// Any other failure leaves the store where it is: here, a file where the socket goes.
	if err = os.WriteFile(filepath.Join(dir, "run", "bus.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = openBus(dir, filepath.Join(dir, "run", "bus.sock"), "", ""); err == nil {
		t.Fatal("the bus started over a file at its socket")
	}
	if _, err = os.Stat(filepath.Join(dir, "bus.db")); err != nil {
		t.Fatalf("a socket failure moved the store aside: %v", err)
	}
}

// A socket path that is not absolute is refused before anything is created for it.
func TestRelativeBusSocketIsRefused(t *testing.T) {
	if _, _, err := listenRootSocket("bus.sock", "bus"); err == nil {
		t.Fatal("a relative socket path was accepted")
	}
}

// The add-on bus socket is mode 0666 inside a 0755 socket dir beneath a 0700 root-only parent, with its claim lock
// kept outside the bind-mounted socket dir, and serves dynamic token clients isolated from root's socket.
func TestOpenBusServesIsolatedAddonsTokenSocket(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the bus socket requires root")
	}
	dir, err := os.MkdirTemp("/var/tmp", "andaru-abus-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rootSock := filepath.Join(dir, "run", "bus.sock")
	addonsSock := filepath.Join(dir, "addons", "bus-socket", "bus.sock")
	events, closeEvents, err := openBus(dir, rootSock, "", addonsSock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := closeEvents(); cerr != nil {
			t.Errorf("closeEvents: %v", cerr)
		}
	}()

	// Verify directory and socket modes and that no .lock file sits inside bus-socket/.
	if info, err := os.Lstat(filepath.Join(dir, "addons")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("addons parent mode = %v, err=%v", info, err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "addons", "bus-socket")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("bus-socket dir mode = %v, err=%v", info, err)
	}
	if info, err := os.Lstat(addonsSock); err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("addons bus.sock mode = %v, err=%v", info, err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "addons", "bus-socket"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "bus.sock" {
		t.Fatalf("bus-socket dir entries = %v, err=%v; want only [bus.sock]", entries, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "run", "addons-bus.sock.lock")); err != nil {
		t.Fatalf("expected claim lock in private run dir: %v", err)
	}

	// A second engine cannot steal the socket while the first holds its lock.
	if _, _, err := listenAddonsBusSocket(addonsSock, filepath.Join(dir, "run", "addons-bus.sock.lock")); err == nil {
		t.Fatal("second listener stole locked addons bus socket")
	}

	// Add-on client registered via SetClient authenticates on addonsSock and is refused on rootSock.
	if err := events.SetClient("addon.sample", "tok-live", bus.Grants{Subscribe: []string{"apply/+"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := bus.Dial(ctx, addonsSock, "realize", ""); err == nil {
		c.Close()
		t.Fatal("realize accepted on addons socket")
	}
	if c, err := bus.Dial(ctx, rootSock, "addon.sample", "tok-live"); err == nil {
		c.Close()
		t.Fatal("addon accepted on root socket")
	}
	c, err := bus.Dial(ctx, addonsSock, "addon.sample", "tok-live")
	if err != nil {
		t.Fatalf("addon dial failed: %v", err)
	}
	_ = c.Close()
}

//go:build integration

package engine

import (
	"os"
	"path/filepath"
	"testing"
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
	events, closeEvents, err := openBus(dir, filepath.Join(dir, "run", "bus.sock"))
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
	if _, _, err = openBus(dir, filepath.Join(dir, "run", "bus.sock")); err == nil {
		t.Fatal("the bus started over a file at its socket")
	}
	if _, err = os.Stat(filepath.Join(dir, "bus.db")); err != nil {
		t.Fatalf("a socket failure moved the store aside: %v", err)
	}
}

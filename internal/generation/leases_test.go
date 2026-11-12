package generation

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const leaseKea = `{"Dhcp4":{"subnet4":[
	{"id":1,"subnet":"192.168.40.0/24","reservations":[{"hw-address":"02:00:00:00:00:10","ip-address":"192.168.40.10","hostname":"nas"}],
	 "user-context":{"andaru":{"domain":"home.arpa","register":true}}},
	{"id":2,"subnet":"192.168.50.0/24","user-context":{"andaru":{"domain":"home.arpa","register":false}}}]}}`

type lease struct {
	Address  string `json:"ip-address"`
	Hostname string `json:"hostname"`
	State    int    `json:"state"`
	Subnet   int    `json:"subnet-id"`
	Valid    int    `json:"valid-lft"`
	Cltt     int    `json:"cltt"`
}

// kea answers lease4-get-all on the bench's Kea socket with leases, counting the questions.
func (b *bench) kea(leases ...lease) *int {
	b.t.Helper()
	writeFile(b.t, filepath.Join(b.net.root, keaFile), leaseKea)
	path := filepath.Join(b.net.root, "run/kea/kea4-ctrl-socket")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		b.t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { l.Close() })
	asked := new(int)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('}')
			if strings.Contains(line, "lease4-get-all") {
				*asked++
			}
			reply, _ := json.Marshal(map[string]any{"result": 0, "arguments": map[string]any{"leases": leases}})
			c.Write(reply)
			c.Close()
		}
	}()
	return asked
}

func (b *bench) leaseNames() string {
	data, _ := os.ReadFile(filepath.Join(b.net.root, leasesDir, "leases.conf"))
	return string(data)
}

func TestLeaseNamesFromKea(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop-1.example.org", 0, 1, 7200, 100})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := "server:\n" +
		"\tlocal-data: \"laptop-1.home.arpa. 2400 IN A 192.168.40.130\"\n" +
		"\tlocal-data-ptr: \"192.168.40.130 2400 laptop-1.home.arpa.\"\n"
	if got := b.leaseNames(); got != want || b.index("unbound-control reload_keep_cache") < 0 {
		t.Fatalf("leases file %q, argv %q", got, b.net.argv)
	}
}

func TestLeaseNamesRejectUntrustedHostnames(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.131", "a\"\n\tlocal-zone: \".\" redirect", 0, 1, 7200, 100},
		lease{"192.168.40.132", "-bad", 0, 1, 7200, 100}, lease{"192.168.40.133", strings.Repeat("x", 64), 0, 1, 7200, 100})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := b.leaseNames(); got != "server:\n" {
		t.Fatalf("untrusted names published: %q", got)
	}
}

func TestStaticNamesWinOverLeases(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.140", "NAS", 0, 1, 7200, 100})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := b.leaseNames(); strings.Contains(got, "192.168.40.140") {
		t.Fatalf("a lease took a static lease's name: %q", got)
	}
}

func TestNewestLeaseWinsAName(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.150", "phone", 0, 1, 7200, 100}, lease{"192.168.40.151", "phone", 0, 1, 7200, 200}, lease{"192.168.40.152", "gone", 2, 1, 7200, 300})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := b.leaseNames(); !strings.Contains(got, "192.168.40.151") || strings.Contains(got, "192.168.40.150") || strings.Contains(got, "gone") {
		t.Fatalf("leases file %q", got)
	}
}

func TestUnregisteredSubnetPublishesNothing(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.50.130", "printer", 0, 2, 7200, 100})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := b.leaseNames(); got != "server:\n" {
		t.Fatalf("leases file %q", got)
	}
}

func TestUnchangedLeasesDoNotReload(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop-1", 0, 1, 7200, 100})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.index("unbound-control") >= 0 {
		t.Fatalf("reloaded for nothing: %q", b.net.argv)
	}
}

// Kea signalled again while a sync ran; the path unit would not start another, so the sync runs once more.
func TestMarkerMovedDuringSyncRunsAgain(t *testing.T) {
	b := newBench(t)
	asked := b.kea(lease{"192.168.40.130", "laptop-1", 0, 1, 7200, 100})
	marker := filepath.Join(b.net.root, leaseMarker)
	writeFile(t, marker, "")
	moved := false
	b.r.afterLeaseRead = func() {
		if !moved {
			moved = true
			writeFile(t, marker, "x")
		}
	}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if *asked != 2 {
		t.Fatalf("asked Kea %d times", *asked)
	}
}

func TestNoDHCPServerEmptiesLeaseNames(t *testing.T) {
	b := newBench(t)
	writeFile(t, filepath.Join(b.net.root, leasesDir, "leases.conf"), "server:\n\tlocal-data: \"old.home.arpa. 60 IN A 192.168.40.9\"\n")
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := b.leaseNames(); got != "server:\n" {
		t.Fatalf("leases file %q", got)
	}
}

// The leases file, untrusted names and all, is one Unbound accepts as Debian's unbound.conf includes it.
func TestLeaseFileLoadsInUnbound(t *testing.T) {
	if _, err := exec.LookPath("unbound-checkconf"); err != nil {
		t.Skip("unbound-checkconf not installed")
	}
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop-1", 0, 1, 7200, 100}, lease{"192.168.40.131", "a\"\n\tlocal-zone: \".\" redirect", 0, 1, 7200, 100})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(t.TempDir(), "unbound.conf")
	writeFile(t, main, "server:\n\tchroot: \"\"\n\tlocal-zone: \"home.arpa.\" static\ninclude: \""+filepath.Join(b.net.root, leasesDir, "leases.conf")+"\"\n")
	if out, err := exec.Command("unbound-checkconf", main).CombinedOutput(); err != nil {
		t.Fatalf("unbound-checkconf: %s", out)
	}
}

// A reserved name on one segment is not taken by a lease on another.
func TestStaticNamesWinAcrossSubnets(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.50.9", "nas", 0, 2, 7200, 100})
	writeFile(t, filepath.Join(b.net.root, keaFile), strings.Replace(leaseKea, `"register":false`, `"register":true`, 1))
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := b.leaseNames(); strings.Contains(got, "nas.") {
		t.Fatalf("a lease took another segment's static name: %q", got)
	}
}

// A reload that failed is retried at the next sync, though the file already holds the names.
func TestFailedReloadIsRetried(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop-1", 0, 1, 7200, 100})
	b.net.fail = "unbound-control reload_keep_cache"
	if err := b.r.SyncLeases(t.Context()); err == nil {
		t.Fatal("a failed reload passed")
	}
	b.net.fail, b.net.argv = "", nil
	if err := b.r.SyncLeases(t.Context()); err != nil || b.index("unbound-control reload_keep_cache") < 0 {
		t.Fatalf("reload not retried: %v %q", err, b.net.argv)
	}
}

// A sync waits for a generation change in progress, so it never publishes names that change just removed.
func TestLeaseSyncWaitsForGenerationChanges(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop-1", 0, 1, 7200, 100})
	unlock, err := b.r.lock()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.r.SyncLeases(t.Context()) }()
	select {
	case <-done:
		t.Fatal("the sync ran during a generation change")
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil || !strings.Contains(b.leaseNames(), "laptop-1") {
		t.Fatalf("sync after the change: %v %q", err, b.leaseNames())
	}
}

// A stopped Unbound reads the file when it starts; the sync writes it and asks nothing of Unbound.
func TestStoppedUnboundIsNotReloaded(t *testing.T) {
	b := newBench(t)
	b.kea(lease{"192.168.40.130", "laptop-1", 0, 1, 7200, 100})
	b.net.fail = "systemctl is-active --quiet unbound.service"
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := b.leaseNames(); !strings.Contains(got, "laptop-1.home.arpa.") || b.index("unbound-control") >= 0 {
		t.Fatalf("leases file %q, argv %q", got, b.net.argv)
	}
}

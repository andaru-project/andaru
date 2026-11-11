package generation

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// debian builds a host the Debian installer set up: ifupdown raised eth0 with dhcpcd, networkd is stopped.
func debian(t *testing.T) *bench {
	b := newBench(t)
	b.net.stopped, b.net.address = true, map[string][]string{"eth0": {"10.0.1.19/20"}}
	b.net.offered = map[string][]string{"eth0": {"192.0.2.53"}}
	b.net.dhcpcd("eth0")
	writeFile(t, filepath.Join(b.net.root, "etc/network/interfaces"), "allow-hotplug eth0\niface eth0 inet dhcp\n")
	writeFile(t, filepath.Join(b.net.root, "etc/dhcpcd.conf"), "duid\n")
	writeFile(t, filepath.Join(b.net.root, "var/lib/dhcpcd/duid"), "00:01:00:01:32:87:03:54:02:00:00:00:00:01\n")
	b.r.HandBackWait = 200 * time.Millisecond
	return b
}

func (b *bench) ifupdownTakeover() int {
	b.t.Helper()
	return create(b.t, b.dir, Manifest{SavedVersion: "7", Takeover: true, Baseline: "ifupdown", ConfirmTimeoutSeconds: 120}, one)
}

// after returns the commands run since the first one starting with from.
func (b *bench) after(from string) []string {
	i := slices.IndexFunc(b.net.argv, func(a string) bool { return len(a) >= len(from) && a[:len(from)] == from })
	if i < 0 {
		b.t.Fatalf("%q never ran: %q", from, b.net.argv)
	}
	return b.net.argv[i:]
}

func TestActivateIfupdownOrder(t *testing.T) {
	b := debian(t)
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	got := b.after("dhcpcd")
	want := []string{"dhcpcd -p -x eth0", "systemctl start systemd-networkd.service", "networkctl reload"}
	if !slices.Equal(got[:3], want) {
		t.Fatalf("commands = %q, want %q first", got, want)
	}
	kind, base, err := ReadBaselineKind(b.dir)
	if err != nil || kind != "ifupdown" || base == nil || len(base.Interfaces) != 1 || !base.Interfaces[0].Hotplug || base.NetworkdWasActive || !slices.Equal(base.Connected, []string{"eth0"}) {
		t.Fatalf("baseline %q %+v %v", kind, base, err)
	}
	if g := b.read(id); g.State.State != Awaiting {
		t.Fatalf("state = %+v", g.State)
	}
}

func TestActivateIfupdownFailureHandsBack(t *testing.T) {
	b := debian(t)
	b.net.fail = "systemctl start systemd-networkd"
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activation succeeded without networkd")
	}
	if b.net.ran("systemctl restart ifup@eth0.service") != 1 || b.exists(netDir+"/50-andaru-eth0.network") {
		t.Fatalf("not handed back: %q", b.net.argv)
	}
	if g, p := b.read(id), b.pointers(); g.State.State != Failed || p.Pending != nil || p.Live != nil {
		t.Fatalf("state %+v pointers %+v", g.State, p)
	}
}

func TestHandBackRunsEveryStep(t *testing.T) {
	b := debian(t)
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.fail = "systemctl stop"
	if err := b.r.Rollback(t.Context(), id, "operator"); err == nil {
		t.Fatal("a failed stop was not reported")
	}
	if b.net.ran("systemctl restart ifup@eth0.service") != 1 {
		t.Fatalf("ifup@ not restarted after a failed stop: %q", b.net.argv)
	}
}

func TestHandBackSkipsInterfacesAlreadyUp(t *testing.T) {
	b := debian(t)
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.dhcpcd("eth0") // a retry after ifupdown already took the interface back
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if n := b.net.ran("systemctl restart"); n != 0 {
		t.Fatalf("bounced an interface that was up: %q", b.net.argv)
	}
	if g := b.read(id); g.State.State != RolledBack {
		t.Fatalf("state = %+v", g.State)
	}
}

func TestRollbackStaysPendingUntilAddressesReturn(t *testing.T) {
	b := debian(t)
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.ifupFails, b.net.address["eth0"] = true, nil
	if err := b.r.Rollback(t.Context(), id, "operator"); err == nil {
		t.Fatal("rolled back without the address")
	}
	if p := b.pointers(); p.Pending == nil || *p.Pending != id {
		t.Fatalf("pointers = %+v", p)
	}
	b.net.ifupFails = false
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if g, p := b.read(id), b.pointers(); g.State.State != RolledBack || p.Pending != nil || p.Live != nil {
		t.Fatalf("state %+v pointers %+v", g.State, p)
	}
	if _, err := os.Stat(filepath.Join(b.net.root, "run/dhcpcd/eth0.pid")); err != nil {
		t.Fatal("dhcpcd not back")
	}
}

func (b *bench) confirmIfupdown() int {
	b.t.Helper()
	id := b.ifupdownTakeover()
	if err := b.r.Activate(b.t.Context(), id); err != nil {
		b.t.Fatal(err)
	}
	if err := b.r.Confirm(b.t.Context(), id); err != nil {
		b.t.Fatal(err)
	}
	return id
}

func TestConfirmIfupdownMasks(t *testing.T) {
	b := debian(t)
	b.confirmIfupdown()
	if b.net.ran("systemctl enable systemd-networkd.service") != 1 || b.net.ran("systemctl mask networking.service ifup@.service") != 1 {
		t.Fatalf("commands = %q", b.net.argv)
	}
	if !b.exists("etc/network/interfaces") || b.exists(cloudConfig) {
		t.Fatal("confirm touched ifupdown's files or wrote cloud-init's")
	}
}

func TestConfirmIfupdownRepeatsAfterKill(t *testing.T) {
	b := debian(t)
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.r.failAt = func(step string) error {
		if step == "permanent:mask" {
			return errors.New("killed")
		}
		return nil
	}
	if err := b.r.Confirm(t.Context(), id); err == nil {
		t.Fatal("confirm hid the failed mask")
	}
	b.r.failAt = nil
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.ran("systemctl mask --no-reload networking.service ifup@.service") != 1 {
		t.Fatalf("boot did not mask ifupdown: %q", b.net.argv)
	}
}

func TestBootFallsBackToIfupdown(t *testing.T) {
	b := debian(t)
	id := b.confirmIfupdown()
	writeFile(t, filepath.Join(b.dir, itoa(id), "files", "50-andaru-eth0.network"), "evil")
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Boot runs before networking.service; waiting for it would deadlock.
	for _, want := range []string{"systemctl unmask networking.service ifup@.service", "systemctl start --no-block networking.service", "systemctl start --no-block ifup@eth0.service"} {
		if b.net.ran(want) != 1 {
			t.Fatalf("%q not run: %q", want, b.net.argv)
		}
	}
	if len(b.net.netFiles()) != 0 || b.pointers().Live != nil {
		t.Fatalf("files %v pointers %+v", b.net.netFiles(), b.pointers())
	}
}

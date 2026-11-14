package generation

import (
	"errors"
	"github.com/andaru-project/andaru/internal/host"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	b.net.ifupFails = true
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
	if b.net.ran("systemctl enable systemd-networkd.service") != 1 || b.net.ran("systemctl mask networking.service ifup@.service") != 1 ||
		b.net.ran("systemctl mask nftables.service") != 1 {
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

// Restarting networking.service runs ifdown -a first, so it must not run while an auto interface is already back.
func TestHandBackSkipsNetworkingWhenAutoIsUp(t *testing.T) {
	b := debian(t)
	writeFile(t, filepath.Join(b.net.root, "etc/network/interfaces"), "auto eth0\niface eth0 inet dhcp\n")
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.dhcpcd("eth0")
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if b.net.ran("systemctl restart networking.service") != 0 {
		t.Fatalf("bounced an auto interface that was back: %q", b.net.argv)
	}
	// Down, it is raised by networking.service.
	b = debian(t)
	writeFile(t, filepath.Join(b.net.root, "etc/network/interfaces"), "auto eth0\niface eth0 inet dhcp\n")
	id = b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil || b.net.ran("systemctl restart networking.service") != 1 {
		t.Fatalf("auto interface not raised: %v %q", err, b.net.argv)
	}
}

// A failed activation whose hand-back fails leaves the generation to the armed timer, never the host without network.
func TestActivateIfupdownHandBackFailureStaysPending(t *testing.T) {
	b := debian(t)
	b.net.fail, b.net.ifupFails = "systemctl start systemd-networkd", true
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activation succeeded")
	}
	if p, g := b.pointers(), b.read(id); p.Pending == nil || *p.Pending != id || g.State.State == Failed {
		t.Fatalf("pointers %+v state %+v", p, g.State)
	}
}

// After an ifupdown takeover resolved is the only resolver; activation without any server is no success.
func TestActivateIfupdownNeedsDNSServers(t *testing.T) {
	b := debian(t)
	b.net.offered = nil
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activated with no DNS server")
	}
	if g := b.read(id); g.State.State != Failed || !strings.Contains(g.State.Reason, "no DNS server") {
		t.Fatalf("state = %+v", g.State)
	}
}

// An interface ifupdown gained after the apply was judged has no file in the generation; masking ifupdown would strand it.
func TestActivateIfupdownRefusesAnUndeclaredInterface(t *testing.T) {
	b := debian(t)
	b.net.devices["eth1"] = true
	writeFile(t, filepath.Join(b.net.root, "etc/network/interfaces"), "allow-hotplug eth0\niface eth0 inet dhcp\nallow-hotplug eth1\niface eth1 inet static\n address 192.0.2.9/24\n")
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err == nil || !strings.Contains(err.Error(), "eth1") {
		t.Fatalf("activate: %v", err)
	}
	if b.net.ran("dhcpcd") != 0 {
		t.Fatalf("handed over anyway: %q", b.net.argv)
	}
}

// ifupdown that changed into something the reader refuses since the apply was judged is not captured or taken over.
func TestActivateIfupdownRefusesAChangedBaseline(t *testing.T) {
	b := debian(t)
	writeFile(t, filepath.Join(b.net.root, "etc/network/interfaces"), "allow-hotplug eth0\niface eth0 inet dhcp\n pre-up true\n")
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err == nil || !strings.Contains(err.Error(), "ifupdown changed since the apply was judged") {
		t.Fatalf("activate: %v", err)
	}
	if kind, _, _ := ReadBaselineKind(b.dir); kind != "" || b.net.ran("dhcpcd") != 0 || b.pointers().Pending != nil {
		t.Fatalf("baseline %q, commands %q", kind, b.net.argv)
	}
}

// After confirmation ifupdown is masked and only the capture knows how to hand back; without one there is nothing to trust.
func TestActivateIfupdownRefusesAConfirmedTakeoverWithoutItsBaseline(t *testing.T) {
	b := debian(t)
	b.confirmIfupdown()
	writeFile(t, filepath.Join(b.dir, "baseline", "manifest.json"), `{"kind":"netplan","files":[]}`)
	b.net.argv = nil
	id := b.ifupdownTakeover()
	if err := b.r.Activate(t.Context(), id); err == nil || !strings.Contains(err.Error(), "recorded no ifupdown baseline") {
		t.Fatalf("activate: %v", err)
	}
	if b.net.ran("dhcpcd") != 0 || b.pointers().Pending != nil {
		t.Fatalf("commands %q", b.net.argv)
	}
}

// A static interface counts as back only with every address ifupdown gives it.
func TestHasStaticNeedsEveryAddress(t *testing.T) {
	i := host.IfupdownInterface{Detected: host.Detected{Name: "eth0", IPv4: host.Addressing{Mode: "static", Addresses: []string{"192.0.2.1/24"}},
		IPv6: host.Addressing{Mode: "static", Addresses: []string{"2001:db8::1/64"}}}}
	o := observation{links: []host.LinkStatus{{Name: "eth0", Addresses: []host.AddressStatus{{Family: "ipv4", Address: "192.0.2.1", PrefixLength: 24}}}}}
	if hasStatic(o, i) {
		t.Fatal("IPv6 missing but counted as back")
	}
	o.links[0].Addresses = append(o.links[0].Addresses, host.AddressStatus{Family: "ipv6", Address: "2001:db8::1", PrefixLength: 64})
	if !hasStatic(o, i) {
		t.Fatal("all addresses present but not counted as back")
	}
}

// networkd keeps a device whose .netdev is gone; handing back must delete the ones the takeover made.
func TestHandBackDeletesTakeoverDevices(t *testing.T) {
	b := debian(t)
	files := map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-vl0.netdev": vlanDevice, "50-andaru-vl0.network": vlanNetwork}
	id := create(t, b.dir, Manifest{SavedVersion: "7", Takeover: true, Baseline: "ifupdown", ConfirmTimeoutSeconds: 120}, files)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if b.net.ran("networkctl delete vl0") != 1 {
		t.Fatalf("device left behind: %q", b.net.argv)
	}
}

// After a confirmed takeover ifupdown is masked; falling back to it at runtime must unmask it first.
func TestRollbackToIfupdownBaselineUnmasks(t *testing.T) {
	b := debian(t)
	first := b.confirmIfupdown()
	second := b.gen(false, two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, itoa(first), "files", "50-andaru-eth0.network"), "evil")
	b.net.argv = nil
	if err := b.r.Rollback(t.Context(), second, "operator"); err != nil {
		t.Fatal(err)
	}
	unmask := slices.Index(b.net.argv, "systemctl unmask networking.service ifup@.service")
	restart := slices.IndexFunc(b.net.argv, func(a string) bool { return strings.HasPrefix(a, "systemctl restart") })
	if unmask < 0 || restart < 0 || unmask > restart {
		t.Fatalf("commands = %q", b.net.argv)
	}
}

// A failed ifupdown takeover undoes the services it started, as a rollback does.
func TestActivateIfupdownFailureStopsServices(t *testing.T) {
	b := debian(t)
	b.net.fail = "unbound-checkconf"
	files := map[string]string{keaFile: `{"Dhcp4":{}}`, unboundFile: "server:\n"}
	maps.Copy(files, one)
	id := create(b.t, b.dir, Manifest{SavedVersion: "7", Takeover: true, Baseline: "ifupdown", ConfirmTimeoutSeconds: 120}, files)
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activation succeeded with Unbound refusing")
	}
	if b.index("systemctl stop kea-dhcp4-server.service") < 0 || b.read(id).State.State != Failed {
		t.Fatalf("Kea left serving: %q", b.net.argv)
	}
}

// Rolling a takeover back to ifupdown stops the DHCP server it started.
func TestRollbackToIfupdownStopsServices(t *testing.T) {
	b := debian(t)
	files := map[string]string{keaFile: `{"Dhcp4":{}}`}
	maps.Copy(files, one)
	id := create(b.t, b.dir, Manifest{SavedVersion: "7", Takeover: true, Baseline: "ifupdown", ConfirmTimeoutSeconds: 120}, files)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if b.index("systemctl stop kea-dhcp4-server.service") < 0 || b.read(id).State.State != RolledBack {
		t.Fatalf("Kea left serving: %q", b.net.argv)
	}
}

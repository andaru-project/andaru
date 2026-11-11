package generation

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	netplanNetwork = "[Match]\nPermanentMACAddress=bc:24:11:89:01:10\nName=eth0\n\n[Network]\nDHCP=ipv4\n"
	netplanLink    = "[Match]\nPermanentMACAddress=bc:24:11:89:01:10\n\n[Link]\nName=eth0\n"
	netplanYAML    = "network:\n  version: 2\n"
	cloudConfig    = "etc/cloud/cloud.cfg.d/99-andaru-network.cfg"
)

var netplan = map[string]string{"10-netplan-eth0.network": netplanNetwork, "10-netplan-eth0.link": netplanLink}

// generate is netplan's generator at boot: it writes its files whenever its YAML exists.
func (b *bench) generate() {
	b.t.Helper()
	for name, body := range netplan {
		path := filepath.Join(b.net.root, netDir, name)
		writeFile(b.t, path, body)
		if err := os.Chmod(path, 0o640); err != nil {
			b.t.Fatal(err)
		}
	}
}

// cloud builds a netplan cloud-image host.
func cloud(t *testing.T) *bench {
	b := newBench(t)
	b.generate()
	writeFile(t, filepath.Join(b.net.root, "etc/netplan/50-cloud-init.yaml"), netplanYAML)
	if err := os.MkdirAll(filepath.Join(b.net.root, "etc/cloud/cloud.cfg.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	b.net.apply()
	return b
}

// reboot empties /run as a boot does; netplan regenerates only while its YAML is there.
func (b *bench) reboot() {
	b.t.Helper()
	if err := os.RemoveAll(filepath.Join(b.net.root, "run")); err != nil {
		b.t.Fatal(err)
	}
	if yaml, _ := filepath.Glob(filepath.Join(b.net.root, "etc/netplan/*.yaml")); len(yaml) > 0 {
		b.generate()
	}
	b.net.argv = nil
}

func (b *bench) takeover() int {
	b.t.Helper()
	id := b.gen(true, one)
	if err := b.r.Activate(b.t.Context(), id); err != nil {
		b.t.Fatal(err)
	}
	return id
}

func (b *bench) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(b.net.root, rel))
	return err == nil
}

func (b *bench) wantPermanent() {
	b.t.Helper()
	if data, _ := os.ReadFile(filepath.Join(b.net.root, cloudConfig)); string(data) != "network: {config: disabled}\n" {
		b.t.Fatalf("cloud-init file = %q", data)
	}
	if yaml, _ := filepath.Glob(filepath.Join(b.net.root, "etc/netplan/*.yaml")); len(yaml) != 0 {
		b.t.Fatalf("netplan YAML left: %v", yaml)
	}
	if data, _ := os.ReadFile(filepath.Join(b.dir, "baseline/netplan/50-cloud-init.yaml")); string(data) != netplanYAML {
		b.t.Fatalf("kept YAML = %q", data)
	}
	if b.net.ran("systemctl enable") == 0 {
		b.t.Fatalf("networkd was not enabled: %q", b.net.argv)
	}
}

func TestTakeoverCapturesBaselineOnly(t *testing.T) {
	b := cloud(t)
	writeFile(t, filepath.Join(b.net.root, netDir, "50-andaru-old.network"), "stale")
	b.takeover()
	var manifest struct {
		Kind  string
		Files []struct {
			Path, Mode, SHA256 string
			UID, GID           int
		}
	}
	data, err := os.ReadFile(filepath.Join(b.dir, "baseline/manifest.json"))
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		t.Fatalf("baseline manifest: %v", err)
	}
	if manifest.Kind != "netplan" || len(manifest.Files) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	for _, f := range manifest.Files {
		if want := sum([]byte(netplan[filepath.Base(f.Path)])); f.SHA256 != want || f.Mode != "0640" || f.UID != os.Getuid() {
			t.Fatalf("captured %+v", f)
		}
	}
	b.wantDisk(map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-old.network": "stale"})
	if !b.exists("etc/netplan/50-cloud-init.yaml") || b.exists(cloudConfig) {
		t.Fatal("the host was changed outside /run before confirmation")
	}
	b.wantPointers(0, 0, 1, 1)
}

func TestTakeoverRollbackRestoresBaselineExactly(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	// networkd reads its files as systemd-network; the group must survive the round trip.
	path := filepath.Join(b.dir, "baseline/manifest.json")
	data, _ := os.ReadFile(path)
	uid, gid := `"uid":`+itoa(os.Getuid()), `"gid":`+itoa(os.Getgid())
	if !strings.Contains(string(data), uid) || !strings.Contains(string(data), gid) {
		t.Fatalf("manifest does not record ownership: %s", data)
	}
	writeFile(t, path, strings.NewReplacer(uid, `"uid":0`, gid, `"gid":192`).Replace(string(data)))
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(netplan)
	for name := range netplan {
		info, _ := os.Stat(filepath.Join(b.net.root, netDir, name))
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("%s mode = %v", name, info.Mode())
		}
		if !slices.Contains(b.net.chowns, netDir+name+" 0:192") {
			t.Fatalf("%s ownership not restored: %q", name, b.net.chowns)
		}
	}
	b.wantPointers(0, 0, 0, 0)
}

func TestSecondTakeoverAttemptRecaptures(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	changed := netplanNetwork + "LinkLocalAddressing=ipv6\n"
	writeFile(t, filepath.Join(b.net.root, netDir, "10-netplan-eth0.network"), changed)
	second := b.takeover()
	if err := b.timerFires(second); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(map[string]string{"10-netplan-eth0.network": changed, "10-netplan-eth0.link": netplanLink})
}

func TestBaselineFrozenAfterConfirm(t *testing.T) {
	b := cloud(t)
	if err := b.r.Confirm(t.Context(), b.takeover()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(b.dir, "baseline/manifest.json"))
	writeFile(t, filepath.Join(b.net.root, netDir, "10-netplan-zz.network"), "late")
	second := b.takeover()
	after, _ := os.ReadFile(filepath.Join(b.dir, "baseline/manifest.json"))
	if string(before) != string(after) {
		t.Fatal("a confirmed takeover's baseline was rewritten")
	}
	b.wantPointers(1, 0, second, second)
}

func TestConfirmMovesPointersAndMakesPermanent(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.wantPointers(1, 0, 0, 1)
	b.wantPermanent()
	// Andaru's links are not required for "online", so wait-online would hold every boot for its two-minute timeout.
	if b.net.ran("systemctl enable systemd-networkd.service") != 1 || b.net.ran("systemctl disable systemd-networkd-wait-online.service") != 1 {
		t.Fatalf("argv = %q", b.net.argv)
	}
	if g := b.read(id); g.State.State != Confirmed || g.Reason != "" {
		t.Fatalf("state = %+v", g.State)
	}
	// A later apply confirmed on top keeps the first as last-known-good.
	second := b.gen(false, two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	b.wantPointers(2, 1, 0, 2)
}

func TestConfirmRefusals(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	if err := b.r.Confirm(t.Context(), 99); !errors.Is(err, ErrWrongState) {
		t.Fatalf("unknown id: %v", err)
	}
	b.now = b.now.Add(121 * time.Second)
	if err := b.r.Confirm(t.Context(), id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("after the deadline: %v", err)
	}
	b.wantPointers(0, 0, 1, 1)
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("after a rollback: %v", err)
	}
	b.wantPointers(0, 0, 0, 0)

	b = cloud(t)
	id = b.takeover()
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("twice: %v", err)
	}
	b.wantPointers(1, 0, 0, 1)
}

func TestRollbackAfterConfirmDoesNothing(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	before := len(b.net.argv)
	if err := b.timerFires(id); !errors.Is(err, ErrWrongState) {
		t.Fatalf("a late timer rolled back a confirmed generation: %v", err)
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
	if len(b.net.argv) != before {
		t.Fatalf("argv = %q", b.net.argv[before:])
	}
}

func TestRollbackQueuedBehindConfirm(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	held, err := os.OpenFile(filepath.Join(b.dir, "lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.timerFires(id) }()
	time.Sleep(150 * time.Millisecond) // the timer is now waiting on the lock
	if err := writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{Confirmed: &id, Live: &id}); err != nil {
		t.Fatal(err)
	}
	_ = held.Close()
	if err := <-done; !errors.Is(err, ErrWrongState) {
		t.Fatalf("rollback queued behind a confirmation: %v", err)
	}
	b.wantDisk(one)
}

func TestConfirmKilledBetweenWrites(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	killed := errors.New("killed")
	b.r.failAt = func(step string) error {
		if step == "confirm:state" {
			return killed
		}
		return nil
	}
	if err := b.r.Confirm(t.Context(), id); !errors.Is(err, killed) {
		t.Fatal(err)
	}
	b.r.failAt = nil
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
	b.wantPermanent()
	if g := b.read(id); g.State.State != Confirmed {
		t.Fatalf("a confirmed apply was undone by the reboot: %+v", g.State)
	}
}

func TestBootCompletesInterruptedConfirm(t *testing.T) {
	for _, step := range []string{"permanent:enable", "permanent:cloud-init", "permanent:netplan"} {
		t.Run(step, func(t *testing.T) {
			b := cloud(t)
			id := b.takeover()
			b.r.failAt = func(at string) error {
				if at == step {
					return errors.New("injected")
				}
				return nil
			}
			if err := b.r.Confirm(t.Context(), id); err == nil {
				t.Fatal("confirm hid a takeover that is not yet permanent")
			}
			if g := b.read(id); g.State.State != Confirmed || !strings.Contains(g.Reason, "completes at the next boot") {
				t.Fatalf("state = %+v", g.State)
			}
			b.r.failAt = nil
			b.reboot()
			if err := b.r.Boot(t.Context()); err != nil {
				t.Fatal(err)
			}
			b.wantDisk(one)
			b.wantPermanent()
			b.wantPointers(1, 0, 0, 1)
			if step == "permanent:enable" && b.net.ran("systemctl enable --no-reload systemd-networkd.service") != 1 {
				t.Fatalf("argv = %q", b.net.argv)
			}
		})
	}
}

func TestBootContinuesWhenPermanentStepFails(t *testing.T) {
	b := cloud(t)
	if err := b.r.Confirm(t.Context(), b.takeover()); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.fail = "systemctl"
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	if b.net.ran("networkctl") != 0 {
		t.Fatalf("boot talked to a networkd that has not started: %q", b.net.argv)
	}
}

func TestBootBeforeAnyConfirmation(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(netplan)
	b.wantPointers(0, 0, 0, 0)
	if g := b.read(id); g.State.State != RolledBack || g.Reason != "reboot" {
		t.Fatalf("state = %+v", g.State)
	}
	if !b.exists("etc/netplan/50-cloud-init.yaml") || b.exists(cloudConfig) {
		t.Fatal("an unconfirmed takeover changed the host at boot")
	}
}

func TestBootFallsBack(t *testing.T) {
	b := cloud(t)
	if err := b.r.Confirm(t.Context(), b.takeover()); err != nil {
		t.Fatal(err)
	}
	second := b.gen(false, two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	corrupt := func(id int) {
		writeFile(t, filepath.Join(b.dir, itoa(id), "files", "50-andaru-eth0.network"), "evil")
	}

	corrupt(2)
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	b.wantPointers(2, 1, 0, 1) // live differs from confirmed: that is the reported fallback

	corrupt(1)
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(netplan)
	b.wantPointers(2, 1, 0, 0)

	files, _ := filepath.Glob(filepath.Join(b.dir, "baseline/files/*"))
	writeFile(t, files[0], "evil")
	b.reboot()
	if err := b.r.Boot(t.Context()); err == nil {
		t.Fatal("boot reported success with nothing usable")
	}
	if got := b.net.netFiles(); len(got) != 0 {
		t.Fatalf("boot wrote unverified files: %v", slices.Collect(maps.Keys(got)))
	}
}

func TestDeadlineRollbackWaitsForTheDeadline(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(false, two)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	// The timer was armed before activation; if it fires early the operator must still get the window they were shown.
	if err := b.r.Rollback(t.Context(), id, "deadline"); err == nil || errors.Is(err, ErrWrongState) {
		t.Fatalf("the timer rolled back before the deadline: %v", err)
	}
	b.wantDisk(two)
	b.wantPointers(1, 0, id, id)
	if err := b.r.Rollback(t.Context(), id, "requested"); err != nil {
		t.Fatalf("an operator's rollback must not wait: %v", err)
	}
}

func TestBootFinishesOnlyAConfirmedTakeover(t *testing.T) {
	// A takeover that was rolled back leaves its baseline capture behind; that alone must not make boot remove netplan.
	b := cloud(t)
	id := b.takeover()
	if err := b.r.Rollback(t.Context(), id, "requested"); err != nil {
		t.Fatal(err)
	}
	for name := range netplan {
		os.Remove(filepath.Join(b.net.root, netDir, name))
	}
	b.net.apply()
	plain := b.gen(false, one)
	if err := b.r.Activate(t.Context(), plain); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), plain); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(b.net.root, "run")); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !b.exists("etc/netplan/50-cloud-init.yaml") || b.exists(cloudConfig) || b.net.ran("systemctl") != 0 {
		t.Fatalf("boot made a takeover permanent that was never confirmed: %q", b.net.argv)
	}

	// A confirmed takeover stays one after later ordinary applies are confirmed on top of it.
	b = cloud(t)
	if err := b.r.Confirm(t.Context(), b.takeover()); err != nil {
		t.Fatal(err)
	}
	second := b.gen(false, two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.net.root, "etc/netplan/50-cloud-init.yaml"), netplanYAML) // cloud-init wrote it back
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantPermanent()
	b.wantDisk(two)
}

func TestConfirmAfterFallbackKeepsTheUsableGeneration(t *testing.T) {
	b := newBench(t)
	b.live(one)
	second := b.gen(false, two)
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, itoa(second), "files", "50-andaru-eth0.network"), "evil")
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.net.devices = map[string]bool{"eth0": true}
	b.net.apply()
	b.wantPointers(second, 1, 0, 1) // boot fell back to generation 1
	third := b.gen(false, map[string]string{"50-andaru-eth0.network": uplinkStatic})
	if err := b.r.Activate(t.Context(), third); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), third); err != nil {
		t.Fatal(err)
	}
	// Last-known-good must be what actually ran before, not the corrupt generation the confirmed pointer still named.
	b.wantPointers(third, 1, 0, third)
}

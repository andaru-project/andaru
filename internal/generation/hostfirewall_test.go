package generation

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The host's nftables.service is started for what systemctl is-enabled calls enabled, for this boot only included.
func TestStartHostFirewallStates(t *testing.T) {
	for state, start := range map[string]bool{"enabled": true, "enabled-runtime": true, "disabled": false, "masked": false, "static": false} {
		var ran []string
		r := Realizer{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			ran = append(ran, filepath.Base(name)+" "+strings.Join(args, " "))
			if args[0] == "is-enabled" {
				return []byte(state + "\n"), nil
			}
			return nil, nil
		}}
		if err := r.startHostFirewall(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(ran, "systemctl start nftables.service") != start {
			t.Errorf("%s: %q", state, ran)
		}
	}
}

// Only a link to /dev/null is a mask; a link to the unit file, as an administrator's copy may be, is not.
func TestNftablesMaskedIsALinkToDevNull(t *testing.T) {
	for target, masked := range map[string]bool{"/dev/null": true, "/usr/lib/systemd/system/nftables.service": false} {
		r := Realizer{Root: t.TempDir()}
		if err := os.MkdirAll(filepath.Dir(r.Root+nftablesUnit), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, r.Root+nftablesUnit); err != nil {
			t.Fatal(err)
		}
		if r.nftablesMasked() != masked {
			t.Errorf("link to %s: masked %v", target, !masked)
		}
	}
}

// handBacks are the ways a confirmed takeover gives the host back its own network configuration.
var handBacks = map[string]struct {
	bench func(t *testing.T) *bench
	hand  func(t *testing.T, b *bench)
	// start is how the host's nftables.service is started; after is the last hand-back step it must follow.
	start, after string
}{
	"boot to ifupdown": {debian, func(t *testing.T, b *bench) {
		bootPast(t, b, b.confirmIfupdown())
	}, "systemctl start --no-block nftables.service", "systemctl unmask networking.service ifup@.service nftables.service"},
	"boot to netplan": {cloud, func(t *testing.T, b *bench) {
		bootPast(t, b, b.confirmTakeover())
	}, "systemctl start --no-block nftables.service", "systemctl unmask nftables.service"},
	"rollback to ifupdown": {debian, func(t *testing.T, b *bench) {
		b.rollbackPast(b.confirmIfupdown(), filtered(two, "22"))
	}, "systemctl start nftables.service", "nft delete table inet andaru"},
	"rollback to netplan": {cloud, func(t *testing.T, b *bench) {
		b.rollbackPast(b.confirmTakeover(), filtered(two, "22"))
	}, "systemctl start nftables.service", "nft delete table inet andaru"},
}

// Every hand-back to the host's own network configuration after a confirmed takeover gives it its own firewall back
// at once: nftables.service is unmasked and, when the host enables it, started. Boot only queues the start.
func TestHandBackStartsHostFirewall(t *testing.T) {
	for name, tc := range handBacks {
		for _, enabled := range []bool{true, false} {
			t.Run(name, func(t *testing.T) {
				b := tc.bench(t)
				b.net.nftablesEnabled = enabled
				b.generator()
				tc.hand(t, b)
				started, after := b.index(tc.start), b.index(tc.after)
				switch {
				case enabled && (started < 0 || after < 0 || started < after):
					t.Fatalf("enabled: %q not after %q: %q", tc.start, tc.after, b.net.argv)
				case !enabled && b.startedFirewall():
					t.Fatalf("disabled but started: %q", b.net.argv)
				case b.nftablesMasked():
					t.Fatalf("nftables.service left masked: %q", b.net.argv)
				}
				// Unmasking reloads systemd, whose netplan generator, the YAML gone, would delete the restored files.
				if strings.HasSuffix(name, "netplan") {
					b.wantDisk(netplan)
				}
			})
		}
	}
}

// An administrator who had masked nftables.service before the takeover keeps that mask at every hand-back.
func TestHandBackKeepsAdministratorsMask(t *testing.T) {
	for name, tc := range handBacks {
		t.Run(name, func(t *testing.T) {
			b := tc.bench(t)
			b.net.nftablesEnabled = true
			if err := os.MkdirAll(filepath.Dir(filepath.Join(b.net.root, nftablesUnit)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/dev/null", filepath.Join(b.net.root, nftablesUnit)); err != nil {
				t.Fatal(err)
			}
			tc.hand(t, b)
			if !b.nftablesMasked() || b.touchedFirewall() {
				t.Fatalf("the administrator's mask was not kept: %q", b.net.argv)
			}
		})
	}
}

// Only a hand-back after a confirmed takeover unmasks: a rollback to an earlier generation keeps Andaru's filter, and
// before a confirmation nothing was masked.
func TestNoHostFirewallWithoutHandBack(t *testing.T) {
	for name, hand := range map[string]func(t *testing.T) *bench{
		"rollback to an earlier generation": func(t *testing.T) *bench {
			b := cloud(t)
			first := b.confirmTakeover()
			second := b.gen(false, filtered(two, "22"))
			if err := b.r.Activate(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			b.net.argv = nil
			if err := b.r.Rollback(t.Context(), second, "operator"); err != nil {
				t.Fatal(err)
			}
			if live := b.pointers().Live; live == nil || *live != first {
				t.Fatalf("live %v", live)
			}
			return b
		},
		"rollback of an unconfirmed netplan takeover": func(t *testing.T) *bench {
			b := cloud(t)
			id := b.takeover()
			b.net.argv = nil
			if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
				t.Fatal(err)
			}
			return b
		},
		"rollback of an unconfirmed ifupdown takeover": func(t *testing.T) *bench {
			b := debian(t)
			id := b.ifupdownTakeover()
			if err := b.r.Activate(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			b.net.argv = nil
			if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
				t.Fatal(err)
			}
			return b
		},
		"boot to netplan without a confirmed takeover": func(t *testing.T) *bench {
			b := cloud(t)
			id := b.confirmTakeover()
			p := b.pointers()
			p.Takeover = false
			if err := writeJSON(filepath.Join(b.dir, "pointers.json"), p); err != nil {
				t.Fatal(err)
			}
			bootPast(t, b, id)
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			if b := hand(t); b.touchedFirewall() {
				t.Fatalf("nftables.service unmasked or started: %q", b.net.argv)
			}
		})
	}
}

// A failing unmask still lets the rollback reload networkd onto the restored files; the timer repeats the rest.
func TestRollbackReloadsDespiteFailedUnmask(t *testing.T) {
	b := cloud(t)
	first := b.confirmTakeover()
	b.net.fail = "systemctl unmask nftables.service"
	second := b.gen(false, filtered(two, "22"))
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, itoa(first), "files", "50-andaru-eth0.network"), "evil")
	b.net.argv = nil
	if err := b.r.Rollback(t.Context(), second, "operator"); err == nil || !strings.Contains(err.Error(), "nftables.service") {
		t.Fatalf("rollback hid the failed unmask: %v", err)
	}
	if b.index("networkctl reload") < 0 {
		t.Fatalf("networkd was not reloaded: %q", b.net.argv)
	}
	b.wantDisk(netplan)
}

// A failed ifupdown activation after a confirmed takeover hands the host back with its own firewall running.
func TestFailedActivationStartsHostFirewall(t *testing.T) {
	b := debian(t)
	b.net.nftablesEnabled = true
	bootPast(t, b, b.confirmIfupdown())
	b.net.fail = "unbound-checkconf"
	files := map[string]string{keaFile: `{"Dhcp4":{}}`, unboundFile: "server:\n"}
	maps.Copy(files, one)
	id := create(b.t, b.dir, Manifest{SavedVersion: "8", Takeover: true, Baseline: "ifupdown", ConfirmTimeoutSeconds: 120}, files)
	b.net.argv = nil
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activation succeeded with Unbound refusing")
	}
	if b.read(id).State.State != Failed || b.index("systemctl start nftables.service") < 0 {
		t.Fatalf("state %+v, commands %q", b.read(id).State, b.net.argv)
	}
}

// Boot finishing a takeover whose confirmation could not mask nftables.service masks it with a reload, or this
// boot would still start it and flush Andaru's filter.
func TestBootMasksNftablesForThisBoot(t *testing.T) {
	b := cloud(t)
	id := b.takeover()
	b.r.failAt = func(at string) error {
		if at == "permanent:nftables" {
			return os.ErrPermission
		}
		return nil
	}
	if err := b.r.Confirm(t.Context(), id); err == nil {
		t.Fatal("confirm hid the failed mask")
	}
	b.r.failAt = nil
	b.reboot()
	b.net.argv = nil
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.ran("systemctl mask nftables.service") != 1 || !b.nftablesMasked() {
		t.Fatalf("commands %q", b.net.argv)
	}
	// Masked already, the next boot leaves it alone.
	b.reboot()
	b.net.argv = nil
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.ran("systemctl mask") != 0 {
		t.Fatalf("commands %q", b.net.argv)
	}
}

func (b *bench) confirmTakeover() int {
	b.t.Helper()
	id := b.takeover()
	if err := b.r.Confirm(b.t.Context(), id); err != nil {
		b.t.Fatal(err)
	}
	return id
}

// bootPast damages the confirmed generation and reboots: no generation is usable, so boot hands the host back.
func bootPast(t *testing.T, b *bench, confirmed int) {
	t.Helper()
	writeFile(t, filepath.Join(b.dir, itoa(confirmed), "files", "50-andaru-eth0.network"), "evil")
	b.reboot()
	b.net.argv = nil
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.pointers().Live != nil {
		t.Fatalf("not on the baseline: %+v", b.pointers())
	}
}

// rollbackPast activates a generation on top of confirmed, damages confirmed and rolls back: the host goes to its baseline.
func (b *bench) rollbackPast(confirmed int, files map[string]string) {
	b.t.Helper()
	second := b.gen(false, files)
	if err := b.r.Activate(b.t.Context(), second); err != nil {
		b.t.Fatal(err)
	}
	writeFile(b.t, filepath.Join(b.dir, itoa(confirmed), "files", "50-andaru-eth0.network"), "evil")
	b.net.argv = nil
	if err := b.r.Rollback(b.t.Context(), second, "operator"); err != nil {
		b.t.Fatal(err)
	}
	if b.pointers().Live != nil {
		b.t.Fatalf("not on the baseline: %+v", b.pointers())
	}
}

// generator makes every reloading systemctl run netplan's generator, which deletes its files once the YAML is gone.
func (b *bench) generator() {
	b.net.onRun = func(command string) {
		if !strings.HasPrefix(command, "systemctl ") || strings.Contains(command, "--no-reload") {
			return
		}
		verb := strings.Fields(command)[1]
		if verb != "mask" && verb != "unmask" && verb != "enable" && verb != "disable" {
			return
		}
		if yaml, _ := filepath.Glob(filepath.Join(b.net.root, "etc/netplan/*.yaml")); len(yaml) > 0 {
			return
		}
		generated, _ := filepath.Glob(filepath.Join(b.net.root, "run/systemd/network/10-netplan-*"))
		for _, path := range generated {
			_ = os.Remove(path)
		}
	}
}

func (b *bench) nftablesMasked() bool {
	target, _ := os.Readlink(filepath.Join(b.net.root, nftablesUnit))
	return target == "/dev/null"
}

func (b *bench) startedFirewall() bool {
	return b.index("systemctl start --no-block nftables.service") >= 0 || b.index("systemctl start nftables.service") >= 0
}

// touchedFirewall reports an unmask or a start of nftables.service.
func (b *bench) touchedFirewall() bool {
	for _, a := range b.net.argv {
		if strings.HasPrefix(a, "systemctl unmask ") && strings.HasSuffix(a, " nftables.service") {
			return true
		}
	}
	return b.startedFirewall()
}

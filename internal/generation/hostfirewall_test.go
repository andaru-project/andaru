package generation

import (
	"path/filepath"
	"testing"
)

// Every hand-back to the host's own network configuration after a confirmed takeover gives it its own firewall back
// at once: nftables.service is unmasked and, when the host enables it, started. Boot only queues the start.
func TestHandBackStartsHostFirewall(t *testing.T) {
	for name, tc := range map[string]struct {
		hand  func(t *testing.T, b *bench)
		start string
		// after is the last hand-back step the start must follow: the files and Andaru's table go first.
		after string
	}{
		"boot to ifupdown": {func(t *testing.T, b *bench) {
			id := b.confirmIfupdown()
			writeFile(t, filepath.Join(b.dir, itoa(id), "files", "50-andaru-eth0.network"), "evil")
			b.reboot()
			b.net.argv = nil
			if err := b.r.Boot(t.Context()); err != nil {
				t.Fatal(err)
			}
		}, "systemctl start --no-block nftables.service", "systemctl unmask networking.service ifup@.service nftables.service"},
		"boot to netplan": {func(t *testing.T, b *bench) {
			id := b.takeover()
			if err := b.r.Confirm(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(b.dir, itoa(id), "files", "50-andaru-eth0.network"), "evil")
			b.reboot()
			b.net.argv = nil
			if err := b.r.Boot(t.Context()); err != nil {
				t.Fatal(err)
			}
		}, "systemctl start --no-block nftables.service", "systemctl unmask nftables.service"},
		"rollback to ifupdown": {func(t *testing.T, b *bench) {
			first := b.confirmIfupdown()
			b.rollbackPast(first, filtered(two, "22"))
		}, "systemctl start nftables.service", "nft delete table inet andaru"},
		"rollback to netplan": {func(t *testing.T, b *bench) {
			first := b.takeover()
			if err := b.r.Confirm(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			b.rollbackPast(first, filtered(two, "22"))
		}, "systemctl start nftables.service", "nft delete table inet andaru"},
	} {
		for _, enabled := range []bool{true, false} {
			t.Run(name, func(t *testing.T) {
				var b *bench
				if name == "boot to ifupdown" || name == "rollback to ifupdown" {
					b = debian(t)
				} else {
					b = cloud(t)
				}
				b.net.nftablesEnabled = enabled
				tc.hand(t, b)
				started, after := b.index(tc.start), b.index(tc.after)
				switch {
				case enabled && (started < 0 || after < 0 || started < after):
					t.Fatalf("enabled: %q not after %q: %q", tc.start, tc.after, b.net.argv)
				case !enabled && b.index("systemctl start --no-block nftables.service")+b.index("systemctl start nftables.service") != -2:
					t.Fatalf("disabled but started: %q", b.net.argv)
				}
			})
		}
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

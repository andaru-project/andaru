package generation

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	pppUnitPath    = "/run/systemd/system/andaru-pppoe-pp0.service"
	pppOptionsPath = "/run/andaru-ppp/pp0.options"
	pppNetworkBody = "[Match]\nName=pp0\nType=ppp\n\n[Network]\nKeepConfiguration=yes\n"
)

func session(options string) map[string]string {
	return map[string]string{
		"50-andaru-eth0.network": uplinkDHCP, "50-andaru-pp0.network": pppNetworkBody,
		pppUnitPath: "[Service]\nExecStart=/usr/sbin/pppd file " + pppOptionsPath + "\n", pppOptionsPath: options,
	}
}

// sessionCommands is what a step asked systemd about PPPoE sessions.
func (b *bench) sessionCommands(from int) []string {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(b.net.argv[from:]), func(c string) bool {
		return !strings.Contains(c, "andaru-pppoe-") && c != "systemctl daemon-reload"
	})
}

func TestSessionsRestartOnlyWhenTheirFilesChange(t *testing.T) {
	b := newBench(t)
	b.live(one)
	const unit = "andaru-pppoe-pp0.service"
	steps := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"new", session("password \"one\"\n"), []string{"systemctl daemon-reload", "systemctl restart --no-block " + unit}},
		{"unchanged", session("password \"one\"\n"), []string{"systemctl daemon-reload", "systemctl start --no-block " + unit}},
		{"new password", session("password \"two\"\n"), []string{"systemctl daemon-reload", "systemctl restart --no-block " + unit}},
		{"dropped", one, []string{"systemctl stop " + unit, "systemctl daemon-reload"}},
		{"none before or after", one, nil},
	}
	for _, s := range steps {
		from := len(b.net.argv)
		id := b.gen(s.files)
		if err := b.r.Activate(t.Context(), id); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if err := b.r.Confirm(t.Context(), id); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		// The session's interface does not exist in the fake: its .network is not waited for.
		if got := b.sessionCommands(from); !slices.Equal(got, s.want) {
			t.Fatalf("%s: commands = %q, want %q", s.name, got, s.want)
		}
		if s.name == "new" {
			info, err := os.Stat(filepath.Join(b.net.root, pppOptionsPath))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("options: %v %v; they hold the password", info, err)
			}
		}
	}
	for _, path := range []string{pppUnitPath, pppOptionsPath, pppStamp(unit)} {
		if b.exists(path) {
			t.Fatalf("%s is still there after the session was dropped", path)
		}
	}
}

func TestSessionThatWasUpMustComeBack(t *testing.T) {
	b := newBench(t)
	b.live(session("password \"one\"\n"))
	// The session is up: networkd manages pp0 and pppd gave it an address.
	b.net.devices["pp0"] = true
	b.net.ppp = map[string]string{"pp0": "203.0.113.7/32"}
	b.net.apply()
	b.r.VerifyWait = 100 * b.r.Poll
	b.net.onRun = func(command string) {
		switch {
		case command == "systemctl restart --no-block andaru-pppoe-pp0.service" && b.net.ppp["pp0"] != "":
			// The new password is refused: pppd never gets an address again.
			b.net.ppp["pp0"], b.net.address["pp0"] = "", nil
		case command == "systemctl restart --no-block andaru-pppoe-pp0.service":
			b.net.ppp["pp0"] = "203.0.113.7/32" // the restore redials on the old password
		}
	}
	id := b.gen(session("password \"wrong\"\n"))
	if err := b.r.Activate(t.Context(), id); err == nil || !strings.Contains(err.Error(), "pp0 lost its IPv4 address") {
		t.Fatalf("activate = %v, want a failure for the lost session", err)
	}
	// Restored: the old options are back and the session is restarted on them.
	data, _ := os.ReadFile(filepath.Join(b.net.root, pppOptionsPath))
	if string(data) != "password \"one\"\n" || b.net.ppp["pp0"] == "" {
		t.Fatalf("options after the restore = %q, session %q", data, b.net.ppp["pp0"])
	}
}

func TestBootStartsSessions(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(session("password \"one\"\n"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl daemon-reload", "systemctl restart --no-block andaru-pppoe-pp0.service"}
	if got := b.sessionCommands(0); !slices.Equal(got, want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if info, err := os.Stat(filepath.Join(b.net.root, pppOptionsPath)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("options: %v %v", info, err)
	}
}

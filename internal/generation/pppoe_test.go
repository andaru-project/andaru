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
	pppUnit0       = "andaru-pppoe-pp0.service"
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

// dialer makes the fake pppd: a stop takes pp0 away, and a start brings it up only on the password "one".
func (b *bench) dialer() {
	b.net.devices["pp0"] = true
	b.net.ppp = map[string]string{"pp0": "203.0.113.7/32"}
	b.net.apply()
	b.net.onRun = func(command string) {
		switch command {
		case "systemctl stop " + pppUnit0:
			delete(b.net.ppp, "pp0")
			delete(b.net.devices, "pp0")
			delete(b.net.address, "pp0")
		case "systemctl start --no-block " + pppUnit0:
			if data, _ := os.ReadFile(filepath.Join(b.net.root, pppOptionsPath)); strings.Contains(string(data), `"one"`) {
				b.net.devices["pp0"] = true
				b.net.ppp["pp0"] = "203.0.113.7/32"
				b.net.address["pp0"] = []string{"203.0.113.7/32"}
			}
		}
	}
}

func (b *bench) applyConfirmed(files map[string]string) error {
	b.t.Helper()
	id := b.gen(files)
	if err := b.r.Activate(b.t.Context(), id); err != nil {
		return err
	}
	return b.r.Confirm(b.t.Context(), id)
}

func TestSessionsRestartOnlyWhenTheirFilesChange(t *testing.T) {
	b := newBench(t)
	b.live(one)
	reload, stop, start := "systemctl daemon-reload", "systemctl stop "+pppUnit0, "systemctl start --no-block "+pppUnit0
	for _, s := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"new", session("password \"one\"\n"), []string{reload, stop, start}},
		{"unchanged", session("password \"one\"\n"), []string{reload, start}},
		// The old pppd is stopped and gone before the new one starts, so verification never sees the old session.
		{"new password", session("password \"two\"\n"), []string{reload, stop, start}},
		{"dropped", one, []string{stop, reload}},
		{"none before or after", one, nil},
	} {
		from := len(b.net.argv)
		if err := b.applyConfirmed(s.files); err != nil {
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
	for _, path := range []string{pppUnitPath, pppOptionsPath, pppStamp("pp0")} {
		if b.exists(path) {
			t.Fatalf("%s is still there after the session was dropped", path)
		}
	}
}

func TestSessionThatWasUpMustComeBack(t *testing.T) {
	b := newBench(t)
	b.live(session("password \"one\"\n"))
	b.dialer()
	b.r.VerifyWait = 100 * b.r.Poll
	id := b.gen(session("password \"wrong\"\n"))
	if err := b.r.Activate(t.Context(), id); err == nil || !strings.Contains(err.Error(), "pp0 lost its IPv4 address") {
		t.Fatalf("activate = %v, want a failure for the lost session", err)
	}
	// Restored: the old options are back and the session redialled on them.
	data, _ := os.ReadFile(filepath.Join(b.net.root, pppOptionsPath))
	if string(data) != "password \"one\"\n" || b.net.ppp["pp0"] == "" {
		t.Fatalf("options after the restore = %q, session %q", data, b.net.ppp["pp0"])
	}
}

func TestSessionThatWasUpMayBeDropped(t *testing.T) {
	b := newBench(t)
	b.live(session("password \"one\"\n"))
	b.dialer()
	if err := b.applyConfirmed(one); err != nil {
		t.Fatalf("dropping a session that was up: %v", err)
	}
	if b.net.devices["pp0"] {
		t.Fatal("pp0 is still up")
	}
}

// A unit systemd never loaded cannot be stopped, and is not running: as after a crash between placing the files and
// the reload, or a reset run again after one.
func TestStoppingAnUnloadedSessionIsNoError(t *testing.T) {
	for _, loaded := range []bool{false, true} {
		b := newBench(t)
		b.live(session("password \"one\"\n"))
		b.net.fail, b.net.pppLoaded = "systemctl stop "+pppUnit0, loaded
		err := b.applyConfirmed(one)
		if !loaded && err != nil {
			t.Fatalf("not loaded: apply = %v", err)
		}
		if loaded && (err == nil || !strings.Contains(err.Error(), "stop the PPPoE session "+pppUnit0)) {
			t.Fatalf("loaded: apply = %v, want the failed stop", err)
		}
	}
}

func TestBootStartsSessions(t *testing.T) {
	b := newBench(t)
	b.live(one)
	if err := b.applyConfirmed(session("password \"one\"\n")); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl daemon-reload", "systemctl stop " + pppUnit0, "systemctl start --no-block " + pppUnit0}
	if got := b.sessionCommands(0); !slices.Equal(got, want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if info, err := os.Stat(filepath.Join(b.net.root, pppOptionsPath)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("options: %v %v", info, err)
	}
}

// Moving a DHCP uplink to PPPoE on the same interface: the parent gives up its address, and the session must carry the
// default route instead.
func TestDHCPUplinkMovesToPPPoE(t *testing.T) {
	parent := "[Match]\nName=eth0\n\n[Network]\nLinkLocalAddressing=no\n"
	for _, password := range []string{"one", "wrong"} {
		b := newBench(t)
		b.live(one)
		b.dialer()
		delete(b.net.ppp, "pp0")
		delete(b.net.devices, "pp0")
		b.net.apply()
		b.r.VerifyWait = 100 * b.r.Poll
		files := session("password \"" + password + "\"\n")
		files["50-andaru-eth0.network"] = parent
		err := b.applyConfirmed(files)
		if password == "one" && err != nil {
			t.Fatalf("move to PPPoE: %v", err)
		}
		if password == "wrong" && (err == nil || !strings.Contains(err.Error(), "the IPv4 default route is gone")) {
			t.Fatalf("session that never comes up: %v, want the lost default route", err)
		}
	}
}

func TestAsksIPv4(t *testing.T) {
	for data, want := range map[string]bool{
		"DHCP=yes\n": true, "DHCP=ipv4\n": true, "Address=192.0.2.1/24\n": true, "Address=nonsense\n": true,
		"DHCP=ipv6\n": false, "DHCP=no\n": false, "Address=2001:db8::1/64\n": false, "": false,
	} {
		if got := asksIPv4([]byte("[Network]\n" + data)); got != want {
			t.Errorf("asksIPv4(%q) = %v, want %v", data, got, want)
		}
	}
}

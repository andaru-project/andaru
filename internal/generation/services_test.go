package generation

import (
	"maps"
	"path/filepath"
	"testing"
)

// served is one generation's network file plus the DHCP server, the DNS service and the host resolver.
func served(kea string) map[string]string {
	out := map[string]string{keaFile: kea, unboundFile: "server:\n", resolvedFile: "[Resolve]\nDNS=127.0.0.1 ::1\n"}
	maps.Copy(out, one)
	return out
}

func TestActivateStartsServicesAfterNetworkd(t *testing.T) {
	b := newBench(t)
	id := b.gen(false, served(`{"Dhcp4":{}}`))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	order := []string{"networkctl reload", "kea-dhcp4 -t " + keaFile, "systemctl restart kea-dhcp4-server.service", "unbound-checkconf", "unbound-control reload_keep_cache", "systemctl try-reload-or-restart systemd-resolved.service"}
	last := -1
	for _, c := range order {
		i := b.index(c)
		if i <= last {
			t.Fatalf("%q out of order in %q", c, b.net.argv)
		}
		last = i
	}
	if !b.exists("run/kea/andaru-leases-changed") || !b.exists("run/andaru-leases") {
		t.Fatal("lease sync not signalled or its directory missing")
	}
}

func TestInvalidServiceConfigRestoresThePrevious(t *testing.T) {
	b := newBench(t)
	b.net.fail = "kea-dhcp4 -t"
	id := b.gen(false, served(`{"Dhcp4":{}}`))
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activated a configuration Kea refuses")
	}
	if b.exists(keaFile[1:]) || b.index("systemctl restart kea-dhcp4-server.service") >= 0 || b.read(id).State.State != Failed {
		t.Fatalf("Kea started or its file stayed: %q %+v", b.net.argv, b.read(id).State)
	}
}

func TestKeaNotRunningFailsActivation(t *testing.T) {
	b := newBench(t)
	b.net.fail = "systemctl is-active --quiet kea-dhcp4-server.service"
	id := b.gen(false, served(`{"Dhcp4":{}}`))
	if err := b.r.Activate(t.Context(), id); err == nil || b.read(id).State.State != Failed {
		t.Fatalf("activation survived a DHCP server that does not run: %v", err)
	}
	if b.index("systemctl stop kea-dhcp4-server.service") < 0 {
		t.Fatalf("Kea left running after the restore: %q", b.net.argv)
	}
}

func TestRollbackToBaselineStopsServices(t *testing.T) {
	b := newBench(t)
	id := b.gen(false, served(`{"Dhcp4":{}}`))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{keaFile, unboundFile, resolvedFile} {
		if b.exists(path[1:]) {
			t.Fatalf("%s left on the baseline", path)
		}
	}
	for _, c := range []string{"systemctl stop kea-dhcp4-server.service", "unbound-control reload_keep_cache", "systemctl try-reload-or-restart systemd-resolved.service"} {
		if b.index(c) < 0 {
			t.Fatalf("%q not run: %q", c, b.net.argv)
		}
	}
}

func TestGenerationWithoutServicesTouchesNoDaemon(t *testing.T) {
	b := newBench(t)
	id := b.gen(false, one)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"kea", "unbound", "systemctl restart", "systemctl stop", "systemctl try-reload"} {
		for _, a := range b.net.argv {
			if len(a) >= len(c) && (a[:len(c)] == c || filepath.Base(a) == c) {
				t.Fatalf("ran %q without any service", a)
			}
		}
	}
}

func TestBootCreatesServiceDirectories(t *testing.T) {
	b := cloud(t)
	id := b.gen(true, served(`{"Dhcp4":{}}`))
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
	for _, path := range []string{keaFile[1:], "run/andaru-unbound", "run/andaru-leases"} {
		if !b.exists(path) {
			t.Fatalf("%s missing after boot", path)
		}
	}
}

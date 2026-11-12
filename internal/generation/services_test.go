package generation

import (
	"maps"
	"os"
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
	order := []string{"networkctl reload", "kea-dhcp4 -t " + keaFile, "systemctl restart kea-dhcp4-server.service", "unbound-helper root_trust_anchor_update", "unbound-checkconf", "systemctl restart unbound.service", "systemctl try-reload-or-restart systemd-resolved.service"}
	last := -1
	for _, c := range order {
		i := b.index(c)
		if i <= last {
			t.Fatalf("%q out of order in %q", c, b.net.argv)
		}
		last = i
	}
	// The sync is started, never signalled by a write as root into Kea's own directory.
	if b.index("systemctl start --no-block andaru-leases.service") < 0 || b.exists("run/kea/andaru-leases-changed") || !b.exists("run/andaru-leases") {
		t.Fatalf("lease sync not started or its directory missing: %q", b.net.argv)
	}
}

// The unit's own anchor update fails quietly (ExecStartPre=-); unbound-checkconf judges the result.
func TestAnchorUpdateFailureLeavesTheCheckToUnbound(t *testing.T) {
	b := newBench(t)
	b.net.fail = "unbound-helper"
	if err := b.r.Activate(t.Context(), b.gen(false, served(`{"Dhcp4":{}}`))); err != nil || b.index("unbound-checkconf") < 0 {
		t.Fatalf("%v; the check did not run after the helper failed: %q", err, b.net.argv)
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

func TestKeaCheckWaitsForLateInterfaces(t *testing.T) {
	b := newBench(t)
	// networkctl reload returns before networkd has created every VLAN and bridge: Kea refuses an absent interface.
	checks := 0
	b.net.fail, b.net.failWith = "kea-dhcp4 -t", "interface 'vlan99' doesn't exist in the system"
	b.net.onRun = func(command string) {
		if command == "kea-dhcp4 -t "+keaFile {
			if checks++; checks == 3 {
				b.net.fail = ""
			}
		}
	}
	id := b.gen(false, served(`{"Dhcp4":{}}`))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatalf("activation failed while the served interface was still being created: %v", err)
	}
	// Any other refusal is final at once: retrying a bad configuration only delays the rollback.
	if checks != 3 {
		t.Fatalf("checked %d times", checks)
	}
	if b.index("systemctl restart kea-dhcp4-server.service") < 0 {
		t.Fatalf("Kea not started: %q", b.net.argv)
	}
}

func TestKeaNotRunningFailsActivation(t *testing.T) {
	b := newBench(t)
	// systemd reports a Type=simple unit active at fork; only Kea's own answer says it serves.
	b.net.keaDead = true
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
	for _, c := range []string{"systemctl stop kea-dhcp4-server.service", "systemctl restart unbound.service", "systemctl try-reload-or-restart systemd-resolved.service"} {
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

func TestUnboundRefusalRestoresThePrevious(t *testing.T) {
	b := newBench(t)
	b.net.fail = "unbound-checkconf"
	id := b.gen(false, served(`{"Dhcp4":{}}`))
	if err := b.r.Activate(t.Context(), id); err == nil || b.read(id).State.State != Failed || b.exists(unboundFile[1:]) {
		t.Fatalf("activated a configuration Unbound refuses: %v", err)
	}
}

// Kea answers its socket while it is still failing to bind; only "ready" sockets serve.
func TestKeaSocketsNotReadyFailsActivation(t *testing.T) {
	b := newBench(t)
	b.net.keaSockets = "retrying"
	id := b.gen(false, served(`{"Dhcp4":{}}`))
	if err := b.r.Activate(t.Context(), id); err == nil || b.read(id).State.State != Failed {
		t.Fatalf("activation passed with sockets retrying: %v", err)
	}
}

// Without DHCP the old clients' names go before Unbound starts again.
func TestRemovingDHCPClearsLeaseNames(t *testing.T) {
	b := newBench(t)
	first := b.gen(false, served(`{"Dhcp4":{}}`))
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.net.root, leasesDir, "leases.conf"), "server:\n\tlocal-data: \"old.home.arpa. 60 IN A 192.168.40.9\"\n")
	noKea := served("")
	delete(noKea, keaFile)
	if err := b.r.Activate(t.Context(), b.gen(false, noKea)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(b.net.root, leasesDir, "leases.conf")); string(data) != "server:\n" {
		t.Fatalf("old names kept: %q", data)
	}
}

func TestBadKeaConfigFailsWithoutWaiting(t *testing.T) {
	b := newBench(t)
	b.net.fail = "kea-dhcp4 -t"
	checks := 0
	b.net.onRun = func(command string) {
		if command == "kea-dhcp4 -t "+keaFile {
			checks++
		}
	}
	if err := b.r.Activate(t.Context(), b.gen(false, served(`{"Dhcp4":{}}`))); err == nil || checks != 1 {
		t.Fatalf("a refused configuration was retried %d times (%v)", checks, err)
	}
}

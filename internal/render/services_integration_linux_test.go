//go:build integration

package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Each service golden is accepted by the daemon's own checker, as Debian 13 ships it.
func TestServiceGoldensPassTheirTools(t *testing.T) {
	for _, tool := range []string{"kea-dhcp4", "unbound-checkconf"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	// Debian's and Ubuntu's AppArmor profiles let kea-dhcp4 read only its own directories and, with
	// packaging/apparmor installed, Andaru's: the configuration is staged where it lives on a host.
	if err := os.MkdirAll("/run/andaru-kea", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"services-recursive", "services-forward-dot", "services-static-leases"} {
		dir := t.TempDir()
		for _, f := range render(t, fixture(t, name)).Files {
			switch f.Path {
			case keaPath:
				path := "/run/andaru-kea/test-" + name + ".conf"
				t.Cleanup(func() { os.Remove(path) })
				// kea-dhcp4 -t refuses interfaces this host lacks and hooks it cannot load: the fixtures name
				// amd64's hook directory and cards that are not here.
				hooks, _ := filepath.Glob("/usr/lib/*/kea/hooks")
				if len(hooks) == 0 {
					t.Skip("no Kea hook directory")
				}
				content := strings.ReplaceAll(strings.ReplaceAll(f.Content, `"ens19"`, `"lo"`), allFacts.KeaHooks, hooks[0])
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("kea-dhcp4", "-t", path).CombinedOutput(); err != nil {
					t.Fatalf("%s: kea-dhcp4 refused it: %s", name, out)
				}
			case unboundPath:
				fragment, main := filepath.Join(dir, "andaru.conf"), filepath.Join(dir, "unbound.conf")
				if err := os.WriteFile(fragment, []byte(f.Content), 0o644); err != nil {
					t.Fatal(err)
				}
				// As Debian's unbound.conf includes Andaru's file.
				if err := os.WriteFile(main, []byte("server:\n\tchroot: \"\"\ninclude: \""+fragment+"\"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("unbound-checkconf", main).CombinedOutput(); err != nil || !strings.Contains(string(out), "no errors") {
					t.Fatalf("%s: unbound-checkconf refused it: %s", name, out)
				}
			}
		}
	}
}

// Kea's own checker accepts the DHCPv6 configuration, with and without the DNS service's options.
func TestDHCPv6GoldensPassKea(t *testing.T) {
	if _, err := exec.LookPath("kea-dhcp6"); err != nil {
		t.Skip("kea-dhcp6 not installed")
	}
	hooks, _ := filepath.Glob("/usr/lib/*/kea/hooks")
	if len(hooks) == 0 {
		t.Skip("no Kea hook directory")
	}
	if err := os.MkdirAll("/run/andaru-kea", 0o755); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]Input{
		"dhcp6":        fixture(t, "dhcp6"),
		"dhcp6-no-dns": change(t, fixture(t, "dhcp6"), func(d map[string]any) { delete(d, "dns") }),
	} {
		path := "/run/andaru-kea/test-" + name + ".conf"
		t.Cleanup(func() { os.Remove(path) })
		content := strings.ReplaceAll(strings.ReplaceAll(fileContent(t, render(t, in), kea6Path), `"ens19"`, `"lo"`), allFacts.KeaHooks, hooks[0])
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("kea-dhcp6", "-t", path).CombinedOutput(); err != nil {
			t.Fatalf("%s: kea-dhcp6 refused it: %s", name, out)
		}
	}
}

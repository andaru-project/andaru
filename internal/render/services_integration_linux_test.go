//go:build integration

package render

import (
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/delegation"
)

// Each service golden is accepted by the daemon's own checker, as Fedora ships it.
func TestServiceGoldensPassTheirTools(t *testing.T) {
	for _, tool := range []string{"kea-dhcp4", "unbound-checkconf"} {
		if _, err := exec.LookPath(tool); err != nil {
			skipMissingTool(t, tool+" not installed")
		}
	}
	// The configuration is staged where it lives on a host.
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
				// kea-dhcp4 -t refuses interfaces this host lacks: the fixtures name cards
				// that are not here, so the check uses lo.
				if _, err := os.Stat(allFacts.KeaHooks); err != nil {
					skipMissingTool(t, "no Kea hook directory")
				}
				content := strings.ReplaceAll(f.Content, `"ens19"`, `"lo"`)
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
				// As Fedora's unbound.conf includes Andaru's file, where it is installed; otherwise a minimal main
				// file. One fixture at a time: two would bind the same addresses.
				if _, err := os.Stat("/etc/unbound/conf.d"); err == nil {
					main = "/etc/unbound/unbound.conf"
					if err := os.WriteFile("/etc/unbound/conf.d/andaru-test.conf", []byte(f.Content), 0o644); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(main, []byte("server:\n\tchroot: \"\"\ninclude: \""+fragment+"\"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				out, err := exec.Command("unbound-checkconf", main).CombinedOutput()
				os.Remove("/etc/unbound/conf.d/andaru-test.conf")
				if err != nil || !strings.Contains(string(out), "no errors") {
					t.Fatalf("%s: unbound-checkconf refused it: %s", name, out)
				}
			}
		}
	}
}

// Kea's own checker accepts the DHCPv6 configuration, with and without the DNS service's options.
func TestDHCPv6GoldensPassKea(t *testing.T) {
	if _, err := exec.LookPath("kea-dhcp6"); err != nil {
		skipMissingTool(t, "kea-dhcp6 not installed")
	}
	if _, err := os.Stat(allFacts.KeaHooks); err != nil {
		skipMissingTool(t, "no Kea hook directory")
	}
	if err := os.MkdirAll("/run/andaru-kea", 0o755); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]Input{
		"dhcp6":           fixture(t, "dhcp6"),
		"dhcp6-no-dns":    change(t, fixture(t, "dhcp6"), func(d map[string]any) { delete(d, "dns") }),
		"dhcp6-delegated": fixture(t, "dhcp6-delegated"),
	} {
		path := "/run/andaru-kea/test-" + name + ".conf"
		t.Cleanup(func() { os.Remove(path) })
		result := render(t, in)
		kea := fileContent(t, result, kea6Path)
		if tpl, ok := fileOf(result, kea6TemplatePath); ok {
			// Filled as root fills it, with a documentation prefix standing in for the WAN's.
			var parsed delegation.Template
			err := json.Unmarshal([]byte(tpl), &parsed)
			subnets, _ := delegation.FillTemplate(parsed, map[string]delegation.Held{"ens18": {Prefix: netip.MustParsePrefix("2001:db8::/48")}})
			var conf map[string]map[string]any
			if err != nil || json.Unmarshal([]byte(kea), &conf) != nil {
				t.Fatalf("%s: %v", name, err)
			}
			conf["Dhcp6"]["subnet6"] = subnets
			raw, _ := json.Marshal(conf)
			kea = string(raw)
		}
		content := strings.ReplaceAll(kea, `"ens19"`, `"lo"`)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("kea-dhcp6", "-t", path).CombinedOutput(); err != nil {
			t.Fatalf("%s: kea-dhcp6 refused it: %s", name, out)
		}
	}
}

func fileOf(result Result, path string) (string, bool) {
	for _, f := range result.Files {
		if f.Path == path {
			return f.Content, true
		}
	}
	return "", false
}

//go:build integration

package render

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests hand rendered files to the real Debian tools without bringing
// up a link: pppd parses and exits, systemd verifies, and a private networkd
// loads the files in its own network and mount namespaces.

const sentinel = "TEST_ONLY_PPP_SECRET"

func tool(t *testing.T, paths ...string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("tool proofs require root; this is not a skipped proof")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required for this proof: %v", err)
		}
	}
}

func workDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "andaru-render-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func run(t *testing.T, name string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	}
	t.Fatalf("%s: %v\n%s", name, err, out)
	return "", 0
}

func hostLinks(t *testing.T) string {
	out, code := run(t, "/usr/bin/ip", "-j", "link", "show")
	if code != 0 {
		t.Fatal(out)
	}
	return out
}

func TestPppdAcceptsRenderedOptions(t *testing.T) {
	tool(t, "/dev/ppp", "/usr/sbin/pppd", "/usr/bin/unshare")
	dry := func(t *testing.T, options, parent string) (string, int) {
		path := filepath.Join(workDir(t), "options")
		if err := os.WriteFile(path, []byte(options), 0o600); err != nil {
			t.Fatal(err)
		}
		// The plugin checks nic- against a live interface while parsing; dryrun exits before any packet.
		return run(t, "/usr/bin/unshare", "-n", "sh", "-c", `ip link add "$1" mtu 1508 type dummy && exec /usr/sbin/pppd file "$2" logfd 1 dryrun`, "sh", parent, path)
	}
	hostile := "a \"q\" \\ # b\nc"
	cases := []struct{ fixture, parent, user, wantUser, mtu string }{
		{"owner", "vl000000020000", "kpn@internet", "user kpn@internet\t", "1500"},
		{"owner", "vl000000020000", hostile, `user a \"q\" \\ # b\nc` + "\t", "1500"},
		{"pppoe-ethernet", "enx-1.2", "kpn@internet", "user kpn@internet\t", "1492"},
	}
	for _, tc := range cases {
		in := change(t, fixture(t, tc.fixture), func(d map[string]any) {
			for i := range d["links"].([]any) {
				if item(d, "links", i)["kind"] == "pppoe" {
					item(d, "links", i)["username"] = tc.user
				}
			}
		})
		in.Secrets = map[string]string{"WAN_PASSWORD": sentinel + ` "x" \ #`}
		options := fileEnding(t, render(t, in), ".options").Real()
		out, code := dry(t, options, tc.parent)
		for _, want := range []string{"pppd options in effect:", "ifname pp", "mtu " + tc.mtu + "\t", "mru " + tc.mtu + "\t", tc.wantUser, "password ??????", "noauth", "persist"} {
			if code != 0 || !strings.Contains(out, want) {
				t.Fatalf("%s: exit %d, missing %q:\n%s", tc.fixture, code, want, out)
			}
		}
		if strings.Contains(out, sentinel) {
			t.Fatal("pppd echoed the password")
		}
		if out, code := dry(t, options+"bogusopt\n", tc.parent); code != 2 {
			t.Fatalf("an unknown option was accepted, so this proof cannot fail: exit %d\n%s", code, out)
		}
	}
}

func TestSystemdAcceptsRenderedUnit(t *testing.T) {
	tool(t, "/usr/bin/systemd-analyze", "/usr/bin/systemd-escape", "/usr/sbin/pppd", "/usr/lib/systemd/systemd-networkd-wait-online")
	for fixtureName, parent := range map[string]string{"owner": "vl000000020000", "pppoe-ethernet": "enx-1.2"} {
		unit := fileEnding(t, render(t, fixture(t, fixtureName)), ".service")
		path := filepath.Join(workDir(t), filepath.Base(unit.Path))
		if err := os.WriteFile(path, []byte(unit.Content), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := run(t, "/usr/bin/systemd-analyze", "verify", path); code != 0 || out != "" {
			t.Fatalf("%s: exit %d\n%s", fixtureName, code, out)
		}
		escaped, _ := run(t, "/usr/bin/systemd-escape", "--path", "/sys/subsystem/net/devices/"+parent)
		if want := "BindsTo=" + strings.TrimSpace(escaped) + ".device\n"; !strings.Contains(unit.Content, want) {
			t.Fatalf("device unit name differs from systemd's own escaping; want %q in\n%s", want, unit.Content)
		}
		if err := os.WriteFile(path, []byte(unit.Content+"Bogus=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, _ := run(t, "/usr/bin/systemd-analyze", "verify", path); out == "" {
			t.Fatal("an unknown key was accepted silently, so this proof cannot fail")
		}
	}
}

const privateNetworkd = `set -e
mkdir -p /run/systemd/network /run/systemd/netif
mount -t tmpfs tmpfs /run/systemd/network
mount -t tmpfs -o mode=0755 tmpfs /run/systemd/netif
chown systemd-network:systemd-network /run/systemd/netif
install -d -o systemd-network -g systemd-network -m 0755 /run/systemd/netif/links /run/systemd/netif/leases /run/systemd/netif/lldp
install -m 0644 "$1"/* /run/systemd/network/
SYSTEMD_LOG_LEVEL=debug SYSTEMD_LOG_TARGET=console timeout 5 /usr/lib/systemd/systemd-networkd 2>&1 || true
`

func TestNetworkdAcceptsRenderedFiles(t *testing.T) {
	tool(t, "/usr/lib/systemd/systemd-networkd", "/usr/bin/unshare")
	before := hostLinks(t)
	load := func(t *testing.T, files map[string]string) string {
		dir := workDir(t)
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, _ := run(t, "/usr/bin/unshare", "-n", "-m", "sh", "-c", privateNetworkd, "sh", dir)
		return out
	}
	complaints := func(log string) (bad []string) {
		for _, line := range strings.Split(log, "\n") {
			if !strings.Contains(line, "50-andaru-") {
				continue
			}
			for _, word := range []string{"Unknown", "Failed", "Invalid", "ignoring", "Ignoring"} {
				if strings.Contains(line, word) {
					bad = append(bad, line)
				}
			}
		}
		return
	}
	names, _ := testdata.ReadDir("testdata")
	checked := 0
	for _, entry := range names {
		name, ok := strings.CutSuffix(entry.Name(), ".links.json")
		if !ok {
			continue
		}
		files := map[string]string{}
		for _, f := range render(t, fixture(t, name)).Files {
			if strings.HasPrefix(f.Path, "/run/systemd/network/") {
				files[filepath.Base(f.Path)] = f.Content
			}
		}
		log := load(t, files)
		for file := range files {
			// .link files are udev's: networkd never reads them, and nothing checks them offline.
			if strings.HasSuffix(file, ".link") {
				continue
			}
			if !strings.Contains(log, "/run/systemd/network/"+file+": Successfully loaded.") {
				t.Fatalf("%s: networkd did not load %s:\n%s", name, file, log)
			}
			checked++
		}
		if bad := complaints(log); len(bad) != 0 {
			t.Fatalf("%s: networkd rejected rendered configuration:\n%s", name, strings.Join(bad, "\n"))
		}
	}
	if checked < 15 {
		t.Fatalf("only %d files reached networkd", checked)
	}
	broken := load(t, map[string]string{"50-andaru-x.network": "[Match]\nName=x\n\n[Network]\nBogusKey=1\n"})
	if len(complaints(broken)) == 0 {
		t.Fatalf("an unknown key was accepted silently, so this proof cannot fail:\n%s", broken)
	}
	if after := hostLinks(t); after != before {
		t.Fatalf("the private networkd changed host links:\n%s\n%s", before, after)
	}
}

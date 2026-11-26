//go:build integration

package render

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPPPoESessionInNamespaces dials a real session with the rendered options: rp-pppoe's pppoe-server answers in one
// network namespace, pppd runs the rendered options in another, joined by a veth pair named as the parent the options
// name. It proves what the offline proofs cannot: the credentials as quoted reach the server intact, the session's
// interface gets its name, MTU, address and default route, and a wrong password does not come up.
func TestPPPoESessionInNamespaces(t *testing.T) {
	for _, path := range []string{"/dev/ppp", "/usr/sbin/pppd", "/usr/sbin/pppoe-server", "/usr/bin/unshare"} {
		if _, err := os.Stat(path); err != nil {
			skipMissingTool(t, "a PPPoE session needs "+path)
		}
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespaces need root")
	}
	const user, password = `kpn "q" \ # b`, `TEST_ONLY "x" \ #`
	cases := []struct {
		fixture, parent string
		mtu             int
	}{
		{"pppoe-ethernet", "enx-1.2", 1492},
		// RFC 4638: a 1500-byte session needs 1508 on the parent.
		{"owner", "vl000000020000", 1500},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			isp, box := namespace(t), namespace(t)
			mtu, parent := strconv.Itoa(tc.mtu), strconv.Itoa(tc.mtu+8)
			ip(t, "link", "add", "isp0", "netns", isp, "mtu", parent, "type", "veth", "peer", "name", tc.parent, "netns", box, "mtu", parent)
			ip(t, "-n", isp, "link", "set", "isp0", "up")
			ip(t, "-n", box, "link", "set", tc.parent, "up")

			// The server's pppd reads /etc/ppp, so it gets a private one in its own mount namespace.
			etc := workDir(t)
			// Written by hand, not with quoteOption: the same mistake on both sides would still authenticate.
			write(t, filepath.Join(etc, "chap-secrets"), `"kpn \"q\" \\ # b" * "TEST_ONLY \"x\" \\ #" *`+"\n")
			write(t, filepath.Join(etc, "server"), "require-chap\nlcp-echo-interval 5\nlcp-echo-failure 3\nmtu "+mtu+"\nmru "+mtu+"\n")
			start(t, "ip", "netns", "exec", isp, "unshare", "-m", "sh", "-c",
				`mount --bind "$1" /etc/ppp && exec pppoe-server -F -I isp0 -L 10.64.0.1 -R 10.64.0.10 -N 4 -O /etc/ppp/server`, "sh", etc)

			dial := func(t *testing.T, secret string) (string, *bytes.Buffer) {
				in := change(t, fixture(t, tc.fixture), func(d map[string]any) {
					for i := range d["links"].([]any) {
						if item(d, "links", i)["kind"] == "pppoe" {
							item(d, "links", i)["username"] = user
						}
					}
				})
				in.Secrets = map[string]string{"WAN_PASSWORD": secret}
				options := fileEnding(t, render(t, in), ".options")
				path := filepath.Join(workDir(t), "options")
				write(t, path, options.Real())
				name := strings.TrimSuffix(filepath.Base(options.Path), ".options")
				// As the unit runs it, less up_sdnotify, which needs systemd; logfd overrides the options' nolog.
				log := start(t, "ip", "netns", "exec", box, "/usr/sbin/pppd", "file", path, "nodetach", "logfd", "2")
				return name, log
			}

			t.Run("dials", func(t *testing.T) {
				name, log := dial(t, password)
				link := waitLink(t, box, name, 20*time.Second)
				if link == nil {
					t.Fatalf("%s never got its address:\n%s", name, log)
				}
				if got := int(link["mtu"].(float64)); got != tc.mtu {
					t.Fatalf("MTU %d, want %d:\n%s", got, tc.mtu, log)
				}
				// The realizer counts a session that was up only if the kernel calls it a ppp link.
				if kind, _ := link["linkinfo"].(map[string]any)["info_kind"].(string); kind != "ppp" {
					t.Fatalf("link kind %q, want ppp: %v", kind, link)
				}
				if out := ip(t, "-n", box, "route", "show", "default"); !strings.Contains(out, "dev "+name) {
					t.Fatalf("default route %q does not leave through %s", out, name)
				}
				if strings.Contains(log.String(), "TEST_ONLY") {
					t.Fatalf("pppd logged the password:\n%s", log)
				}
			})
			t.Run("refuses a wrong password", func(t *testing.T) {
				name, log := dial(t, password+"x")
				if link := waitLink(t, box, name, 8*time.Second); link != nil {
					t.Fatalf("a wrong password came up:\n%s", log)
				}
				if !strings.Contains(log.String(), "CHAP authentication failed") {
					t.Fatalf("no authentication failure, so this proof cannot fail for the right reason:\n%s", log)
				}
			})
		})
	}
}

// namespace creates a network namespace that the test removes.
func namespace(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "andaru-test-" + hex.EncodeToString(b)
	ip(t, "netns", "add", name)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", name).Run() })
	return name
}

func ip(t *testing.T, args ...string) string {
	t.Helper()
	out, code := run(t, "/usr/bin/ip", args...)
	if code != 0 {
		t.Fatalf("ip %v: exit %d\n%s", args, code, out)
	}
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// start runs a daemon in its own process group, which the test kills with everything it forked.
func start(t *testing.T, name string, args ...string) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	})
	return &out
}

// waitLink returns the session's interface once it has the address the server hands out, or nil.
func waitLink(t *testing.T, netns, name string, wait time.Duration) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	for ctx.Err() == nil {
		out, err := exec.Command("ip", "-n", netns, "-d", "-j", "address", "show", "dev", name).Output()
		var links []map[string]any
		if err == nil && json.Unmarshal(out, &links) == nil && len(links) == 1 && strings.Contains(string(out), `"local":"10.64.0.10"`) {
			return links[0]
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

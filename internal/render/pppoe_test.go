package render

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

func fileEnding(t *testing.T, r Result, suffix string) File {
	t.Helper()
	for _, f := range r.Files {
		if strings.HasSuffix(f.Path, suffix) {
			return f
		}
	}
	t.Fatalf("no file ending %q in %s", suffix, golden(r))
	return File{}
}

func TestRedactionByConstruction(t *testing.T) {
	// The owner's line: the password is a substring of the username.
	options := fileEnding(t, render(t, fixture(t, "owner")), ".options")
	if !options.Secret || options.Mode != "0600" ||
		!strings.Contains(options.Content, `user "kpn@internet"`) || !strings.Contains(options.Content, `password "<redacted>"`) ||
		strings.Contains(options.Content, `password "kpn"`) || !strings.Contains(options.Real(), `password "kpn"`) {
		t.Fatalf("shown:\n%s\nreal:\n%s", options.Content, options.Real())
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	in := fixture(t, "owner")
	in.Secrets = map[string]string{"WAN_PASSWORD": "TEST_ONLY_PPP_SECRET"}
	result := render(t, in)
	body, _ := json.Marshal(result)
	if strings.Contains(string(body)+logs.String(), "TEST_ONLY_PPP_SECRET") {
		t.Fatal("secret reached the serialised result or the log")
	}
	if !strings.Contains(fileEnding(t, result, ".options").Real(), `password "TEST_ONLY_PPP_SECRET"`) {
		t.Fatal("real options file lost its password")
	}
}

func TestQuoteOption(t *testing.T) {
	// pppd keeps every byte of a quoted word except that \ escapes the next one.
	if got := quoteOption("a \"q\" \\ # b\nc\t\r"); got != "\"a \\\"q\\\" \\\\ # b\nc\t\r\"" {
		t.Fatalf("%q", got)
	}
}

func TestDeviceUnit(t *testing.T) {
	for name, want := range map[string]string{
		"eth0":           `sys-subsystem-net-devices-eth0.device`,
		"enx-1.2":        `sys-subsystem-net-devices-enx\x2d1.2.device`,
		"vl000000020000": `sys-subsystem-net-devices-vl000000020000.device`,
	} {
		if got := deviceUnit(name); got != want {
			t.Fatalf("%s: %s", name, got)
		}
	}
}

func TestPPPoEFindings(t *testing.T) {
	v6 := func(d map[string]any) map[string]any {
		return item(d, "segments", 0)["addressing"].(map[string]any)["ipv6"].(map[string]any)
	}
	long := strings.Repeat("S", 300)
	cases := []struct {
		name, severity, code, path string
		rendered                   bool
		in                         func() Input
	}{
		{"256-byte username", "error", "CREDENTIAL_TOO_LONG", "/links/2/username", true, func() Input {
			return change(t, fixture(t, "owner"), func(d map[string]any) { item(d, "links", 2)["username"] = strings.Repeat("u", 256) })
		}},
		{"300-byte password", "error", "CREDENTIAL_TOO_LONG", "/links/2/password", true, func() Input {
			in := fixture(t, "owner")
			in.Secrets = map[string]string{"WAN_PASSWORD": long}
			return in
		}},
		{"static IPv6 on PPP", "error", "UNSUPPORTED_COMBINATION", "/segments/0/addressing/ipv6/mode", false, func() Input {
			return change(t, fixture(t, "pppoe-ethernet"), func(d map[string]any) {
				v6(d)["mode"] = "static"
				v6(d)["addresses"] = []any{"2001:db8::1/64"}
			})
		}},
		{"delegated IPv6 on PPP", "error", "UNSUPPORTED_COMBINATION", "/segments/0/addressing/ipv6/mode", false, func() Input {
			return change(t, fixture(t, "owner"), func(d map[string]any) {
				v6(d)["mode"] = "delegated"
				v6(d)["from"] = item(d, "segments", 1)["id"]
			})
		}},
		{"RA on PPP", "error", "UNSUPPORTED_COMBINATION", "/segments/0/addressing/ipv6/ra", false, func() Input {
			return change(t, fixture(t, "owner"), func(d map[string]any) {
				v6(d)["ra"] = map[string]any{"enabled": true, "managed": false, "otherInfo": false}
			})
		}},
		{"no protocol at all", "error", "UNSUPPORTED_COMBINATION", "/segments/0/addressing", false, func() Input {
			return change(t, fixture(t, "pppoe-ethernet"), func(d map[string]any) {
				item(d, "segments", 0)["addressing"].(map[string]any)["ipv4"] = map[string]any{"mode": "none"}
			})
		}},
		{"no segment", "warning", "UNSUPPORTED_COMBINATION", "/links/1", false, func() Input {
			return change(t, fixture(t, "pppoe-ethernet"), func(d map[string]any) { d["segments"] = []any{} })
		}},
		{"pppd absent", "error", "TOOL_MISSING", "", true, func() Input {
			in := fixture(t, "owner")
			in.Facts = host.RealizerFacts{PPPoEPlugin: true, PPPDevice: true}
			return in
		}},
		{"plugin absent", "error", "TOOL_MISSING", "", true, func() Input {
			in := fixture(t, "owner")
			in.Facts = host.RealizerFacts{PPPDaemon: true, PPPDevice: true}
			return in
		}},
		{"cloud kernel", "error", "KERNEL_SUPPORT_MISSING", "", true, func() Input {
			in := fixture(t, "owner")
			in.Facts = host.RealizerFacts{PPPDaemon: true, PPPoEPlugin: true}
			return in
		}},
	}
	for _, hostile := range []string{"a%b", "a$b", "a#b", "a b", `a"b`, `a\b`} {
		cases = append(cases, struct {
			name, severity, code, path string
			rendered                   bool
			in                         func() Input
		}{"parent named " + hostile, "error", "INTERFACE_NAME_UNSUPPORTED", "/links/1/parent", false, func() Input {
			in := fixture(t, "pppoe-ethernet")
			in.Links[0].Name = hostile
			return in
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := render(t, tc.in())
			if !hasFinding(result, tc.severity, tc.code, tc.path) || hasFile(result, ".options") != tc.rendered || hasFile(result, ".service") != tc.rendered {
				t.Fatalf("want %s %s at %q rendered=%v:\n%s", tc.severity, tc.code, tc.path, tc.rendered, golden(result))
			}
			if body, _ := json.Marshal(result); strings.Contains(string(body), long) {
				t.Fatal("over-long secret leaked through its finding")
			}
		})
	}
	exact := change(t, fixture(t, "owner"), func(d map[string]any) { item(d, "links", 2)["username"] = strings.Repeat("u", 255) })
	exact.Secrets = map[string]string{"WAN_PASSWORD": strings.Repeat("p", 255)}
	if result := render(t, exact); len(result.Findings) != 0 {
		t.Fatalf("255 bytes fits pppd's buffer: %+v", result.Findings)
	}
	disabled := change(t, fixture(t, "owner"), func(d map[string]any) { item(d, "links", 2)["enabled"] = false })
	disabled.Facts = host.RealizerFacts{}
	result := render(t, disabled)
	if hasFile(result, ".options") || hasFinding(result, "error", "TOOL_MISSING", "") || hasFinding(result, "error", "KERNEL_SUPPORT_MISSING", "") {
		t.Fatalf("a disabled PPPoE link needs no pppd: %s", golden(result))
	}
}

func TestPPPoEMTU(t *testing.T) {
	for name, want := range map[string]string{"owner": "mtu 1500\nmru 1500\n", "pppoe-1492": "mtu 1492\nmru 1492\n"} {
		if got := fileEnding(t, render(t, fixture(t, name)), ".options").Real(); !strings.Contains(got, want) {
			t.Fatalf("%s:\n%s", name, got)
		}
	}
}

func TestHostWidePPPFindingsAreRaisedOnce(t *testing.T) {
	in := change(t, fixture(t, "owner"), func(d map[string]any) {
		second := map[string]any{"id": "lnk_00000007-0000-4000-8000-000000000007", "name": "second", "kind": "pppoe", "mtu": 1492, "enabled": true,
			"parent": item(d, "links", 3)["id"], "username": "u", "password": map[string]any{"secret": "WAN_PASSWORD"}}
		d["links"] = append(d["links"].([]any), second)
		d["segments"] = append(d["segments"].([]any), map[string]any{"id": "seg_00000007-0000-4000-8000-000000000007", "name": "second", "link": second["id"], "zone": "z",
			"addressing": map[string]any{"ipv4": map[string]any{"mode": "ppp"}, "ipv6": map[string]any{"mode": "none"}}})
	})
	in.Facts = host.RealizerFacts{}
	count := map[string]int{}
	result := render(t, in)
	for _, f := range result.Findings {
		count[f.Code]++
	}
	if count["TOOL_MISSING"] != 1 || count["KERNEL_SUPPORT_MISSING"] != 1 {
		t.Fatalf("two PPPoE links repeated a host-wide finding: %v\n%s", count, golden(result))
	}
}

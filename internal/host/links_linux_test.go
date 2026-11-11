package host

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain runs controlled child processes without a shell or a fake production mode.
func TestMain(m *testing.M) {
	if i := slices.Index(os.Args, "--inventory-helper"); i >= 0 {
		mode := os.Args[i+1]
		if i+2 < len(os.Args) && os.Args[i+2] != "" {
			if err := os.WriteFile(os.Args[i+2], []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
				os.Exit(91)
			}
		}
		switch mode {
		case "boundary", "oversize":
			n := 4 * 1024 * 1024
			if mode == "oversize" {
				n++
			}
			_, _ = io.WriteString(os.Stdout, "[]"+strings.Repeat(" ", n-2))
			if mode == "oversize" {
				time.Sleep(5 * time.Second)
			}
		case "interrupted":
			_, _ = io.WriteString(os.Stdout, "[]")
			_, _ = io.WriteString(os.Stderr, "Dump was interrupted and may be inconsistent.\n")
		case "stderr":
			_, _ = io.WriteString(os.Stderr, "stderr-proof:"+strings.Repeat("x", 8192)+":stderr-proof")
			os.Exit(42)
		case "exit":
			_, _ = io.WriteString(os.Stdout, "partial")
			os.Exit(42)
		case "hang":
			_, _ = io.WriteString(os.Stdout, "partial")
			time.Sleep(5 * time.Second)
		default:
			os.Exit(92)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestParseLinksPreservesHostAddresses(t *testing.T) {
	data := []byte(`[
	 {"ifindex":2,"ifname":"eth0","link_type":"ether","operstate":"UP","mtu":1500,"flags":["UP","LOWER_UP"],"address":"02:00:00:00:00:02","unused":true,"addr_info":[{"family":"inet","local":"192.0.2.7","prefixlen":24,"scope":"global","valid_life_time":4294967295},{"family":"inet6","local":"2001:db8::7","prefixlen":64,"scope":"global"}]},
	 {"ifindex":1,"ifname":"lo","link_type":"loopback","operstate":"UNKNOWN","mtu":65536,"flags":["LOOPBACK"],"address":"00:00:00:00:00:00","addr_info":[]},
	 {"ifindex":3,"ifname":"veth-test","link_type":"ether","operstate":"DOWN","mtu":1500,"addr_info":[]}
	]`)
	got, err := ParseLinks(data)
	if err != nil || len(got) != 3 {
		t.Fatalf("snapshot: %#v, %v", got, err)
	}
	if got[0].IfIndex != 1 || got[1].IfIndex != 2 || got[2].IfIndex != 3 {
		t.Fatalf("wrong link order: %#v", got)
	}
	if got[0].Name != "lo" || got[0].OperState != "UNKNOWN" || got[0].LinkAddress != "00:00:00:00:00:00" {
		t.Fatalf("invented loopback metadata: %#v", got[0])
	}
	if got[1].Name != "eth0" || got[1].MTU != 1500 || got[1].LinkType != "ether" || got[1].LinkAddress != "02:00:00:00:00:02" || !slices.Equal(got[1].Flags, []string{"UP", "LOWER_UP"}) {
		t.Fatalf("lost link metadata: %#v", got[1])
	}
	byFamily := map[string]AddressStatus{}
	for _, address := range got[1].Addresses {
		byFamily[address.Family] = address
	}
	if len(got[1].Addresses) != 2 || byFamily["ipv4"].Address != "192.0.2.7" || byFamily["ipv4"].PrefixLength != 24 || byFamily["ipv4"].Scope != "global" || byFamily["ipv6"].Address != "2001:db8::7" || byFamily["ipv6"].PrefixLength != 64 {
		t.Fatalf("lost host bits/family/prefix: %#v", got[1].Addresses)
	}
	if got[2].LinkAddress != "" || got[2].Addresses == nil || got[2].Flags == nil || got[0].Addresses == nil {
		t.Fatalf("invented link address or null arrays: %#v", got)
	}
	body, err := json.Marshal(got[2])
	if err != nil || strings.Contains(string(body), "linkAddress") || strings.Contains(string(body), "null") {
		t.Fatalf("unknown/empty data encoded incorrectly: %s, %v", body, err)
	}
}

func validToolLink() map[string]any {
	return map[string]any{
		"ifindex": 2, "ifname": "eth0", "link_type": "ether", "operstate": "UP", "mtu": 1500,
		"flags":     []string{"UP"},
		"addr_info": []any{map[string]any{"family": "inet", "local": "192.0.2.7", "prefixlen": 24, "scope": "global"}},
	}
}

func TestParseLinksRejectsInvalidInventory(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"zero index", func(v map[string]any) { v["ifindex"] = 0 }},
		{"negative index", func(v map[string]any) { v["ifindex"] = -1 }},
		{"wrong index type", func(v map[string]any) { v["ifindex"] = "2" }},
		{"missing name", func(v map[string]any) { delete(v, "ifname") }},
		{"missing type", func(v map[string]any) { delete(v, "link_type") }},
		{"missing state", func(v map[string]any) { delete(v, "operstate") }},
		{"missing mtu", func(v map[string]any) { delete(v, "mtu") }},
		{"negative mtu", func(v map[string]any) { v["mtu"] = -1 }},
		{"bad address", func(v map[string]any) { v["addr_info"].([]any)[0].(map[string]any)["local"] = "not-an-ip" }},
		{"unknown family", func(v map[string]any) { v["addr_info"].([]any)[0].(map[string]any)["family"] = "packet" }},
		{"family mismatch", func(v map[string]any) { v["addr_info"].([]any)[0].(map[string]any)["family"] = "inet6" }},
		{"IPv6 literal in IPv4 family", func(v map[string]any) { v["addr_info"].([]any)[0].(map[string]any)["local"] = "2001:db8::7" }},
		{"negative prefix", func(v map[string]any) { v["addr_info"].([]any)[0].(map[string]any)["prefixlen"] = -1 }},
		{"ipv4 prefix", func(v map[string]any) { v["addr_info"].([]any)[0].(map[string]any)["prefixlen"] = 33 }},
		{"ipv6 prefix", func(v map[string]any) {
			a := v["addr_info"].([]any)[0].(map[string]any)
			a["family"], a["local"], a["prefixlen"] = "inet6", "2001:db8::7", 129
		}},
		{"missing prefix", func(v map[string]any) { delete(v["addr_info"].([]any)[0].(map[string]any), "prefixlen") }},
		{"missing scope", func(v map[string]any) { delete(v["addr_info"].([]any)[0].(map[string]any), "scope") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := validToolLink()
			tc.change(v)
			body, err := json.Marshal([]any{validToolLinkWithIndex(1, "first"), v})
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseLinks(body)
			if err == nil || got != nil {
				t.Fatalf("accepted invalid/partial inventory: %#v, %v", got, err)
			}
		})
	}
	for _, body := range []string{"[", "null", "{}", "[] {}"} {
		if got, err := ParseLinks([]byte(body)); err == nil || got != nil {
			t.Fatalf("accepted malformed inventory %q: %#v, %v", body, got, err)
		}
	}
	body, _ := json.Marshal([]any{validToolLink()})
	body = []byte(strings.Replace(string(body), "eth0", "eth"+string([]byte{0xff}), 1))
	if got, err := ParseLinks(body); err == nil || got != nil {
		t.Fatalf("invalid UTF-8 was silently replaced: %#v, %v", got, err)
	}
	for _, pair := range [][]any{
		{validToolLinkWithIndex(2, "eth0"), validToolLinkWithIndex(2, "other")},
		{validToolLinkWithIndex(2, "eth0"), validToolLinkWithIndex(3, "eth0")},
	} {
		body, _ := json.Marshal(pair)
		if got, err := ParseLinks(body); err == nil || got != nil {
			t.Fatalf("accepted duplicate identity: %#v, %v", got, err)
		}
	}
}

func validToolLinkWithIndex(index int, name string) map[string]any {
	v := validToolLink()
	v["ifindex"], v["ifname"] = index, name
	return v
}

func TestReadCommandBoundsAndCancellation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("exact stdout boundary", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		body, err := readCommand(ctx, executable, "--inventory-helper", "boundary")
		if err != nil || len(body) != 4*1024*1024 {
			t.Fatalf("exact boundary rejected: bytes=%d err=%v", len(body), err)
		}
		if _, err := ParseLinks(body); err != nil {
			t.Fatalf("boundary JSON corrupted: %v", err)
		}
	})
	t.Run("oversize terminates and reaps child", func(t *testing.T) {
		pidFile := filepath.Join(t.TempDir(), "pid")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		started := time.Now()
		body, err := readCommand(ctx, executable, "--inventory-helper", "oversize", pidFile)
		if !errors.Is(err, errOutputLimit) || body != nil {
			t.Fatalf("oversize not rejected at the reader: bytes=%d err=%v", len(body), err)
		}
		if elapsed := time.Since(started); elapsed >= 2*time.Second {
			t.Fatalf("oversize child survived until its context deadline: %v", elapsed)
		}
		assertReaped(t, pidFile)
	})
	t.Run("nonzero exit has no partial data", func(t *testing.T) {
		body, err := readCommand(context.Background(), executable, "--inventory-helper", "exit")
		if err == nil || body != nil {
			t.Fatalf("failed command became partial success: %q, %v", body, err)
		}
	})
	t.Run("successful exit with incomplete dump warning", func(t *testing.T) {
		body, err := readCommand(context.Background(), executable, "--inventory-helper", "interrupted")
		if err == nil || body != nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("incomplete dump became success: %q, %v", body, err)
		}
	})
	t.Run("stderr retained but bounded", func(t *testing.T) {
		body, err := readCommand(context.Background(), executable, "--inventory-helper", "stderr")
		if err == nil || body != nil || !strings.Contains(err.Error(), "stderr-proof") || len(err.Error()) > 4096+512 {
			t.Fatalf("missing/unbounded diagnostic: body=%q err=%v", body, err)
		}
	})
	t.Run("deadline cancels and reaps child", func(t *testing.T) {
		pidFile := filepath.Join(t.TempDir(), "pid")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		body, err := readCommand(ctx, executable, "--inventory-helper", "hang", pidFile)
		if !errors.Is(err, context.DeadlineExceeded) || body != nil {
			t.Fatalf("cancelled command became success/wrong error: %q, %v", body, err)
		}
		assertReaped(t, pidFile)
	})
}

func assertReaped(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid controlled child PID %q: %v", data, err)
	}
	var status syscall.WaitStatus
	got, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if !errors.Is(err, syscall.ECHILD) {
		if got == 0 && err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			_, _ = syscall.Wait4(pid, &status, 0, nil)
		}
		t.Fatal(fmt.Sprintf("child was not reaped: pid=%d wait=%d err=%v", pid, got, err))
	}
}

//go:embed testdata/ip-d-j-address.json
var detailedInventory []byte

func TestParseLinksObservesKindAndPermanentAddress(t *testing.T) {
	got, err := ParseLinks(detailedInventory)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"lo": "", "d0": "dummy", "v6": "vlan", "br0": "bridge"}
	for _, link := range got {
		if link.Kind != want[link.Name] || link.PermanentAddress != link.LinkAddress {
			t.Fatalf("%s: kind=%q permanent=%q address=%q", link.Name, link.Kind, link.PermanentAddress, link.LinkAddress)
		}
	}
	// ip prints permaddr only when it differs from the current address.
	changed, err := ParseLinks([]byte(`[{"ifindex":2,"ifname":"eth1","link_type":"ether","operstate":"UP","mtu":1500,"flags":[],"address":"02:11:22:33:44:55","permaddr":"52:54:00:aa:bb:02","addr_info":[]}]`))
	if err != nil || changed[0].PermanentAddress != "52:54:00:aa:bb:02" || changed[0].LinkAddress != "02:11:22:33:44:55" {
		t.Fatalf("permaddr not preferred: %#v %v", changed, err)
	}
	body, err := json.Marshal(append(got, changed...))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"Kind", "PermanentAddress", "permaddr", "52:54:00:aa:bb:02", "dummy"} {
		if strings.Contains(string(body), leaked) {
			t.Fatalf("status response changed: found %q", leaked)
		}
	}
}

func TestLinkInventoryArgs(t *testing.T) {
	if !slices.Equal(linkInventoryArgs, []string{"-d", "-j", "address", "show"}) {
		t.Fatalf("inventory must request link details: %v", linkInventoryArgs)
	}
}

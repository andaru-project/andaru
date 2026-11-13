package render

import (
	"strings"
	"testing"
)

func contains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q in:\n%s", w, body)
		}
	}
}

func TestDHCPv6Rendering(t *testing.T) {
	result := render(t, fixture(t, "dhcp6"))
	kea := fileContent(t, result, kea6Path)
	contains(t, kea,
		`"interface": "ens19"`, `"subnet": "fd00:1::/64"`, `"pool": "fd00:1::1000 - fd00:1::1fff"`,
		`"prefix": "fd00:beef:1::"`, `"prefix-len": 48`, `"delegated-len": 56`, `"excluded-prefix": "fd00:beef:1:ff::"`, `"excluded-prefix-len": 64`,
		`"duid": "00:01:00:01:aa:bb:cc:dd:ee:ff"`, `"ip-addresses": [`, `"fd00:1::10"`, `"prefixes": [`, `"fd00:beef:1:100::/56"`, `"hostname": "nas"`,
		`"name": "dns-servers"`, `"data": "fd00:1::1"`, `"name": "domain-search"`, `"data": "home.arpa"`,
		`"valid-lifetime": 7200`, `"preferred-lifetime": 4500`, `"service-sockets-max-retries": 30`,
		`"socket-name": "/run/kea/kea6-ctrl-socket"`, `libdhcp_lease_cmds.so`, `/usr/share/kea/scripts/andaru-lease-event`)
	contains(t, fileContent(t, result, "/run/andaru-unbound/andaru.conf"),
		`local-data: "nas.home.arpa. 3600 IN AAAA fd00:1::10"`, `local-data-ptr: "fd00:1::10 3600 nas.home.arpa."`,
		"\taccess-control: fd00:beef:1::/48 allow")
	lines(t, networkFile(t, result, "ens19"), "[Route]", "Destination=fd00:beef:1::/48", "Type=unreachable", "Managed=yes")
	contains(t, fileContent(t, result, nftPath), `iifname "ens19" ip6 saddr fe80::/10 udp sport 546 udp dport 547 accept`)
	if !result.Realizable {
		t.Fatalf("findings %+v", result.Findings)
	}
}

func TestDHCP6Unannounced(t *testing.T) {
	for mode, warns := range map[string]bool{"managed": false, "unmanaged": true, "disabled": true} {
		result := render(t, change(t, fixture(t, "dhcp6"), func(d map[string]any) {
			item(d, "segments", 1)["addressing"].(map[string]any)["ipv6"].(map[string]any)["ra"] = map[string]any{"mode": mode}
		}))
		if got := findingAt(result, "DHCP6_UNANNOUNCED", "/dhcp6/0"); got != warns {
			t.Fatalf("%s: warning %v, findings %+v", mode, got, result.Findings)
		}
	}
}

func TestDHCP6ToolMissing(t *testing.T) {
	in := fixture(t, "dhcp6")
	in.Facts.Kea6Tool = false
	if r := render(t, in); r.Realizable || !findingAt(r, "TOOL_MISSING", "/dhcp6") {
		t.Fatalf("findings %+v", r.Findings)
	}
}

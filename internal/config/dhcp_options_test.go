package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDHCPOptionsValidationAndCanonicalization(t *testing.T) {
	entry := func(d map[string]any) map[string]any { return item(d["dhcp"].([]any), 0) }
	for name, tc := range map[string]struct {
		edit func(d map[string]any)
		path string
	}{
		"gateway outside subnet": {func(d map[string]any) { entry(d)["gateway"] = "198.51.100.1" }, "/dhcp/0/gateway"},
		"gateway on network":     {func(d map[string]any) { entry(d)["gateway"] = "192.0.2.0" }, "/dhcp/0/gateway"},
		"gateway on broadcast":   {func(d map[string]any) { entry(d)["gateway"] = "192.0.2.255" }, "/dhcp/0/gateway"},
		"gateway IPv6":           {func(d map[string]any) { entry(d)["gateway"] = "fd00::1" }, "/dhcp/0/gateway"},
		"bad domainName":         {func(d map[string]any) { entry(d)["domainName"] = "bad..domain" }, "/dhcp/0/domainName"},
		"bad domainSearch":       {func(d map[string]any) { entry(d)["domainSearch"] = []any{"ok.example", "-bad"} }, "/dhcp/0/domainSearch/1"},
		"bad ntpServer":          {func(d map[string]any) { entry(d)["ntpServers"] = []any{"fd00::123"} }, "/dhcp/0/ntpServers/0"},
		"bad nextServer":         {func(d map[string]any) { entry(d)["nextServer"] = "not-an-ip" }, "/dhcp/0/nextServer"},
		"bad bootFileName":       {func(d map[string]any) { entry(d)["bootFileName"] = "pxe\nnext" }, "/dhcp/0/bootFileName"},
		"option code > 254": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 255, "type": "uint8", "data": "1"}}
		}, "/dhcp/0/options/0/code"},
		"option reserved code 3": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 3, "type": "ip", "data": "192.0.2.1"}}
		}, "/dhcp/0/options/0/code"},
		"option reserved code 42": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 42, "type": "ip", "data": "192.0.2.1"}}
		}, "/dhcp/0/options/0/code"},
		"option reserved code 67": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 67, "type": "string", "data": "pxe"}}
		}, "/dhcp/0/options/0/code"},
		"option reserved code 119": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 119, "type": "string", "data": "lan"}}
		}, "/dhcp/0/options/0/code"},
		"duplicate option code": {func(d map[string]any) {
			entry(d)["options"] = []any{
				map[string]any{"code": 224, "type": "string", "data": "a"},
				map[string]any{"code": 224, "type": "string", "data": "b"},
			}
		}, "/dhcp/0/options/1/code"},
		"string option control char": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 224, "type": "string", "data": "a\nb"}}
		}, "/dhcp/0/options/0/data"},
		"string option > 255 bytes": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 224, "type": "string", "data": strings.Repeat("a", 256)}}
		}, "/dhcp/0/options/0/data"},
		"ip option with IPv6": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 150, "type": "ip", "data": "fd00::1"}}
		}, "/dhcp/0/options/0/data"},
		"uint8 overflow": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 224, "type": "uint8", "data": "256"}}
		}, "/dhcp/0/options/0/data"},
		"uint16 overflow": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 224, "type": "uint16", "data": "65536"}}
		}, "/dhcp/0/options/0/data"},
		"uint32 negative": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 224, "type": "uint32", "data": "-1"}}
		}, "/dhcp/0/options/0/data"},
		"boolean invalid": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 224, "type": "boolean", "data": "yes"}}
		}, "/dhcp/0/options/0/data"},
		"hex odd length": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 224, "type": "hex", "data": "abc"}}
		}, "/dhcp/0/options/0/data"},
		"hex bad colons": {func(d map[string]any) {
			entry(d)["options"] = []any{map[string]any{"code": 224, "type": "hex", "data": "a:bb"}}
		}, "/dhcp/0/options/0/data"},
	} {
		t.Run(name, func(t *testing.T) {
			d := servicesFixture(t)
			tc.edit(d)
			_, err := decodeMap(t, d)
			var v validationError
			raw, _ := json.Marshal(err)
			if e, ok := err.(validationError); ok {
				v = e
			}
			for _, f := range v {
				if f.Path == tc.path {
					return
				}
			}
			t.Fatalf("want an error at %s, got %v %s", tc.path, err, raw)
		})
	}

	// Valid options canonicalize cleanly and sort by code.
	d := servicesFixture(t)
	e := entry(d)
	e["gateway"] = "192.0.2.254"
	e["domainName"] = "lan.example"
	e["domainSearch"] = []any{"lan.example", "corp.example"}
	e["ntpServers"] = []any{"192.0.2.123", "192.0.2.124"}
	e["nextServer"] = "192.0.2.10"
	e["bootFileName"] = "pxelinux.0"
	e["options"] = []any{
		map[string]any{"code": 252, "type": "string", "data": "http://wpad.lan/wpad.dat", "alwaysSend": true},
		map[string]any{"code": 150, "type": "ip", "data": "192.0.2.10,192.0.2.11"},
		map[string]any{"code": 224, "type": "hex", "data": "AA:BB:CC"},
		map[string]any{"code": 66, "type": "string", "data": "tftp.lan"},
		map[string]any{"code": 225, "type": "uint8", "data": "07"},
		map[string]any{"code": 226, "type": "uint16", "data": "1024"},
		map[string]any{"code": 227, "type": "uint32", "data": "86400"},
		map[string]any{"code": 228, "type": "boolean", "data": "true"},
	}
	got, err := decodeMap(t, d)
	if err != nil {
		t.Fatalf("valid options refused: %v", err)
	}
	opts := got.DHCP[0].Options
	if len(opts) != 8 || opts[0].Code != 66 || opts[1].Code != 150 || opts[1].Data != "192.0.2.10, 192.0.2.11" || opts[2].Data != "aabbcc" || opts[3].Data != "7" || opts[7].Code != 252 || !opts[7].AlwaysSend {
		t.Fatalf("unexpected canonical options: %+v", opts)
	}
}

func TestDHCP6OptionsValidation(t *testing.T) {
	base := func(t *testing.T) map[string]any {
		t.Helper()
		return dhcp6Fixture(t, nil)
	}
	entry6 := func(d map[string]any) map[string]any { return item(d["dhcp6"].([]any), 0) }

	for name, tc := range map[string]struct {
		edit func(d map[string]any)
		path string
	}{
		"bad domainSearch": {func(d map[string]any) { entry6(d)["domainSearch"] = []any{"bad..dom"} }, "/dhcp6/0/domainSearch/0"},
		"link-local ntp":   {func(d map[string]any) { entry6(d)["ntpServers"] = []any{"fe80::1"} }, "/dhcp6/0/ntpServers/0"},
		"v4 ntp":           {func(d map[string]any) { entry6(d)["ntpServers"] = []any{"192.0.2.1"} }, "/dhcp6/0/ntpServers/0"},
		"reserved code 23": {func(d map[string]any) {
			entry6(d)["options"] = []any{map[string]any{"code": 23, "type": "ip", "data": "fd00:1::1"}}
		}, "/dhcp6/0/options/0/code"},
		"reserved code 31": {func(d map[string]any) {
			entry6(d)["options"] = []any{map[string]any{"code": 31, "type": "ip", "data": "fd00:1::123"}}
		}, "/dhcp6/0/options/0/code"},
		"ip option with IPv4": {func(d map[string]any) {
			entry6(d)["options"] = []any{map[string]any{"code": 200, "type": "ip", "data": "192.0.2.1"}}
		}, "/dhcp6/0/options/0/data"},
	} {
		t.Run(name, func(t *testing.T) {
			d := base(t)
			tc.edit(d)
			_, err := decodeMap(t, d)
			var v validationError
			if e, ok := err.(validationError); ok {
				v = e
			}
			for _, f := range v {
				if f.Path == tc.path {
					return
				}
			}
			t.Fatalf("want error at %s, got %v", tc.path, err)
		})
	}

	// Valid DHCPv6 extra options pass and canonicalize.
	d := base(t)
	e := entry6(d)
	e["domainSearch"] = []any{"v6.example", "corp.example"}
	e["ntpServers"] = []any{"fd00:0001::0123"}
	e["options"] = []any{
		map[string]any{"code": 59, "type": "string", "data": "http://boot.example/efi", "alwaysSend": true},
		map[string]any{"code": 200, "type": "ip", "data": "fd00:0001::1, fd00:0001::2"},
	}
	got, err := decodeMap(t, d)
	if err != nil {
		t.Fatalf("valid dhcp6 options refused: %v", err)
	}
	if got.DHCP6[0].NTPServers[0] != "fd00:1::123" || got.DHCP6[0].Options[0].Code != 59 || got.DHCP6[0].Options[1].Data != "fd00:1::1, fd00:1::2" {
		t.Fatalf("unexpected canonical dhcp6: %+v", got.DHCP6[0])
	}
}

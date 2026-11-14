package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// A forwarding DNS service whose upstream is the firewall itself would ask itself forever.
func TestDNSUpstreamIsTheFirewall(t *testing.T) {
	for name, upstream := range map[string]string{
		"a segment address":    "192.0.2.7",
		"with a port":          "192.0.2.7:53",
		"a virtual address":    "172.16.53.53",
		"a v6 virtual address": "fd00:53:53:53:53:53:53:53",
		"a served v6 address":  "fd00:1::1",
	} {
		t.Run(name, func(t *testing.T) {
			d := vipFixture(t)
			d["dns"] = map[string]any{"mode": "forward", "segments": []any{item(segmentsOf(d), 1)["id"]}, "virtualAddresses": []any{vipA, vipB}, "upstreams": []any{map[string]any{"address": upstream, "protocol": "do53"}}}
			refusedAt(t, d, "/dns/upstreams/0/address")
		})
	}
	// Unbound always listens on loopback; only addresses it listens on, port 53, would loop.
	for name, upstream := range map[string]string{
		"loopback":        "127.0.0.1",
		"loopback v6":     "::1",
		"mapped":          "::ffff:192.0.2.7",
		"unspecified":     "0.0.0.0",
		"mapped loopback": "::ffff:127.0.0.1",
		"resolved stub":   "127.0.0.53",
		"resolved proxy":  "127.0.0.54:53",
	} {
		t.Run(name, func(t *testing.T) {
			d := vipFixture(t)
			d["dns"] = map[string]any{"mode": "forward", "segments": []any{item(segmentsOf(d), 1)["id"]}, "virtualAddresses": []any{vipA, vipB}, "upstreams": []any{map[string]any{"address": upstream, "protocol": "do53"}}}
			refusedAt(t, d, "/dns/upstreams/0/address")
		})
	}
	for name, upstream := range map[string]string{
		"another host":             "192.0.2.53",
		"another port":             "192.0.2.7:5353",
		"an unserved segment":      "192.0.2.7",
		"an unlistened virtual v6": "fd00:53:53:53:53:53:53:53",
		"another loopback":         "127.0.2.1:53",
		"another loopback bare":    "127.0.0.2",
	} {
		t.Run(name, func(t *testing.T) {
			d := vipFixture(t)
			d["dns"] = map[string]any{"mode": "forward", "segments": []any{}, "upstreams": []any{map[string]any{"address": upstream, "protocol": "do53"}}}
			if name == "another host" || name == "another port" {
				d["dns"].(map[string]any)["segments"] = []any{item(segmentsOf(d), 1)["id"]}
			}
			accepted(t, d)
		})
	}
}

// A virtual address is the firewall's own: 1:1 NAT may not take it over, in either direction.
func TestOneToOneVirtualAddress(t *testing.T) {
	for field, path := range map[string]string{"internal": "/nat/oneToOne/0/internal", "external": "/nat/oneToOne/0/external"} {
		t.Run(field, func(t *testing.T) {
			d := vipFixture(t)
			o := map[string]any{"id": otoA, "name": "map", "enabled": true, "zone": wanZone(d), "external": "198.51.100.50", "internal": "192.0.2.50", "destination": []any{}}
			o[field] = "172.16.53.53"
			setNAT(d, "oneToOne", o)
			refusedAt(t, d, path)
		})
	}
}

// An IPv6 forward to the address of a segment numbered by DHCPv6 or delegation names what is wrong.
func TestIPv6ForwardToDynamicSegment(t *testing.T) {
	d := vipFixture(t)
	v6(d, 1)["mode"], v6(d, 1)["addresses"] = "delegated", []any{"fd00:a5d:1::1/64"}
	v6(d, 1)["from"], v6(d, 1)["subnetId"] = item(segmentsOf(d), 0)["id"], "1"
	setNAT(d, "portForwards", forward(d, map[string]any{"family": "ipv6", "target": map[string]any{"address": "fd00:a5d:1::2"}, "targetPort": nil, "ports": "53"}))
	_, e := decodeMap(t, d)
	if raw, _ := json.Marshal(e); e == nil || !strings.Contains(string(raw), "use target self") {
		t.Fatalf("%s", raw)
	}
}

// A virtual address 1:1 NAT would take over is named as one.
func TestOneToOneVirtualMessage(t *testing.T) {
	d := vipFixture(t)
	setNAT(d, "oneToOne", map[string]any{"id": otoA, "name": "map", "enabled": true, "zone": wanZone(d), "external": "172.16.53.53", "internal": "192.0.2.50", "destination": []any{}})
	_, e := decodeMap(t, d)
	if raw, _ := json.Marshal(e); !strings.Contains(string(raw), "virtual address") {
		t.Fatalf("%s", raw)
	}
}

// A DNS-over-TLS upstream without a port is asked on 853, where the DNS service does not listen.
func TestDoTUpstreamOnAListenedAddress(t *testing.T) {
	d := vipFixture(t)
	d["dns"] = map[string]any{"mode": "forward", "segments": []any{}, "virtualAddresses": []any{vipA},
		"upstreams": []any{map[string]any{"address": "172.16.53.53", "protocol": "dot", "tlsName": "dns.example"}}}
	accepted(t, d)
	d["dns"].(map[string]any)["upstreams"] = []any{map[string]any{"address": "172.16.53.53:53", "protocol": "dot", "tlsName": "dns.example"}}
	refusedAt(t, d, "/dns/upstreams/0/address")
}

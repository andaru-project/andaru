package config

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func setDNS(d map[string]any, segment int, servers []any, domains ...any) {
	dns := map[string]any{"servers": servers}
	if domains != nil {
		dns["searchDomains"] = domains
	}
	item(segmentsOf(d), segment)["addressing"].(map[string]any)["dns"] = dns
}

func TestDNSValidation(t *testing.T) {
	const base = "/segments/1/addressing/dns"
	server := func(values ...any) func(map[string]any) {
		return func(d map[string]any) { setDNS(d, 1, append([]any{}, values...)) }
	}
	domain := func(values ...any) func(map[string]any) {
		return func(d map[string]any) { setDNS(d, 1, []any{"192.0.2.53"}, values...) }
	}
	for _, tc := range []struct {
		name          string
		change        func(map[string]any)
		path, message string
	}{
		{"no addresses on the segment", func(d map[string]any) {
			a := item(segmentsOf(d), 1)["addressing"].(map[string]any)
			a["ipv4"], a["ipv6"] = map[string]any{"mode": "none"}, map[string]any{"mode": "none"}
			setDNS(d, 1, []any{"192.0.2.53"})
		}, base, "A segment without addresses cannot reach DNS servers."},
		{"second segment", func(d map[string]any) {
			setDNS(d, 0, []any{"192.0.2.53"})
			setDNS(d, 1, []any{"192.0.2.54"})
		}, base, "Only one segment may declare DNS servers."},
		{"empty", server(""), base + "/servers/0", "Invalid DNS server address."},
		{"a name", server("dns.example"), base + "/servers/0", "Invalid DNS server address."},
		{"zoned IPv4", server("192.0.2.53%eth0"), base + "/servers/0", "Invalid DNS server address."},
		{"zoned link-local", server("fe80::1%eth0"), base + "/servers/0", "Invalid DNS server address."},
		{"IPv4-mapped", server("::ffff:192.0.2.53"), base + "/servers/0", "Invalid DNS server address."},
		{"unspecified", server("0.0.0.0"), base + "/servers/0", "Invalid DNS server address."},
		{"loopback", server("127.0.0.1"), base + "/servers/0", "Invalid DNS server address."},
		{"multicast", server("224.0.0.1"), base + "/servers/0", "Invalid DNS server address."},
		{"link-local", server("192.0.2.53", "fe80::1"), base + "/servers/1", "Invalid DNS server address."},
		{"same server written two ways", server("2001:db8::53", "2001:0DB8::53"), base + "/servers/1", "DNS server is listed twice."},
		{"no servers", server(), base + "/servers", "Value does not satisfy the declared shape or constraint."},
		{"nine servers", server("192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6", "192.0.2.7", "192.0.2.8", "192.0.2.9"), base + "/servers", "Value does not satisfy the declared shape or constraint."},
		{"upper case", domain("Example.org"), base + "/searchDomains/0", "Invalid search domain."},
		{"trailing dot", domain("example.org."), base + "/searchDomains/0", "Invalid search domain."},
		{"routing-only", domain("~example.org"), base + "/searchDomains/0", "Invalid search domain."},
		{"space", domain("exa mple.org"), base + "/searchDomains/0", "Invalid search domain."},
		// A newline would start a new line, and so a new key or section, in the rendered file.
		{"newline", domain("example.org\n[Match]"), base + "/searchDomains/0", "Invalid search domain."},
		{"leading hyphen", domain("-a.org"), base + "/searchDomains/0", "Invalid search domain."},
		{"long label", domain(strings.Repeat("a", 64) + ".org"), base + "/searchDomains/0", "Invalid search domain."},
		{"long name", domain(strings.Repeat(strings.Repeat("a", 49)+".", 5) + "aaaa"), base + "/searchDomains/0", "Value does not satisfy the declared shape or constraint."},
		{"duplicate domain", domain("a.example", "a.example"), base + "/searchDomains/1", "Search domain is listed twice."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fixtureMap(t)
			tc.change(d)
			_, e := decodeMap(t, d)
			ve, _ := e.(validationError)
			if !slices.Contains(ve, FieldError{Path: tc.path, Message: tc.message}) {
				t.Fatalf("error %v; fields %+v; want %s %q", e, ve, tc.path, tc.message)
			}
		})
	}
	d := fixtureMap(t)
	setDNS(d, 1, []any{"192.0.2.53", "2001:db8::53"}, "example.org", "xn--bcher-kva.example", "lan", strings.Repeat("a", 63)+".org")
	if _, e := decodeMap(t, d); e != nil {
		t.Fatalf("valid DNS refused: %v", e)
	}
}

func TestDNSRoundTrip(t *testing.T) {
	d := fixtureMap(t)
	plain, e := decodeMap(t, d)
	if e != nil {
		t.Fatal(e)
	}
	if without, _ := canonical(plain); bytes.Contains(without, []byte("dns")) {
		t.Fatalf("a declaration without DNS gained the key: %s", without)
	}
	setDNS(d, 1, []any{"2001:0DB8::53", "192.0.2.53"}, "b.example", "a.example")
	got, e := decodeMap(t, d)
	if e != nil {
		t.Fatal(e)
	}
	out, e := canonical(got)
	// Order is the order servers are tried in; sorting it would change which one answers.
	if e != nil || !bytes.Contains(out, []byte(`"dns":{"searchDomains":["b.example","a.example"],"servers":["2001:db8::53","192.0.2.53"]}`)) {
		t.Fatalf("canonical = %s, %v", out, e)
	}
}

// A batch may edit only the earlier of two declaring segments; the error must land on an object the batch touched.
func TestSecondDNSDeclarerBlamesBoth(t *testing.T) {
	d := fixtureMap(t)
	setDNS(d, 0, []any{"192.0.2.53"})
	setDNS(d, 1, []any{"192.0.2.54"})
	_, e := decodeMap(t, d)
	ve, _ := e.(validationError)
	for _, path := range []string{"/segments/0/addressing/dns", "/segments/1/addressing/dns"} {
		if !slices.Contains(ve, FieldError{Path: path, Message: "Only one segment may declare DNS servers."}) {
			t.Fatalf("no error at %s: %+v", path, ve)
		}
	}
}

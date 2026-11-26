package engine

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestParseAliasSearchQuery(t *testing.T) {
	q, msg := parseAliasSearchQuery(url.Values{
		"type":          {"addresses"},
		"q":             {"web"},
		"kind":          {"host"},
		"family":        {"ipv4"},
		"excludeGroups": {"true"},
		"exclude":       {"adr_self"},
		"limit":         {"15"},
	})
	if msg != "" {
		t.Fatalf("unexpected error: %s", msg)
	}
	if q.Type != "addresses" || q.Query != "web" || q.Kind != "host" || q.Family != "ipv4" || !q.ExcludeGroups || q.Exclude != "adr_self" || q.Limit != 15 {
		t.Fatalf("unexpected parsed query: %+v", q)
	}
	if _, errMsg := parseAliasSearchQuery(url.Values{"type": {"addresses"}, "q": {strings.Repeat("é", 256)}}); errMsg != "" {
		t.Fatalf("256 multibyte runes should fit maxLength 256, got: %s", errMsg)
	}

	cases := []struct {
		name   string
		values url.Values
	}{
		{"missing type", url.Values{"q": {"web"}}},
		{"bad type", url.Values{"type": {"zones"}}},
		{"unknown param", url.Values{"type": {"addresses"}, "foo": {"bar"}}},
		{"duplicate param", url.Values{"type": {"addresses"}, "q": {"a", "b"}}},
		{"mismatched kind for addresses", url.Values{"type": {"addresses"}, "kind": {"tcp"}}},
		{"mismatched kind for ports", url.Values{"type": {"ports"}, "kind": {"host"}}},
		{"family on ports", url.Values{"type": {"ports"}, "family": {"ipv4"}}},
		{"bad family", url.Values{"type": {"addresses"}, "family": {"ipv5"}}},
		{"bad excludeGroups", url.Values{"type": {"addresses"}, "excludeGroups": {"yes"}}},
		{"numeric excludeGroups", url.Values{"type": {"addresses"}, "excludeGroups": {"1"}}},
		{"exclude too long", url.Values{"type": {"addresses"}, "exclude": {strings.Repeat("x", 129)}}},
		{"limit zero", url.Values{"type": {"addresses"}, "limit": {"0"}}},
		{"limit too large", url.Values{"type": {"addresses"}, "limit": {"51"}}},
		{"query too long", url.Values{"type": {"addresses"}, "q": {strings.Repeat("a", 257)}}},
	}
	for _, tc := range cases {
		if _, errMsg := parseAliasSearchQuery(tc.values); errMsg == "" {
			t.Errorf("%s: expected validation error", tc.name)
		}
	}
}

func TestAliasSearchRouteCapability(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		route, ok := routeForRequest(method, "/api/v1/config/aliases/search")
		if !ok || route.Capability != authz.ConfigurationRead {
			t.Fatalf("%s /api/v1/config/aliases/search = (%+v, %t), want %q", method, route, ok, authz.ConfigurationRead)
		}
	}
}

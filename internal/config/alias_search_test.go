package config

import (
	"strings"
	"testing"
)

func joinSegments(segs []TextSegment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

func boldText(segs []TextSegment) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Match {
			b.WriteString("[")
			b.WriteString(s.Text)
			b.WriteString("]")
		} else {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

func sampleAliasDeclaration() *declaration {
	return &declaration{
		Aliases: aliasSet{
			Addresses: []address{
				{identity: identity{ID: "adr_web", Name: "web", Description: "Single web node"}, Kind: "host", Address: "192.168.10.20"},
				{identity: identity{ID: "adr_websrv", Name: "web-servers", Description: "Production frontend"}, Kind: "network", Prefix: "192.168.10.0/24"},
				{identity: identity{ID: "adr_dmzweb", Name: "dmz-web-tier"}, Kind: "range", First: "10.0.2.10", Last: "10.0.2.20"},
				{identity: identity{ID: "adr_myweb", Name: "mywebhost"}, Kind: "host", Address: "192.168.10.25"},
				{identity: identity{ID: "adr_printers", Name: "Printers", Description: "Office floor"}, Kind: "network", Prefix: "192.168.50.0/24"},
				{identity: identity{ID: "adr_v6", Name: "ipv6-servers"}, Kind: "network", Prefix: "2001:db8:1::/64"},
				{identity: identity{ID: "adr_grp4", Name: "all-v4-web"}, Kind: "group", Members: []string{"adr_web", "adr_websrv"}},
				{identity: identity{ID: "adr_grp6", Name: "all-v6-hosts"}, Kind: "group", Members: []string{"adr_v6"}},
			},
			Ports: []portAlias{
				{identity: identity{ID: "svc_https", Name: "HTTPS", Description: "TLS web traffic"}, Protocol: "tcp", Ports: []string{"443"}},
				{identity: identity{ID: "svc_http", Name: "HTTP"}, Protocol: "tcp", Ports: []string{"80"}},
				{identity: identity{ID: "svc_dns", Name: "DNS"}, Protocol: "udp", Ports: []string{"53"}},
				{identity: identity{ID: "svc_ping", Name: "Ping"}, Protocol: "icmp", Types: []string{"echo-request"}},
				{identity: identity{ID: "svc_webgrp", Name: "WebPorts"}, Protocol: "group", Members: []string{"svc_http", "svc_https"}},
			},
		},
	}
}

func TestAliasSearchRankingAndHighlights(t *testing.T) {
	d := sampleAliasDeclaration()

	got := searchAliasesInDeclaration(d, "7", AliasSearchQuery{Type: "addresses", Query: "web"})
	if got.State != "saved" || got.SavedVersion != "7" {
		t.Fatalf("unexpected header: %+v", got)
	}
	wantOrder := []string{"adr_web", "adr_websrv", "adr_dmzweb", "adr_grp4", "adr_myweb"}
	if len(got.Items) != len(wantOrder) {
		t.Fatalf("got %d items (%+v), want %d", len(got.Items), got.Items, len(wantOrder))
	}
	for i, wantID := range wantOrder {
		if got.Items[i].ID != wantID {
			t.Fatalf("rank %d = %s (score=%d), want %s; full=%+v", i, got.Items[i].ID, got.Items[i].Score, wantID, got.Items)
		}
		if joinSegments(got.Items[i].Highlights) != got.Items[i].Name {
			t.Fatalf("highlights do not reconstruct name: %+v", got.Items[i])
		}
		if i > 0 && got.Items[i].Score > got.Items[i-1].Score {
			t.Fatalf("scores not non-increasing: %d > %d", got.Items[i].Score, got.Items[i-1].Score)
		}
	}
	if b := boldText(got.Items[0].Highlights); b != "[web]" {
		t.Fatalf("exact highlight = %q, want [web]", b)
	}
	if b := boldText(got.Items[1].Highlights); b != "[web]-servers" {
		t.Fatalf("prefix highlight = %q, want [web]-servers", b)
	}
	if b := boldText(got.Items[2].Highlights); b != "dmz-[web]-tier" {
		t.Fatalf("boundary highlight = %q, want dmz-[web]-tier", b)
	}
}

func TestAliasSearchSubsequenceTypoAndSummary(t *testing.T) {
	d := sampleAliasDeclaration()

	// Fuzzy subsequence with word-boundary bonuses ("ws" -> "web-servers").
	sub := searchAliasesInDeclaration(d, "1", AliasSearchQuery{Type: "addresses", Query: "ws"})
	if len(sub.Items) == 0 || sub.Items[0].ID != "adr_websrv" {
		t.Fatalf("subsequence top match = %+v, want adr_websrv", sub.Items)
	}
	if b := boldText(sub.Items[0].Highlights); b != "[w]eb-[s]ervers" {
		t.Fatalf("subsequence highlight = %q, want [w]eb-[s]ervers", b)
	}

	// Typo-tolerant match ("pritners" transposition -> "Printers").
	typo := searchAliasesInDeclaration(d, "1", AliasSearchQuery{Type: "addresses", Query: "pritners"})
	if len(typo.Items) == 0 || typo.Items[0].ID != "adr_printers" {
		t.Fatalf("typo top match = %+v, want adr_printers", typo.Items)
	}
	if joinSegments(typo.Items[0].Highlights) != "Printers" {
		t.Fatalf("typo highlights lost original casing: %+v", typo.Items[0].Highlights)
	}

	// Port search by port number matches summary and bolds it.
	byPort := searchAliasesInDeclaration(d, "1", AliasSearchQuery{Type: "ports", Query: "443"})
	if len(byPort.Items) != 1 || byPort.Items[0].ID != "svc_https" {
		t.Fatalf("port summary search = %+v, want svc_https", byPort.Items)
	}
	if b := boldText(byPort.Items[0].SummaryHighlights); b != "TCP [443]" {
		t.Fatalf("summary highlight = %q, want 'TCP [443]'", b)
	}

	// Description match when name and summary don't match.
	byDesc := searchAliasesInDeclaration(d, "1", AliasSearchQuery{Type: "addresses", Query: "office"})
	if len(byDesc.Items) != 1 || byDesc.Items[0].ID != "adr_printers" {
		t.Fatalf("description search = %+v, want adr_printers", byDesc.Items)
	}
}

func TestAliasSearchFiltersAndEmptyQuery(t *testing.T) {
	d := sampleAliasDeclaration()

	// Empty query sorts alphabetically (case-insensitive) and respects limit.
	empty := searchAliasesInDeclaration(d, "2", AliasSearchQuery{Type: "ports", Limit: 3})
	wantPorts := []string{"svc_dns", "svc_http", "svc_https"}
	if len(empty.Items) != len(wantPorts) {
		t.Fatalf("empty query len = %d, want %d", len(empty.Items), len(wantPorts))
	}
	for i, id := range wantPorts {
		if empty.Items[i].ID != id {
			t.Fatalf("empty[%d] = %s, want %s", i, empty.Items[i].ID, id)
		}
	}

	// Family filter ipv6 returns ipv6 network + ipv6 group only.
	v6 := searchAliasesInDeclaration(d, "2", AliasSearchQuery{Type: "addresses", Family: "ipv6"})
	if len(v6.Items) != 2 || v6.Items[0].ID != "adr_grp6" || v6.Items[1].ID != "adr_v6" {
		t.Fatalf("ipv6 filter = %+v, want adr_grp6 and adr_v6", v6.Items)
	}

	// ExcludeGroups + Exclude self.
	members := searchAliasesInDeclaration(d, "2", AliasSearchQuery{
		Type:          "ports",
		ExcludeGroups: true,
		Exclude:       "svc_http",
	})
	for _, it := range members.Items {
		if it.Protocol == "group" || it.ID == "svc_http" {
			t.Fatalf("unexpected excluded item: %+v", it)
		}
	}

	// Kind filter on addresses.
	ranges := searchAliasesInDeclaration(d, "2", AliasSearchQuery{Type: "addresses", Kind: "range"})
	if len(ranges.Items) != 1 || ranges.Items[0].ID != "adr_dmzweb" {
		t.Fatalf("kind=range = %+v, want adr_dmzweb", ranges.Items)
	}
}

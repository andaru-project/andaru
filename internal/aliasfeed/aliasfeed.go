// Package aliasfeed is the message format between the engine, which asks for the members of an address alias fed
// from a URL or from host names on Topic(alias), and andaru-outbound, which fetches or resolves them and answers on
// ResultTopic(alias); and the state file the engine keeps of each such alias, which andaru realize loads into the
// alias's sets.
package aliasfeed

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// Filter is what andaru-outbound's consumer, Consumer, reads.
	Filter   = "alias-fetch/#"
	Consumer = "alias-fetches"
	// ResultFilter is what the engine's consumer, ResultConsumer, reads.
	ResultFilter   = "alias-result/#"
	ResultConsumer = "alias-results"
	// RequestType and ResultType are the types of a request's and a result's event.
	RequestType = "alias.fetch.v1"
	ResultType  = "alias.result.v1"
	// MaxMembers is the most entries an alias may hold.
	MaxMembers = 10000
)

// Topic is where the requests for an alias are published; ResultTopic is where their results are.
func Topic(alias string) string       { return "alias-fetch/" + alias }
func ResultTopic(alias string) string { return "alias-result/" + alias }

// Request asks for one fetch of a URL alias or one resolution of a host-name alias's names.
type Request struct {
	// ID is the request's own, which its result carries.
	ID    string   `json:"id"`
	Alias string   `json:"alias"`
	Kind  string   `json:"kind"` // "url" or "hostnames"
	URL   string   `json:"url,omitempty"`
	Names []string `json:"names,omitempty"`
}

// Result is the outcome of a request. OK with no members is an empty list; not OK keeps the last good list.
type Result struct {
	Request string   `json:"request"`
	OK      bool     `json:"ok"`
	Members []string `json:"members,omitempty"` // addresses and networks, for a URL alias
	Error   string   `json:"error,omitempty"`
	// Names is each name's outcome, for a host-name alias.
	Names     map[string]NameResult `json:"names,omitempty"`
	FetchedAt time.Time             `json:"fetchedAt"`
}

// NameResult is what one host name resolved to, or why it did not.
type NameResult struct {
	Addresses []string `json:"addresses,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// State is what the engine keeps of an alias, in Path(stateDir, alias).
type State struct {
	// Hash is of the alias's kind, URL and names when last asked for: a change asks again at once.
	Hash string `json:"hash"`
	// Members is the last good list: for a host-name alias, every name's last good addresses.
	Members     []string  `json:"members"`
	LastSuccess time.Time `json:"lastSuccess,omitzero"`
	LastAttempt time.Time `json:"lastAttempt,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	Failures    int       `json:"failures"`
	Names       []Name    `json:"names,omitempty"`
	// Pending are the requests sent since the hash last changed and not yet answered, oldest first.
	Pending []string `json:"pending,omitempty"`
}

// Name is a host-name alias's name, with its last good addresses and its last outcome.
type Name struct {
	Name       string    `json:"name"`
	Addresses  []string  `json:"addresses"`
	Error      string    `json:"error,omitempty"`
	ResolvedAt time.Time `json:"resolvedAt,omitzero"`
}

// Dir is where the state files are; Path is one alias's.
func Dir(stateDir string) string          { return filepath.Join(stateDir, "aliases") }
func Path(stateDir, alias string) string { return filepath.Join(Dir(stateDir), alias+".json") }

// Members checks a list of addresses and networks and returns it in the form the sets take: each an IPv4 or IPv6
// address or network, a network without host bits, an IPv4-mapped address as IPv4, sorted, each once, at most
// MaxMembers.
func Members(in []string) ([]string, error) {
	if len(in) > MaxMembers {
		return nil, fmt.Errorf("%d entries, more than %d", len(in), MaxMembers)
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
			out = append(out, a.Unmap().String())
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Addr().Is4In6() {
			return nil, fmt.Errorf("%q is not an address or a network", s)
		}
		out = append(out, p.Masked().String())
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Split returns the members of each family. Members always writes an IPv6 entry with a colon, an IPv4 one without.
func Split(members []string) (v4, v6 []string) {
	for _, m := range members {
		if strings.Contains(m, ":") {
			v6 = append(v6, m)
		} else {
			v4 = append(v4, m)
		}
	}
	return v4, v6
}

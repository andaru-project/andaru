package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/aliasfeed"
	"github.com/andaru-project/andaru/internal/bus"
)

const (
	// maxFetches is how many alias requests are fetched or resolved at once.
	maxFetches   = 8
	maxRedirects = 5
	maxList      = 1 << 20
	lookupFor    = 10 * time.Second
)

// aliasClient is the client alias lists are fetched with, over rt (nil is the default transport): certificates
// verified, at most five redirects, and none downwards.
func aliasClient(rt http.RoundTripper) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			from, to := via[len(via)-1].URL.Scheme, req.URL.Scheme
			switch {
			case len(via) > maxRedirects:
				return fmt.Errorf("more than %d redirects", maxRedirects)
			case to != "http" && to != "https":
				return fmt.Errorf("redirect to a %q address refused", to)
			case from == "https" && to != "https":
				return errors.New("redirect from https to http refused")
			}
			return nil
		}}
}

// goResolver is Go's own resolver, never the C library's. It takes a name that is an address as that address; the
// engine's validation keeps addresses out of the names.
var goResolver = &net.Resolver{PreferGo: true}

// lookupGo resolves a name's A and AAAA records with goResolver.
func lookupGo(ctx context.Context, name string) ([]netip.Addr, error) {
	return goResolver.LookupNetIP(ctx, "ip", name)
}

// fetchList fetches and parses the list at rawURL. Its error holds no URL: a query string may hold a token.
func fetchList(ctx context.Context, client *http.Client, rawURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("not a valid URL")
	}
	req.Header.Set("User-Agent", User)
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "failed to parse Location header") {
			return nil, errors.New("a redirect's Location does not parse") // net/http's error quotes it
		}
		return nil, redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxList+1))
	if err != nil {
		return nil, redact(err)
	}
	if len(body) > maxList {
		return nil, errors.New("list larger than 1 MiB")
	}
	return parseList(body)
}

// parseList reads one address or network per line, after a leading UTF-8 BOM. Blank lines, lines starting with #
// or ;, and a comment after whitespace and # are ignored; any other line that does not parse fails the whole list.
// The limit counts each entry once, in the form the sets take.
func parseList(body []byte) ([]string, error) {
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	// An HTML page (a login or error page served with 200) starts with a tag; no list line does.
	if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '<' {
		return nil, errors.New("not a list")
	}
	seen := map[string]struct{}{}
	n := 0
	for line := range strings.Lines(string(body)) {
		n++
		if i := strings.IndexByte(line, '#'); i > 0 && (line[i-1] == ' ' || line[i-1] == '\t') {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		m, err := aliasfeed.Members([]string{line})
		if err != nil {
			return nil, fmt.Errorf("line %d: %q is not an address or a network", n, cut(line, 64))
		}
		seen[m[0]] = struct{}{}
	}
	return aliasfeed.Members(slices.Collect(maps.Keys(seen)))
}

// resolve looks up each name at once, each within lookupFor. A name with no addresses failed.
func resolve(ctx context.Context, lookup func(context.Context, string) ([]netip.Addr, error), names []string) map[string]aliasfeed.NameResult {
	out := make(map[string]aliasfeed.NameResult, len(names))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, lookupFor)
			defer cancel()
			addrs, err := lookup(ctx, name)
			var r aliasfeed.NameResult
			var dns *net.DNSError
			switch {
			case errors.As(err, &dns):
				r.Error = dns.Err // "no such host", without the server's address
			case err != nil:
				r.Error = err.Error()
			}
			for _, a := range addrs {
				if r.Error == "" && a.Zone() == "" { // a zoned address (fe80::1%eth0 in /etc/hosts) fits no set
					r.Addresses = append(r.Addresses, a.Unmap().String())
				}
			}
			if r.Error == "" && len(r.Addresses) == 0 {
				r.Error = "no addresses"
			}
			mu.Lock()
			out[name] = r
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// feeds collapses the requests for one source that arrive while it waits for a fetch slot: the fetch answers the
// newest only, which the engine takes as the answer to every older one, so one fetch counts once. A request that
// arrives once the fetch has started gets a fetch of its own: it is answered with data fetched after it was asked.
type feeds struct {
	mu       sync.Mutex
	inFlight map[string]*feed // by alias and hash
}

type feed struct {
	newest    string // the request ID the result carries: the one published last
	newestSeq int64
	seqs      []int64 // the bus events it answers
}

// handleAlias answers one request and returns the events it settles: none when another fetch of the same source
// answers it or the session ended first, or several. An error is a result not published, not even as a failure: it ends the session.
func (s *Service) handleAlias(ctx context.Context, c *bus.Client, slots chan struct{}, m bus.Message) ([]int64, error) {
	alias := strings.TrimPrefix(m.Topic, "alias-fetch/")
	var req aliasfeed.Request
	e := bus.Event{Data: &req}
	err := json.Unmarshal(m.Payload, &e)
	switch {
	case err != nil:
	case alias == "" || strings.ContainsAny(alias, "/+#"): // its results' topic would be refused, every time
		err = fmt.Errorf("alias %q", alias)
	case e.Type != aliasfeed.RequestType || req.ID == "" || req.Hash == "":
		err = fmt.Errorf("type %q, request %q, hash %q", e.Type, req.ID, req.Hash)
	case req.Kind == "url" && req.URL == "", req.Kind == "hostnames" && len(req.Names) == 0:
		err = errors.New("nothing to fetch")
	case req.Kind != "url" && req.Kind != "hostnames":
		err = fmt.Errorf("kind %q", req.Kind)
	}
	if err != nil {
		s.Log.Error("alias request unreadable; skipped", "topic", m.Topic, "seq", m.Seq, "error", err)
		return []int64{m.Seq}, nil
	}
	key := alias + "\x00" + req.Hash
	s.feeds.mu.Lock()
	if f := s.feeds.inFlight[key]; f != nil {
		if m.Seq > f.newestSeq { // the requests are handled concurrently, so not in the bus's order
			f.newest, f.newestSeq = req.ID, m.Seq
		}
		f.seqs = append(f.seqs, m.Seq)
		s.feeds.mu.Unlock()
		return nil, nil
	}
	f := &feed{newest: req.ID, newestSeq: m.Seq, seqs: []int64{m.Seq}}
	s.feeds.inFlight[key] = f
	s.feeds.mu.Unlock()
	// Out of the map once the fetch starts: a request that comes after is fetched anew, not answered by this fetch.
	leave := func() {
		s.feeds.mu.Lock()
		defer s.feeds.mu.Unlock()
		if s.feeds.inFlight[key] == f {
			delete(s.feeds.inFlight, key)
		}
	}
	defer leave()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return nil, nil
	}
	leave()
	r, entries := s.fetch(ctx, alias, req)
	r.Request = f.newest // f no longer changes
	payload, err := bus.NewEvent(aliasfeed.ResultType, User, r)
	if err == nil {
		err = c.Publish(ctx, aliasfeed.ResultTopic(alias), payload, true)
	}
	if err != nil && ctx.Err() == nil {
		// The bus refused the result, as one too large: it is told the fetch failed instead, so the engine records the
		// failure and warns. A failure it refuses too means the bus is in trouble or closing.
		s.Log.Error("alias result refused by the bus; a failure is sent instead", "alias", alias, "error", err)
		failed := aliasfeed.Result{Request: r.Request, Hash: r.Hash, Error: "the result could not be sent: " + cut(err.Error(), 200)}
		if payload, err = bus.NewEvent(aliasfeed.ResultType, User, failed); err == nil {
			err = c.Publish(ctx, aliasfeed.ResultTopic(alias), payload, true)
		}
		if err == nil {
			return f.seqs, nil
		}
	}
	if err != nil {
		// The session ended, or the bus refuses even a failure. Unacked, the requests come again, but only to a new
		// session: this one's acks would wait for them.
		return nil, fmt.Errorf("publish the result for alias %s: %w", alias, err)
	}
	switch {
	case r.OK:
		s.Log.Info("alias list fetched", "alias", alias, "entries", entries)
	case req.Kind == "url":
		s.Log.Warn("alias list not fetched", "alias", alias, "reason", r.Error)
	default: // each name's reason is logged already
		s.Log.Warn("alias names not resolved", "alias", alias)
	}
	return f.seqs, nil
}

// fetch fetches a URL alias's list or resolves a host-name alias's names, and counts the entries it got.
func (s *Service) fetch(ctx context.Context, alias string, req aliasfeed.Request) (aliasfeed.Result, int) {
	r := aliasfeed.Result{Hash: req.Hash, FetchedAt: time.Now().UTC()}
	if req.Kind == "url" {
		var err error
		if r.Members, err = fetchList(ctx, s.AliasHTTP, req.URL); err != nil {
			r.Error = err.Error()
		}
		r.OK = err == nil
		return r, len(r.Members)
	}
	// The engine decides from each name's outcome; OK and Error say the same for the log and for a reader.
	r.Names = resolve(ctx, s.Lookup, req.Names)
	var failed, all []string
	for _, name := range req.Names {
		if n := r.Names[name]; n.Error != "" {
			failed = append(failed, name+": "+n.Error)
			s.Log.Warn("alias name not resolved", "alias", alias, "name", name, "reason", n.Error)
		} else {
			all = append(all, n.Addresses...)
		}
	}
	r.OK = len(failed) < len(req.Names)
	if !r.OK {
		r.Error = strings.Join(failed, "; ")
	}
	members, _ := aliasfeed.Members(all)
	return r, len(members)
}

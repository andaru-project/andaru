package acme

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
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"
)

// The providers' API base URLs.
var defaultEndpoints = map[string]string{
	"cloudflare":   "https://api.cloudflare.com/client/v4",
	"hetzner":      "https://dns.hetzner.com/api/v1",
	"digitalocean": "https://api.digitalocean.com/v2",
	"porkbun":      "https://api.porkbun.com/api/json/v3",
}

const (
	defaultPropagation = 120 * time.Second
	txtTTL             = 120
	maxProviderBody    = 1 << 20
)

// present publishes the TXT record value for domain's challenge and waits until the resolver sees it. The cleanup it
// returns, even with an error, removes the record.
func (is *Issuer) present(ctx context.Context, p DNS01, domain, value string) (func(), error) {
	fqdn := "_acme-challenge." + domain
	client := is.providerClient(p)
	var cleanup func(context.Context) error
	var err error
	switch p.Provider {
	case "cloudflare":
		cleanup, err = is.cloudflare(ctx, client, p, fqdn, value)
	case "hetzner":
		cleanup, err = is.hetzner(ctx, client, p, fqdn, value)
	case "digitalocean":
		cleanup, err = is.digitalocean(ctx, client, p, fqdn, value)
	case "porkbun":
		cleanup, err = is.porkbun(ctx, client, p, fqdn, value)
	case "acme-dns":
		err = acmeDNS(ctx, client, p, value) // acme-dns keeps the last two values: nothing to remove
	case "webhook":
		err = webhook(ctx, client, p, "present", domain, fqdn, value)
		cleanup = func(ctx context.Context) error { return webhook(ctx, client, p, "cleanup", domain, fqdn, value) }
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", p.Provider)
	}
	var done func()
	if cleanup != nil {
		done = func() {
			// The order's context may be over; the record is removed regardless.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = cleanup(ctx)
		}
	}
	if err != nil {
		return done, err
	}
	if p.Provider == "acme-dns" && p.FullDomain != "" {
		fqdn = p.FullDomain // the record _acme-challenge's CNAME names
	}
	return done, is.propagated(ctx, p, fqdn, value)
}

// propagated waits until fqdn's TXT records hold value, for at most the provider's propagation timeout.
func (is *Issuer) propagated(ctx context.Context, p DNS01, fqdn, value string) error {
	limit := defaultPropagation
	if p.PropagationTimeoutSeconds > 0 {
		limit = time.Duration(p.PropagationTimeoutSeconds) * time.Second
	}
	lookup, poll := is.LookupTXT, is.Poll
	if lookup == nil {
		lookup = lookupTXT
	}
	if poll <= 0 {
		poll = 2 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if records, err := lookup(pctx, fqdn); err == nil && slices.Contains(records, value) {
			return nil
		}
		select {
		case <-pctx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("the TXT record of %s was not visible within %s", fqdn, limit)
		case <-ticker.C:
		}
	}
}

func (is *Issuer) endpoint(provider string) string {
	if e := is.Endpoints[provider]; e != "" {
		return strings.TrimRight(e, "/")
	}
	return defaultEndpoints[provider]
}

// providerClient talks to a DNS provider. It follows no redirect, which would send the credentials elsewhere; a
// webhook's also reaches no private, loopback, link-local or multicast address unless the order allows it.
func (is *Issuer) providerClient(p DNS01) *http.Client {
	var t *http.Transport
	if is.Transport != nil {
		t = is.Transport.Clone()
	} else {
		t = http.DefaultTransport.(*http.Transport).Clone()
	}
	if (p.Provider == "webhook" || p.Provider == "acme-dns") && !p.AllowPrivateIP {
		t.Proxy = nil // a proxy would dial for us, past the check
		t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirects are not followed")
	}}
}

// publicOnly refuses to connect to an address that is not public: the check runs on the address dialed, after
// resolution, so no DNS answer slips past it.
func publicOnly(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	if !Public(ap.Addr()) {
		return fmt.Errorf("connecting to %s is not allowed: not a public address", ap.Addr())
	}
	return nil
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this" network (RFC 1122)
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space / CGNAT (RFC 6598)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments (RFC 6890)
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1 (RFC 5737)
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking (RFC 2544)
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2 (RFC 5737)
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3 (RFC 5737)
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved (RFC 1112)
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 well-known (RFC 6052)
	netip.MustParsePrefix("64:ff9b:1::/48"),  // NAT64 local-use (RFC 8215)
	netip.MustParsePrefix("100::/64"),        // discard-only (RFC 6666)
	netip.MustParsePrefix("2001::/32"),       // Teredo (RFC 4380)
	netip.MustParsePrefix("2001:db8::/32"),   // documentation (RFC 3849)
	netip.MustParsePrefix("2002::/16"),       // 6to4 (RFC 3056)
	netip.MustParsePrefix("::/96"),           // deprecated IPv4-compatible (RFC 4291)
}

// Public reports whether a is a globally routable public unicast address: not loopback, link-local,
// private (RFC 1918 or ULA), shared (CGNAT), NAT64, reserved, documentation, multicast, or unspecified.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// call sends one JSON request and decodes a JSON answer into out, when given; its errors hold no credential.
func call(ctx context.Context, client *http.Client, method, rawURL string, header map[string]string, body, out any) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, r)
	if err != nil {
		return 0, errors.New("invalid provider URL")
	}
	req.Header.Set("User-Agent", "andaru-outbound")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return 0, ue.Err // the URL may hold a key
		}
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, errors.New("unreadable answer")
		}
	}
	return resp.StatusCode, nil
}

// zones are the names fqdn's zone may be, longest first, down to a registrable two-label name.
func zones(fqdn string) []string {
	labels := strings.Split(strings.TrimSuffix(fqdn, "."), ".")
	var out []string
	for i := 1; i <= len(labels)-2; i++ {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

// relative is fqdn's name inside zone.
func relative(fqdn, zone string) string { return strings.TrimSuffix(fqdn, "."+zone) }

func (is *Issuer) cloudflare(ctx context.Context, client *http.Client, p DNS01, fqdn, value string) (func(context.Context) error, error) {
	base, auth := is.endpoint("cloudflare"), map[string]string{"Authorization": "Bearer " + p.APIToken}
	zoneID := ""
	for _, z := range zones(fqdn) {
		var out struct {
			Success bool `json:"success"`
			Result  []struct {
				ID string `json:"id"`
			} `json:"result"`
		}
		if _, err := call(ctx, client, http.MethodGet, base+"/zones?name="+url.QueryEscape(z), auth, nil, &out); err != nil {
			return nil, fmt.Errorf("cloudflare: find the zone: %w", err)
		}
		if out.Success && len(out.Result) > 0 && out.Result[0].ID != "" {
			zoneID = out.Result[0].ID
			break
		}
	}
	if zoneID == "" {
		return nil, fmt.Errorf("cloudflare: no zone holds %s", fqdn)
	}
	var created struct {
		Success bool `json:"success"`
		Result  struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if _, err := call(ctx, client, http.MethodPost, base+"/zones/"+url.PathEscape(zoneID)+"/dns_records", auth,
		map[string]any{"type": "TXT", "name": fqdn, "content": value, "ttl": txtTTL}, &created); err != nil {
		return nil, fmt.Errorf("cloudflare: create the TXT record: %w", err)
	}
	if !created.Success || created.Result.ID == "" {
		return nil, errors.New("cloudflare: create the TXT record: missing record ID")
	}
	return func(ctx context.Context) error {
		_, err := call(ctx, client, http.MethodDelete, base+"/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(created.Result.ID), auth, nil, nil)
		return err
	}, nil
}

func (is *Issuer) hetzner(ctx context.Context, client *http.Client, p DNS01, fqdn, value string) (func(context.Context) error, error) {
	base, auth := is.endpoint("hetzner"), map[string]string{"Auth-API-Token": p.APIToken}
	zoneID, zoneName := "", ""
	for _, z := range zones(fqdn) {
		var out struct {
			Zones []struct {
				ID string `json:"id"`
			} `json:"zones"`
		}
		status, err := call(ctx, client, http.MethodGet, base+"/zones?name="+url.QueryEscape(z), auth, nil, &out)
		if status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hetzner: find the zone: %w", err)
		}
		if len(out.Zones) > 0 && out.Zones[0].ID != "" {
			zoneID, zoneName = out.Zones[0].ID, z
			break
		}
	}
	if zoneID == "" {
		return nil, fmt.Errorf("hetzner: no zone holds %s", fqdn)
	}
	var created struct {
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	if _, err := call(ctx, client, http.MethodPost, base+"/records", auth,
		map[string]any{"zone_id": zoneID, "type": "TXT", "name": relative(fqdn, zoneName), "value": value, "ttl": txtTTL}, &created); err != nil {
		return nil, fmt.Errorf("hetzner: create the TXT record: %w", err)
	}
	if created.Record.ID == "" {
		return nil, errors.New("hetzner: create the TXT record: missing record ID")
	}
	return func(ctx context.Context) error {
		_, err := call(ctx, client, http.MethodDelete, base+"/records/"+url.PathEscape(created.Record.ID), auth, nil, nil)
		return err
	}, nil
}

func (is *Issuer) digitalocean(ctx context.Context, client *http.Client, p DNS01, fqdn, value string) (func(context.Context) error, error) {
	base, auth := is.endpoint("digitalocean"), map[string]string{"Authorization": "Bearer " + p.APIToken}
	domain := ""
	for _, z := range zones(fqdn) {
		status, err := call(ctx, client, http.MethodGet, base+"/domains/"+url.PathEscape(z), auth, nil, nil)
		if status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("digitalocean: find the domain: %w", err)
		}
		domain = z
		break
	}
	if domain == "" {
		return nil, fmt.Errorf("digitalocean: no domain holds %s", fqdn)
	}
	var created struct {
		Record struct {
			ID int64 `json:"id"`
		} `json:"domain_record"`
	}
	if _, err := call(ctx, client, http.MethodPost, base+"/domains/"+url.PathEscape(domain)+"/records", auth,
		map[string]any{"type": "TXT", "name": relative(fqdn, domain), "data": value, "ttl": txtTTL}, &created); err != nil {
		return nil, fmt.Errorf("digitalocean: create the TXT record: %w", err)
	}
	if created.Record.ID == 0 {
		return nil, errors.New("digitalocean: create the TXT record: missing record ID")
	}
	return func(ctx context.Context) error {
		_, err := call(ctx, client, http.MethodDelete, fmt.Sprintf("%s/domains/%s/records/%d", base, url.PathEscape(domain), created.Record.ID), auth, nil, nil)
		return err
	}, nil
}

func (is *Issuer) porkbun(ctx context.Context, client *http.Client, p DNS01, fqdn, value string) (func(context.Context) error, error) {
	base := is.endpoint("porkbun")
	keys := func(extra map[string]any) map[string]any {
		m := map[string]any{"apikey": p.APIKey, "secretapikey": p.SecretKey}
		maps.Copy(m, extra)
		return m
	}
	type answer struct {
		Status string          `json:"status"`
		ID     json.RawMessage `json:"id"`
	}
	domain := ""
	for _, z := range zones(fqdn) {
		var out answer
		status, err := call(ctx, client, http.MethodPost, base+"/dns/retrieve/"+url.PathEscape(z), nil, keys(nil), &out)
		if status == http.StatusBadRequest || status == http.StatusNotFound || err == nil && out.Status != "SUCCESS" {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("porkbun: find the domain: %w", err)
		}
		domain = z
		break
	}
	if domain == "" {
		return nil, fmt.Errorf("porkbun: no domain holds %s", fqdn)
	}
	var created answer
	if _, err := call(ctx, client, http.MethodPost, base+"/dns/create/"+url.PathEscape(domain), nil,
		keys(map[string]any{"name": relative(fqdn, domain), "type": "TXT", "content": value, "ttl": "600"}), &created); err != nil || created.Status != "SUCCESS" {
		if err == nil {
			err = errors.New("refused")
		}
		return nil, fmt.Errorf("porkbun: create the TXT record: %w", err)
	}
	id := strings.Trim(string(created.ID), `"`)
	if id == "" || id == "null" {
		return nil, errors.New("porkbun: create the TXT record: missing record ID")
	}
	return func(ctx context.Context) error {
		_, err := call(ctx, client, http.MethodPost, base+"/dns/delete/"+url.PathEscape(domain)+"/"+url.PathEscape(id), nil, keys(nil), nil)
		return err
	}, nil
}

func acmeDNS(ctx context.Context, client *http.Client, p DNS01, value string) error {
	if _, err := call(ctx, client, http.MethodPost, strings.TrimRight(p.ServerURL, "/")+"/update",
		map[string]string{"X-Api-User": p.Username, "X-Api-Key": p.Password}, map[string]string{"subdomain": p.Subdomain, "txt": value}, nil); err != nil {
		return fmt.Errorf("acme-dns: update the TXT record: %w", err)
	}
	return nil
}

func webhook(ctx context.Context, client *http.Client, p DNS01, action, domain, fqdn, value string) error {
	var header map[string]string
	if p.Authorization != "" {
		header = map[string]string{"Authorization": p.Authorization}
	}
	if _, err := call(ctx, client, http.MethodPost, p.URL, header, map[string]string{"action": action, "domain": domain, "fqdn": fqdn, "value": value}, nil); err != nil {
		return fmt.Errorf("webhook: %s: %w", action, err)
	}
	return nil
}

// SPDX-License-Identifier: AGPL-3.0-or-later
package api

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// privateSuffixes are name suffixes no public DNS zone can serve, so a malicious site cannot
// make a browser send them as Host (DNS rebinding). Accepting them, and single-label names,
// trusts the local resolver and search domain, which a remote site does not control.
var privateSuffixes = []string{".local", ".lan", ".home.arpa", ".internal", ".localhost"}

// withBrowserGuards protects against a malicious web page in the operator's browser:
//   - a write (any method but GET, HEAD, OPTIONS) from another origin is refused, using the
//     standard library's cross-origin protection (Sec-Fetch-Site, else Origin against Host)
//     plus server.trusted_origins for proxies that rewrite Host;
//   - without an API token, a request whose Host is a public DNS name is refused (421), since
//     only a DNS-rebinding page would reach this server under such a name.
//
// Requests the trusted ingress proxy forwards are exempt, as for authentication.
func (s *Server) withBrowserGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.fromIngressProxy(r) {
			next.ServeHTTP(w, r)
			return
		}
		if err := s.csrf.Check(r); err != nil {
			writeError(w, http.StatusForbidden, "CROSS_ORIGIN", "cross-origin request refused")
			return
		}
		if s.opts.APIToken == "" && !s.allowedHost(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, "HOST_NOT_ALLOWED",
				"host name not allowed without an API token; add it to server.allowed_hosts or set a token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedHost accepts IP literals, single-label names, private suffixes and allowed_hosts.
func (s *Server) allowedHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil || !strings.Contains(host, ".") {
		return true
	}
	for _, suf := range privateSuffixes {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	return slices.Contains(s.opts.AllowedHosts, host) // normalized by config.Parse
}

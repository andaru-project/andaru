// SPDX-License-Identifier: AGPL-3.0-or-later
package api

import (
	"crypto/subtle"
	"html"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
)

// defaultTrustedIngressCIDRs matches only the Home Assistant Supervisor ingress proxy.
// Loopback is deliberately excluded: a reverse proxy on the same host would otherwise let
// any remote client skip bearer auth by sending X-Ingress-Path.
var defaultTrustedIngressCIDRs = []string{"172.30.32.2/32"}

// ingressPathPrefix is the path prefix Home Assistant Supervisor always sends in X-Ingress-Path.
const ingressPathPrefix = "/api/hassio_ingress/"

// authCookieName carries the API token for the browser UI, since EventSource cannot set headers.
const authCookieName = "netplane_token"

func parseTrustedCIDRs(raw []string) []netip.Prefix {
	if len(raw) == 0 {
		raw = defaultTrustedIngressCIDRs
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			// Peers are matched unmapped, so ::ffff:a.b.c.d/n must become a.b.c.d/(n-96).
			if p.Addr().Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(s); err == nil {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

func (s *Server) isTrustedIngressPeer(remoteAddr string) bool {
	if !s.opts.TrustIngressProxy || len(s.trustedIngress) == 0 {
		return false
	}
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range s.trustedIngress {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// sanitizeIngressPath validates X-Ingress-Path as a Supervisor ingress path
// (/api/hassio_ingress/<token>), rejecting full URLs, path traversal, or HTML/control chars.
func sanitizeIngressPath(raw string) string {
	p := strings.TrimRight(strings.TrimSpace(raw), "/")
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, ingressPathPrefix) || len(p) == len(ingressPathPrefix) {
		return ""
	}
	if strings.ContainsAny(p, " \t\r\n\"'<>\\?:;#") || strings.Contains(p, "..") {
		return ""
	}
	return p
}

// withIngress strips a validated X-Ingress-Path prefix when the request arrives from a
// trusted Home Assistant Supervisor / loopback peer.
func (s *Server) withIngress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isTrustedIngressPeer(r.RemoteAddr) {
			if ingressPath := sanitizeIngressPath(r.Header.Get("X-Ingress-Path")); ingressPath != "" &&
				(r.URL.Path == ingressPath || strings.HasPrefix(r.URL.Path, ingressPath+"/")) {
				r2 := r.Clone(r.Context())
				trimmed := strings.TrimPrefix(r.URL.Path, ingressPath)
				if trimmed == "" {
					trimmed = "/"
				}
				r2.URL.Path = trimmed
				// Keep escaped segments (e.g. an interface named Gi1%2F0%2F1) routable.
				r2.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, ingressPath)
				next.ServeHTTP(w, r2)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withAuth enforces bearer token authentication on /api/v1/* when APIToken is configured.
// Health checks remain readable; Home Assistant Ingress requests are trusted only when both
// the peer IP matches trustedIngress CIDRs AND a valid X-Ingress-Path header is present.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.APIToken == "" || !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fromIngressProxy reports whether r was forwarded by the trusted ingress proxy: a trusted
// peer address and a Supervisor ingress path. Auth and the browser guards both exempt it.
func (s *Server) fromIngressProxy(r *http.Request) bool {
	return s.isTrustedIngressPeer(r.RemoteAddr) && sanitizeIngressPath(r.Header.Get("X-Ingress-Path")) != ""
}

// authenticated reports whether r may use token-protected routes: no token is configured,
// it comes from the trusted ingress proxy, or it carries the token.
func (s *Server) authenticated(r *http.Request) bool {
	return s.opts.APIToken == "" ||
		s.fromIngressProxy(r) ||
		s.validToken(r)
}

// validToken accepts the API token as a Bearer Authorization header or, for the browser UI,
// as the netplane_token cookie (SameSite=Strict, so cross-site requests never carry it).
func (s *Server) validToken(r *http.Request) bool {
	want := []byte(s.opts.APIToken)
	if scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " "); ok && strings.EqualFold(scheme, "bearer") {
		return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), want) == 1
	}
	if c, err := r.Cookie(authCookieName); err == nil {
		v, err := url.QueryUnescape(c.Value) // the UI stores encodeURIComponent(token)
		return err == nil && subtle.ConstantTimeCompare([]byte(v), want) == 1
	}
	return false
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown API endpoint")
		return
	}
	if s.opts.WebFS == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>netplane</title></head><body><h1>netplane</h1><p>API server running. OpenAPI contract available at <a href="./api/v1/openapi.json">./api/v1/openapi.json</a>.</p></body></html>`))
		return
	}

	cleaned := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if cleaned == "" {
		cleaned = "index.html"
	}

	if cleaned != "index.html" {
		if f, err := s.opts.WebFS.Open(cleaned); err == nil {
			_ = f.Close()
			http.FileServerFS(s.opts.WebFS).ServeHTTP(w, r)
			return
		}
	}

	indexBytes, err := fs.ReadFile(s.opts.WebFS, "index.html")
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "UI bundle missing index.html")
		return
	}
	// Outside Ingress, a relative base climbs from the route back to the app root, so nested routes
	// such as /devices/<id> load their assets and the app also works under a reverse-proxy sub-path.
	baseHref := "./"
	// Count the path as the browser sent it: Go re-escapes a path it cannot keep verbatim (a raw
	// "|", say), which turns an encoded "%2F" into a slash.
	reqPath, _, _ := strings.Cut(r.RequestURI, "?")
	if !strings.HasPrefix(reqPath, "/") {
		reqPath = r.URL.EscapedPath()
	}
	if depth := strings.Count(reqPath, "/") - 1; depth > 0 {
		baseHref = strings.Repeat("../", depth)
	}
	if s.isTrustedIngressPeer(r.RemoteAddr) {
		if ingressPath := sanitizeIngressPath(r.Header.Get("X-Ingress-Path")); ingressPath != "" {
			baseHref = html.EscapeString(ingressPath) + "/"
		}
	}
	body := strings.Replace(string(indexBytes), "<head>", `<head><base href="`+baseHref+`">`, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

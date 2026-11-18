package web

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"time"

	"github.com/andaru-project/andaru/internal/engine"
)

const maxProxyBody = 17 << 20

type sourceKey struct{}

func newProxy(backendSocket string) http.Handler {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", backendSocket)
		},
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   16,
		MaxConnsPerHost:       64,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 180 * time.Second,
	}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		ErrorLog:  log.New(io.Discard, "", 0),
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(&url.URL{Scheme: "http", Host: "unix"})
			request.Out.Host = request.In.Host
			stripUntrustedMetadata(request.Out.Header)
			request.Out.Header.Set(engine.WebSourceHeader, request.In.Context().Value(sourceKey{}).(string))
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writePublicError(w, http.StatusBadGateway, "BACKEND_UNAVAILABLE", "The management API is temporarily unavailable.")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source, err := clientAddress(r.RemoteAddr)
		if err != nil {
			writePublicError(w, http.StatusBadRequest, "INVALID_SOURCE", "The client address is invalid.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxProxyBody)
		proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sourceKey{}, source)))
	})
}

func clientAddress(remote string) (string, error) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return "", err
	}
	address, err := netip.ParseAddr(host)
	if err != nil || address.IsUnspecified() {
		return "", errors.New("invalid remote address")
	}
	return address.Unmap().String(), nil
}

func stripUntrustedMetadata(header http.Header) {
	for _, name := range []string{
		engine.WebSourceHeader, "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP",
		"X-UID", "X-User", "X-Role", "X-Andaru-Actor", "X-Andaru-Capabilities",
	} {
		header.Del(name)
	}
}

package web

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddress  string
	TLSCertificate string
	TLSKey         string
	Origins        []string
	BackendSocket  string
	AssetsDirectory string
}

func Serve(ctx context.Context, config Config) error {
	if os.Geteuid() == 0 {
		return errors.New("andaru-web refuses to run as root")
	}
	origins, hosts, err := validateConfig(config)
	if err != nil {
		return err
	}
	// Event streams never go idle, so a shutdown ends them rather than waiting for them.
	streams, endStreams := context.WithCancel(context.Background())
	defer endStreams()
	handler, err := newHandler(config.BackendSocket, config.AssetsDirectory, origins, hosts, streams)
	if err != nil {
		return err
	}
	certificate, err := tls.LoadX509KeyPair(config.TLSCertificate, config.TLSKey)
	if err != nil {
		return errors.New("load configured TLS certificate and key")
	}
	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := newServer(handler, certificate)
	server.RegisterOnShutdown(endStreams)
	served := make(chan error, 1)
	go func() { served <- server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	select {
	case err = <-served:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = server.Shutdown(shutdown)
		cancel()
		if err != nil {
			_ = server.Close()
		}
		serveErr := <-served
		if err == nil {
			err = serveErr
		}
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// newServer serves handler over TLS 1.3, with HTTP/2 offered first: a page's requests and its event stream share
// one connection, which HTTP/1.1 would not let them do.
func newServer(handler http.Handler, certificate tls.Certificate) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       180 * time.Second,
		WriteTimeout:      180 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{certificate},
			NextProtos:   []string{"h2", "http/1.1"},
		},
	}
}

func validateConfig(config Config) (map[string]struct{}, map[string]struct{}, error) {
	if err := explicitListenAddress(config.ListenAddress); err != nil {
		return nil, nil, err
	}
	for _, value := range []struct {
		name string
		path string
	}{
		{"TLS certificate", config.TLSCertificate}, {"TLS key", config.TLSKey}, {"backend socket", config.BackendSocket}, {"asset directory", config.AssetsDirectory},
	} {
		if value.path == "" || !filepath.IsAbs(value.path) || filepath.Clean(value.path) != value.path {
			return nil, nil, errors.New(value.name + " requires a canonical absolute path")
		}
	}
	if len(config.BackendSocket) >= 108 {
		return nil, nil, errors.New("backend socket path must be shorter than 108 bytes")
	}
	origins, hosts := make(map[string]struct{}, len(config.Origins)), make(map[string]struct{}, len(config.Origins))
	if len(config.Origins) == 0 {
		return nil, nil, errors.New("at least one exact HTTPS management origin is required")
	}
	for _, origin := range config.Origins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != origin {
			return nil, nil, errors.New("management origins must be exact HTTPS origins without paths, queries, fragments, or credentials")
		}
		if _, duplicate := origins[origin]; duplicate {
			return nil, nil, errors.New("management origins must be unique")
		}
		origins[origin] = struct{}{}
		hosts[parsed.Host] = struct{}{}
	}
	return origins, hosts, nil
}

func explicitListenAddress(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return errors.New("listen requires an explicit IP address and port")
	}
	addressHost := host
	if before, zone, ok := strings.Cut(host, "%"); ok {
		if zone == "" || strings.ContainsAny(zone, "/\\ ") {
			return errors.New("listen address has an invalid IPv6 zone")
		}
		addressHost = before
	}
	address, err := netip.ParseAddr(addressHost)
	parsedPort, portErr := strconv.Atoi(port)
	if err != nil || address.IsUnspecified() || portErr != nil || parsedPort < 1 || parsedPort > 65535 || strconv.Itoa(parsedPort) != port {
		return errors.New("listen requires an explicit non-wildcard IP address and canonical port")
	}
	return nil
}

// newHandler serves the assets and the API. Event streams end when streams does.
func newHandler(backendSocket, assetsDirectory string, origins, hosts map[string]struct{}, streams context.Context) (http.Handler, error) {
	assetInfo, err := os.Lstat(assetsDirectory)
	if err != nil || !assetInfo.IsDir() || assetInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("asset directory must be a real directory")
	}
	indexInfo, err := os.Lstat(filepath.Join(assetsDirectory, "index.html"))
	if err != nil || !indexInfo.Mode().IsRegular() || indexInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("asset directory must contain a regular index.html")
	}
	proxy := newProxy(backendSocket)
	// Event streams hold their backend connections for as long as their pages are open, so they have connections
	// of their own and never leave the API requests waiting for one.
	events := newProxy(backendSocket)
	assets := http.FileServer(http.Dir(assetsDirectory))
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" || r.URL.Path == "/api/v1/fleet/events/stream" { // the bus's and the fleet's
			// The stream lasts as long as the page, so from its first write on, instead of the server's write timeout,
			// each write must finish within streamWriteTimeout: a browser that stops reading loses its stream and
			// its backend connection.
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			defer context.AfterFunc(streams, cancel)()
			events.ServeHTTP(rollingDeadline{w}, r.WithContext(ctx))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writePublicError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		if clean != r.URL.Path && !(r.URL.Path == "" && clean == "/") {
			writePublicError(w, http.StatusBadRequest, "INVALID_PATH", "The request path is invalid.")
			return
		}
		relative := strings.TrimPrefix(clean, "/")
		info, statErr := os.Lstat(filepath.Join(assetsDirectory, filepath.FromSlash(relative)))
		if clean != "/" && statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			assets.ServeHTTP(w, r)
			return
		}
		request := r.Clone(r.Context())
		request.URL.Path = "/"
		w.Header().Set("Cache-Control", "no-store")
		assets.ServeHTTP(w, request)
	})
	return securityHeaders(validatePublicRequest(router, origins, hosts)), nil
}

func validatePublicRequest(next http.Handler, origins, hosts map[string]struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := hosts[r.Host]; !ok {
			writePublicError(w, http.StatusMisdirectedRequest, "INVALID_HOST", "The request Host is not configured for management.")
			return
		}
		cookie := len(r.CookiesNamed("__Host-andaru_session")) != 0
		bearer := len(r.Header.Values("Authorization")) != 0
		if cookie && bearer {
			writePublicError(w, http.StatusBadRequest, "AMBIGUOUS_CREDENTIALS", "Cookie and bearer credentials cannot be combined.")
			return
		}
		originRequired := r.Method == http.MethodPost && r.URL.Path == "/api/v1/session" || cookie && requestChangesState(r.Method)
		if originRequired {
			values := r.Header.Values("Origin")
			if len(values) != 1 {
				writePublicError(w, http.StatusForbidden, "ORIGIN_FAILED", "The request Origin is not trusted.")
				return
			}
			if _, ok := origins[values[0]]; !ok {
				writePublicError(w, http.StatusForbidden, "ORIGIN_FAILED", "The request Origin is not trusted.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requestChangesState(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// streamWriteTimeout bounds each write of an event stream. Over HTTP/2 it also ends a stream that is not written to
// for that long, so it must outlast the engine's heartbeat, every 25 seconds.
var streamWriteTimeout = time.Minute

// rollingDeadline gives each write of a stream streamWriteTimeout to finish.
type rollingDeadline struct{ http.ResponseWriter }

func (w rollingDeadline) Write(p []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	return w.ResponseWriter.Write(p)
}

func (w rollingDeadline) Unwrap() http.ResponseWriter { return w.ResponseWriter }

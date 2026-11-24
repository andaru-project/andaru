package web

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/engine"
)

func TestProxyOverwritesSourceAndIdentityMetadata(t *testing.T) {
	backendPath := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", backendPath)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan *http.Request, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(context.Background())
		clone.Header = r.Header.Clone()
		seen <- clone
		_, _ = io.WriteString(w, `{"ok":true}`)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	assets := t.TempDir()
	if err = os.WriteFile(filepath.Join(assets, "index.html"), []byte("INDEX"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(backendPath, assets, map[string]struct{}{"https://router.example": {}}, map[string]struct{}{"router.example": {}}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://router.example/api/v1/status/links", nil)
	request.Host = "router.example"
	request.RemoteAddr = "192.0.2.44:4242"
	request.Header.Set(engine.WebSourceHeader, "198.51.100.9")
	request.Header.Set("Forwarded", "for=198.51.100.10")
	request.Header.Set("X-User", "root")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("proxy status=%d body=%s", response.Code, response.Body)
	}
	proxied := <-seen
	if proxied.Header.Get(engine.WebSourceHeader) != "192.0.2.44" || proxied.Header.Get("Forwarded") != "" || proxied.Header.Get("X-User") != "" {
		t.Fatalf("untrusted metadata reached backend: %+v", proxied.Header)
	}
}

func TestPublicHostOriginHeadersAndStaticFallback(t *testing.T) {
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("INDEX"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(filepath.Join(t.TempDir(), "missing.sock"), assets, map[string]struct{}{"https://router.example": {}}, map[string]struct{}{"router.example": {}}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	badHost := httptest.NewRequest(http.MethodGet, "https://evil.example/", nil)
	badHost.Host = "evil.example"
	badHost.RemoteAddr = "192.0.2.1:1"
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, badHost)
	if badResponse.Code != http.StatusMisdirectedRequest {
		t.Fatalf("bad Host status=%d", badResponse.Code)
	}
	badOrigin := httptest.NewRequest(http.MethodPost, "https://router.example/api/v1/session", strings.NewReader(`{"login":"admin","password":"not-logged"}`))
	badOrigin.Host = "router.example"
	badOrigin.RemoteAddr = "192.0.2.1:1"
	badOrigin.Header.Set("Origin", "https://evil.example")
	originResponse := httptest.NewRecorder()
	handler.ServeHTTP(originResponse, badOrigin)
	if originResponse.Code != http.StatusForbidden {
		t.Fatalf("bad Origin status=%d", originResponse.Code)
	}
	page := httptest.NewRequest(http.MethodGet, "https://router.example/users", nil)
	page.Host = "router.example"
	page.RemoteAddr = "192.0.2.1:1"
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, page)
	if pageResponse.Code != http.StatusOK || pageResponse.Body.String() != "INDEX" || pageResponse.Header().Get("Content-Security-Policy") == "" || pageResponse.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("static fallback status=%d headers=%v body=%q", pageResponse.Code, pageResponse.Header(), pageResponse.Body.String())
	}
}

func TestWebConfigurationRefusesDefaultsAndWildcards(t *testing.T) {
	base := Config{
		ListenAddress: "192.0.2.1:8443", TLSCertificate: "/etc/andaru/tls.crt", TLSKey: "/etc/andaru/tls.key",
		Origins: []string{"https://router.example:8443"}, BackendSocket: "/run/andaru-web/engine.sock", AssetsDirectory: "/usr/share/andaru/web",
	}
	if _, _, err := validateConfig(base); err != nil {
		t.Fatal(err)
	}
	for _, listen := range []string{"", ":8443", "0.0.0.0:8443", "[::]:8443", "router.example:8443"} {
		candidate := base
		candidate.ListenAddress = listen
		if _, _, err := validateConfig(candidate); err == nil {
			t.Fatalf("wildcard/default listen %q accepted", listen)
		}
	}
	candidate := base
	candidate.Origins = nil
	if _, _, err := validateConfig(candidate); err == nil {
		t.Fatal("missing management origin accepted")
	}
}

// An event stream outlives the server's read and write timeouts, and its own limit on each write, and each event
// reaches the browser as it is sent.
func TestEventStreamOutlivesTheTimeouts(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP/1.1", true: "HTTP/2"}[h2], func(t *testing.T) { eventStreamOutlivesTheTimeouts(t, h2) })
	}
}

func eventStreamOutlivesTheTimeouts(t *testing.T, h2 bool) {
	defer func(previous time.Duration) { streamWriteTimeout = previous }(streamWriteTimeout)
	// Over HTTP/2 a write deadline resets the stream when it passes, written to or not, so it must outlast the
	// gap between writes (200 ms here, 25 s from the engine); the server's own timeouts are shorter than either.
	streamWriteTimeout = 300 * time.Millisecond
	backendPath := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", backendPath)
	if err != nil {
		t.Fatal(err)
	}
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			_, _ = io.WriteString(w, "data: "+strconv.Itoa(i)+"\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(200 * time.Millisecond)
		}
	})}
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(func() { _ = backend.Close() })
	assets := t.TempDir()
	if err = os.WriteFile(filepath.Join(assets, "index.html"), []byte("INDEX"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(backendPath, assets, map[string]struct{}{"https://router.example": {}}, map[string]struct{}{"router.example": {}}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ReadTimeout, server.Config.WriteTimeout = 100*time.Millisecond, 100*time.Millisecond
	server.EnableHTTP2 = h2
	server.StartTLS()
	defer server.Close()
	for _, path := range []string{"/api/v1/events", "/api/v1/fleet/events/stream"} {
		request, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		request.Host = "router.example"
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if want := map[bool]int{false: 1, true: 2}[h2]; response.ProtoMajor != want {
			t.Fatalf("HTTP/%d, want HTTP/%d", response.ProtoMajor, want)
		}
		reader := bufio.NewReader(response.Body)
		for i := range 3 {
			line, err := reader.ReadString('\n')
			if want := "data: " + strconv.Itoa(i) + "\n"; err != nil || line != want {
				t.Fatalf("line %d = %q, %v; want %q", i, line, err, want)
			}
			_, _ = reader.ReadString('\n')
		}
	}
}

// A stream ends, and lets go of the engine, when its browser stops reading and when andaru-web shuts down.
func TestEventStreamEndsWhenStalledOrShutDown(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP/1.1", true: "HTTP/2"}[h2], func(t *testing.T) { eventStreamEndsWhenStalledOrShutDown(t, h2) })
	}
}

func eventStreamEndsWhenStalledOrShutDown(t *testing.T, h2 bool) {
	defer func(previous time.Duration) { streamWriteTimeout = previous }(streamWriteTimeout)
	streamWriteTimeout = 200 * time.Millisecond
	backendPath := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", backendPath)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{}, 2)
	chunk := "data: " + strings.Repeat("x", 32<<10) + "\n\n"
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { ended <- struct{}{} }()
		w.Header().Set("Content-Type", "text/event-stream")
		for r.Context().Err() == nil {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if r.URL.Query().Has("slow") {
				time.Sleep(50 * time.Millisecond)
			}
		}
	})}
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(func() { _ = backend.Close() })
	assets := t.TempDir()
	if err = os.WriteFile(filepath.Join(assets, "index.html"), []byte("INDEX"), 0600); err != nil {
		t.Fatal(err)
	}
	streams, endStreams := context.WithCancel(context.Background())
	handler, err := newHandler(backendPath, assets, map[string]struct{}{"https://router.example": {}}, map[string]struct{}{"router.example": {}}, streams)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = h2
	server.StartTLS()
	defer server.Close()
	open := func(query string) *http.Response {
		t.Helper()
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/events"+query, nil)
		request.Host = "router.example"
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	wait := func(what string) {
		t.Helper()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatalf("the engine's stream outlived %s", what)
		}
	}

	stalled := open("")
	defer stalled.Body.Close()
	wait("a browser that stopped reading")

	reading := open("?slow")
	defer reading.Body.Close()
	go func() { _, _ = io.Copy(io.Discard, reading.Body) }()
	endStreams()
	wait("the shutdown")
}

// The server offers HTTP/2, and still answers HTTP/1.1.
func TestServerOffersHTTP2(t *testing.T) {
	certified := httptest.NewTLSServer(http.NotFoundHandler())
	certified.Close()
	server := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Proto) }), certified.TLS.Certificates[0])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	defer server.Close()
	for _, h2 := range []bool{true, false} {
		transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: h2}
		response, err := (&http.Client{Transport: transport}).Get("https://" + listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if want := map[bool]string{true: "HTTP/2.0", false: "HTTP/1.1"}[h2]; string(body) != want {
			t.Errorf("served %s, want %s", body, want)
		}
	}
}

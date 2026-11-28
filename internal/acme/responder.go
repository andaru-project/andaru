package acme

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// challengePath is the only path the HTTP-01 responder serves.
const challengePath = "/.well-known/acme-challenge/"

// validToken is an RFC 8555 token: base64url, no padding.
var validToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,256}$`)

// responder answers HTTP-01 challenges while an order is open, and nothing else.
type responder struct {
	srv     *http.Server
	mu      sync.Mutex
	answers map[string]string
}

func startResponder(addr string) (*responder, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen for HTTP-01 on %s: %w", addr, err)
	}
	r := &responder{answers: map[string]string{}}
	r.srv = &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		if err := r.srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = l.Close()
		}
	}()
	return r, nil
}

func (r *responder) add(token, answer string) bool {
	if !validToken.MatchString(token) {
		return false
	}
	r.mu.Lock()
	r.answers[token] = answer
	r.mu.Unlock()
	return true
}

func (r *responder) close() { _ = r.srv.Close() }

func (r *responder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	token, ok := strings.CutPrefix(req.URL.Path, challengePath)
	r.mu.Lock()
	answer, known := r.answers[token]
	r.mu.Unlock()
	if req.Method != http.MethodGet || !ok || !validToken.MatchString(token) || !known {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(answer))
}

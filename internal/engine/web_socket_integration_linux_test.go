//go:build integration

package engine

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebSocketChecksExactPeerBeforeHTTPParsing(t *testing.T) {
	requireRoot(t)
	t.Run("configured peer and modes", func(t *testing.T) {
		path := filepath.Join(privateTestDir(t), "andaru-web", "engine.sock")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- serveWebUnix(ctx, WebSocketOptions{SocketPath: path, UID: 0, GID: 0}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peer, ok := r.Context().Value(peerKey{}).(peerIdentity)
				if !ok || !peer.verified || !peer.web || peer.uid != 0 {
					t.Error("verified web peer context missing")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
		}()
		waitForUnixSocket(t, path)
		parent, err := os.Lstat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		socket, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if parent.Mode().Perm() != 0710 || socket.Mode().Perm() != 0660 {
			t.Fatalf("web socket modes parent=%v socket=%v", parent.Mode(), socket.Mode())
		}
		response, err := unixTestClient(path).Get("http://unix/test")
		if err != nil || response.StatusCode != http.StatusNoContent {
			t.Fatalf("verified web peer response=%v err=%v", response, err)
		}
		response.Body.Close()
		cancel()
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("wrong peer is closed before handler", func(t *testing.T) {
		path := filepath.Join(privateTestDir(t), "andaru-web", "engine.sock")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var parsed atomic.Bool
		done := make(chan error, 1)
		go func() {
			done <- serveWebUnix(ctx, WebSocketOptions{SocketPath: path, UID: 65534, GID: 0}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				parsed.Store(true)
			}))
		}()
		waitForUnixSocket(t, path)
		connection, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.SetDeadline(time.Now().Add(time.Second))
		if _, err = io.WriteString(connection, "GET /forged HTTP/1.1\r\nHost: unix\r\nX-User: root\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1)
		if count, readErr := connection.Read(buffer); count != 0 || readErr == nil {
			t.Fatalf("wrong peer reached HTTP: bytes=%d err=%v", count, readErr)
		}
		_ = connection.Close()
		if parsed.Load() {
			t.Fatal("wrong UID reached the HTTP handler")
		}
		cancel()
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func waitForUnixSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Unix socket did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

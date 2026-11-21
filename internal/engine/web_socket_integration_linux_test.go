//go:build integration

package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestWebSocketChecksExactPeerBeforeHTTPParsing(t *testing.T) {
	requireRoot(t)
	t.Run("configured peer and modes", func(t *testing.T) {
		path := filepath.Join(shortPrivateDir(t), "andaru-web", "engine.sock")
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
		path := filepath.Join(shortPrivateDir(t), "andaru-web", "engine.sock")
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
		_, writeErr := io.WriteString(connection, "GET /forged HTTP/1.1\r\nHost: unix\r\nX-User: root\r\n\r\n")
		if writeErr != nil && !errors.Is(writeErr, syscall.EPIPE) {
			t.Fatal(writeErr)
		}
		if writeErr == nil {
			buffer := make([]byte, 1)
			if count, readErr := connection.Read(buffer); count != 0 || readErr == nil {
				t.Fatalf("wrong peer reached HTTP: bytes=%d err=%v", count, readErr)
			}
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
func shortPrivateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "aw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
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

func TestWebSocketReplacesOnlyADeadLeftover(t *testing.T) {
	requireRoot(t)
	serve := func(ctx context.Context, path string) <-chan error {
		done := make(chan error, 1)
		go func() {
			done <- serveWebUnix(ctx, WebSocketOptions{SocketPath: path, UID: 0, GID: 0}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
		}()
		return done
	}

	t.Run("a killed engine's socket is replaced", func(t *testing.T) {
		path := filepath.Join(shortPrivateDir(t), "andaru-web", "engine.sock")
		if err := os.Mkdir(filepath.Dir(path), 0710); err != nil {
			t.Fatal(err)
		}
		// A listener closed without unlinking leaves what a SIGKILL leaves: a socket file nothing answers.
		dead, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		dead.SetUnlinkOnClose(false)
		dead.Close()
		ctx, cancel := context.WithCancel(context.Background())
		done := serve(ctx, path)
		// The path exists from the start, so wait for an answer, not for the file.
		var response *http.Response
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if response, err = unixTestClient(path).Get("http://unix/test"); err == nil || time.Now().After(deadline) {
				break
			}
		}
		if err != nil || response.StatusCode != http.StatusNoContent {
			t.Fatalf("engine did not serve over the replaced socket: response=%v err=%v", response, err)
		}
		response.Body.Close()
		cancel()
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a socket something listens on is refused", func(t *testing.T) {
		path := filepath.Join(shortPrivateDir(t), "andaru-web", "engine.sock")
		if err := os.Mkdir(filepath.Dir(path), 0710); err != nil {
			t.Fatal(err)
		}
		other, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		err = <-serve(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), "something listens on it") {
			t.Fatalf("a live socket was not refused: %v", err)
		}
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("the live socket was removed: %v", statErr)
		}
	})

	t.Run("a file that is not a socket is refused", func(t *testing.T) {
		path := filepath.Join(shortPrivateDir(t), "andaru-web", "engine.sock")
		if err := os.Mkdir(filepath.Dir(path), 0710); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		err := <-serve(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), "not a root-owned socket") {
			t.Fatalf("a plain file was not refused: %v", err)
		}
	})
}

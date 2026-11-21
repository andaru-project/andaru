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

// waitForUnixSocket waits for the socket in the mode serveWebUnix leaves it in: between bind and chmod it is
// there with the umask's mode, and a test that looked then would see that one.
func waitForUnixSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 && info.Mode().Perm() == 0660 {
			return
		}
		if time.Now().After(deadline) {
			mode := os.FileMode(0)
			if info != nil {
				mode = info.Mode()
			}
			t.Fatalf("Unix socket did not become ready: mode=%v err=%v", mode, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWebSocketReplacesOnlyADeadLeftover(t *testing.T) {
	requireRoot(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// webSocketPath returns a socket path in a parent of the mode serveWebUnix requires, whatever the umask.
	webSocketPath := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(shortPrivateDir(t), "andaru-web", "engine.sock")
		if err := os.Mkdir(filepath.Dir(path), 0710); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(path), 0710); err != nil {
			t.Fatal(err)
		}
		// serveWebUnix wants the parent's group to be the web GID, whatever group this process runs with.
		if err := os.Chown(filepath.Dir(path), 0, 0); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// deadSocket leaves what a SIGKILL leaves: a socket file nothing answers.
	deadSocket := func(t *testing.T, path string) {
		t.Helper()
		dead, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		dead.SetUnlinkOnClose(false)
		dead.Close()
	}
	get := func(path string) error {
		response, err := unixTestClient(path).Get("http://unix/test")
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			return errors.New(response.Status)
		}
		return nil
	}

	// refused runs an engine that must fail at once; a regression that serves instead fails here, not at the suite's timeout.
	refused := func(t *testing.T, path, want string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- serveWebUnix(ctx, WebSocketOptions{SocketPath: path, UID: 0, GID: 0}, handler) }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want a refusal with %q, got %v", want, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the engine served instead of refusing with %q", want)
		}
	}
	kept := func(t *testing.T, path string) {
		t.Helper()
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("the refused path was removed: %v", err)
		}
	}
	// served starts an engine, waits for its answer and returns how to stop it. The path may exist from the start,
	// so it waits for an answer, not for the file, and reports an engine that failed to start.
	served := func(t *testing.T, path string) func() {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- serveWebUnix(ctx, WebSocketOptions{SocketPath: path, UID: 0, GID: 0}, handler) }()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			select {
			case err := <-done:
				cancel()
				t.Fatalf("the engine did not start: %v", err)
			default:
			}
			err := get(path)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("the engine did not serve: %v", err)
			}
		}
		return func() {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("a killed engine's socket is replaced, a second engine is refused, and a restart works", func(t *testing.T) {
		path := webSocketPath(t)
		deadSocket(t, path)
		stop := served(t, path)
		// While it serves, its live socket is not a leftover to anyone else.
		refused(t, path, "another engine serves this web socket")
		if err := get(path); err != nil {
			t.Fatalf("the first engine stopped serving: %v", err)
		}
		stop()
		// The lock went with the engine: the same path serves again in this process.
		served(t, path)()
		if info, err := os.Lstat(path + ".lock"); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("the lock file is not a 0600 file: %v %v", info, err)
		}
	})

	t.Run("a file that is not a socket is refused and kept, and frees the lock", func(t *testing.T) {
		path := webSocketPath(t)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		refused(t, path, "not a root-owned socket")
		kept(t, path)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		// A refusal that kept the lock would refuse this one too.
		served(t, path)()
	})

	t.Run("a socket root does not own is refused and kept", func(t *testing.T) {
		path := webSocketPath(t)
		deadSocket(t, path)
		if err := os.Lchown(path, 65534, 0); err != nil {
			t.Fatal(err)
		}
		refused(t, path, "not a root-owned socket")
		kept(t, path)
	})

	t.Run("a listener that does not hold the lock keeps its socket", func(t *testing.T) {
		path := webSocketPath(t)
		other, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		refused(t, path, "something listens on it without holding its lock")
		kept(t, path)
	})

	t.Run("a socket that cannot be shown dead is kept", func(t *testing.T) {
		path := webSocketPath(t)
		// A datagram socket answers a stream connect with EPROTOTYPE, not ECONNREFUSED.
		other, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		refused(t, path, "cannot be shown dead")
		kept(t, path)
	})

	t.Run("a lock that is a symlink or not a private file is refused", func(t *testing.T) {
		path := webSocketPath(t)
		target := filepath.Join(filepath.Dir(path), "elsewhere")
		if err := os.Symlink(target, path+".lock"); err != nil {
			t.Fatal(err)
		}
		refused(t, path, "open web engine socket lock")
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("the engine wrote through the lock symlink: %v", err)
		}
		if err := os.Remove(path + ".lock"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path+".lock", 0666); err != nil {
			t.Fatal(err)
		}
		refused(t, path, "root-owned 0600 regular file")
	})
}

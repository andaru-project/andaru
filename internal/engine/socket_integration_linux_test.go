//go:build integration

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/host"
)

func TestMain(m *testing.M) {
	if i := slices.Index(os.Args, "--peer-helper"); i >= 0 {
		client := unixTestClient(os.Args[i+1])
		req, err := http.NewRequest(http.MethodGet, "http://unix/api/v1/status/links", nil)
		if err != nil {
			os.Exit(92)
		}
		req.Header.Set("X-UID", "0")
		req.Header.Set("X-User", "root")
		req.Header.Set("Authorization", "Bearer root")
		response, err := client.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(93)
		}
		defer response.Body.Close()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			os.Exit(94)
		}
		fmt.Printf("%d %s\n", response.StatusCode, body.Error.Code)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("integration suite requires root; this is not a skipped proof")
	}
}

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// TempDir's leaf is 0777 & umask beneath a private parent, not necessarily 0700.
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func traversablePeerDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "andaru-peer-")
	if err != nil {
		t.Fatal(err)
	}
	owned, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		current, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		stat, ok := current.Sys().(*syscall.Stat_t)
		if !current.IsDir() || !ok || stat.Uid != 0 || !os.SameFile(owned, current) {
			t.Error("peer fixture ownership changed; refusing cleanup")
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(dir, 0711); err != nil {
		t.Fatal(err)
	}
	return dir
}

func unixTestClient(path string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
}

type runningTestEngine struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func (e *runningTestEngine) stop(t *testing.T) {
	t.Helper()
	e.cancel()
	select {
	case <-e.done:
		if e.err != nil {
			t.Errorf("engine shutdown: %v", e.err)
		}
	case <-time.After(5 * time.Second):
		t.Error("engine did not finish bounded shutdown")
	}
}

func startTestEngine(t *testing.T, path string, handler http.Handler) *runningTestEngine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e := &runningTestEngine{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(e.done)
		if handler == nil {
			e.err = Serve(ctx, path)
		} else {
			e.err = serveUnix(ctx, path, handler)
		}
	}()
	t.Cleanup(func() { e.stop(t) })
	client := unixTestClient(path)
	t.Cleanup(client.CloseIdleConnections)
	ready, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	for {
		req, _ := http.NewRequestWithContext(ready, http.MethodGet, "http://unix/api/v1/openapi.json", nil)
		response, err := client.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return e
			}
		}
		select {
		case <-e.done:
			t.Fatalf("engine exited before readiness: %v", e.err)
		case <-ready.Done():
			t.Fatal("engine never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestUnixAPIRejectsUntrustedPeers(t *testing.T) {
	requireRoot(t)
	root := traversablePeerDir(t)
	path := filepath.Join(root, "run", "engine.sock")
	e := startTestEngine(t, path, nil)
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	socket, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Mode().Perm() != 0700 || socket.Mode().Perm() != 0600 {
		t.Fatalf("unsafe default modes: %v %v", parent.Mode(), socket.Mode())
	}
	// The child must reach HTTP, so prove the kernel gate independently of file permissions.
	if err := os.Chmod(filepath.Dir(path), 0711); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Dir(path), 0700); _ = os.Chmod(path, 0600) }()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	childPath := filepath.Join(root, "peer-helper")
	output, err := os.OpenFile(childPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(childPath, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, childPath, "--peer-helper", path)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}}}
	body, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(body)) != "403 FORBIDDEN" {
		t.Fatalf("untrusted peer was not denied: %q %v", body, err)
	}
	e.stop(t)
}

func TestUnixAPISocketLifecycle(t *testing.T) {
	requireRoot(t)
	t.Run("existing file", func(t *testing.T) {
		path := filepath.Join(privateTestDir(t), "engine.sock")
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := Serve(ctx, path)
		body, readErr := os.ReadFile(path)
		if err == nil || readErr != nil || string(body) != "keep" {
			t.Fatalf("unrelated file changed: %q %v %v", body, err, readErr)
		}
	})
	t.Run("existing symlink", func(t *testing.T) {
		root := privateTestDir(t)
		target := filepath.Join(root, "target")
		path := filepath.Join(root, "engine.sock")
		if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := Serve(ctx, path); err == nil {
			t.Fatal("existing symlink accepted")
		}
		if got, err := os.Readlink(path); err != nil || got != target {
			t.Fatalf("symlink changed: %q %v", got, err)
		}
		if body, err := os.ReadFile(target); err != nil || string(body) != "keep" {
			t.Fatalf("symlink target changed: %q %v", body, err)
		}
	})
	for _, stale := range []bool{false, true} {
		name := "live socket"
		if stale {
			name = "stale socket"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(privateTestDir(t), "engine.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			listener.SetUnlinkOnClose(false)
			defer listener.Close()
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if stale {
				listener.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := Serve(ctx, path); err == nil {
				t.Fatal("pre-existing endpoint accepted")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("pre-existing socket replaced: %v", err)
			}
			if !stale {
				connection, err := net.Dial("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				connection.Close()
			}
		})
	}
	for _, symlink := range []bool{false, true} {
		name := "writable ancestor"
		if symlink {
			name = "symlink ancestor"
		}
		t.Run(name, func(t *testing.T) {
			root := privateTestDir(t)
			ancestor := filepath.Join(root, "ancestor")
			target := filepath.Join(root, "target")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(target, ancestor); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(ancestor, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(ancestor, 0777); err != nil {
					t.Fatal(err)
				}
			}
			parent := filepath.Join(ancestor, "private")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "engine.sock")
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if err := Serve(ctx, path); err == nil {
				t.Fatal("untrusted ancestor accepted")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("endpoint created despite untrusted ancestor: %v", err)
			}
			info, err := os.Lstat(ancestor)
			if err != nil || (symlink && info.Mode()&os.ModeSymlink == 0) || (!symlink && info.Mode().Perm() != 0777) {
				t.Fatalf("ancestor changed: %v", err)
			}
		})
	}
	t.Run("unsafe parent", func(t *testing.T) {
		parent := privateTestDir(t)
		if err := os.Chmod(parent, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(parent, "engine.sock")
		if err := Serve(context.Background(), path); err == nil {
			t.Fatal("permissive parent accepted")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("socket created despite unsafe parent: %v", err)
		}
		info, err := os.Stat(parent)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatal("existing parent silently changed")
		}
	})
	t.Run("symlink parent", func(t *testing.T) {
		root := privateTestDir(t)
		target := filepath.Join(root, "target")
		parent := filepath.Join(root, "link")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, parent); err != nil {
			t.Fatal(err)
		}
		if err := Serve(context.Background(), filepath.Join(parent, "engine.sock")); err == nil {
			t.Fatal("symlink parent accepted")
		}
	})
	t.Run("wrong parent owner", func(t *testing.T) {
		parent := privateTestDir(t)
		if err := os.Chown(parent, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		defer os.Chown(parent, 0, 0)
		if err := Serve(context.Background(), filepath.Join(parent, "engine.sock")); err == nil {
			t.Fatal("foreign-owned parent accepted")
		}
	})
	t.Run("untrusted ancestor", func(t *testing.T) {
		root := privateTestDir(t)
		ancestor := filepath.Join(root, "foreign")
		parent := filepath.Join(ancestor, "private")
		if err := os.Mkdir(ancestor, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(parent, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(ancestor, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		defer os.Chown(ancestor, 0, 0)
		path := filepath.Join(parent, "engine.sock")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := Serve(ctx, path); err == nil {
			t.Fatal("an ancestor could let another user rename the private directory")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("socket created beneath an untrusted namespace: %v", err)
		}
	})
	t.Run("explicit modes and clean restart", func(t *testing.T) {
		previous := syscall.Umask(0022)
		defer syscall.Umask(previous)
		path := filepath.Join(privateTestDir(t), "run", "engine.sock")
		e := startTestEngine(t, path, nil)
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("manual umask weakened socket: %v %v", info, err)
		}
		e.stop(t)
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("owned socket remains after shutdown: %v", err)
		}
		startTestEngine(t, path, nil).stop(t)
	})
	t.Run("replacement remains untouched", func(t *testing.T) {
		root := privateTestDir(t)
		path := filepath.Join(root, "engine.sock")
		e := startTestEngine(t, path, nil)
		if err := os.Rename(path, filepath.Join(root, "original.sock")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		e.stop(t)
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "replacement" {
			t.Fatalf("replacement was removed: %q %v", body, err)
		}
	})
}

func TestUnixAPIShutdownCancelsReadsAndClosesHeldConnections(t *testing.T) {
	requireRoot(t)
	started, cancelled, heldStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := newHandler(func(ctx context.Context) ([]host.LinkStatus, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	path := filepath.Join(privateTestDir(t), "run", "engine.sock")
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/held-test-request" {
			close(heldStarted)
			_, _ = io.Copy(io.Discard, r.Body)
			return
		}
		h.ServeHTTP(w, r)
	})
	e := startTestEngine(t, path, testHandler)
	client := unixTestClient(path)
	defer client.CloseIdleConnections()
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		response, err := client.Get("http://unix/api/v1/status/links")
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("inventory never started")
	}
	held, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := io.WriteString(held, "POST /held-test-request HTTP/1.1\r\nHost: unix\r\nContent-Length: 10\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-heldStarted:
	case <-time.After(time.Second):
		t.Fatal("held request never reached the server")
	}
	e.cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("daemon cancellation did not reach the active read before forced close")
	}
	e.stop(t)
	if err := held.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := held.Read(b[:]); err == nil {
		t.Fatal("held connection still readable without closure")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("held connection was not closed")
	}
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("active response did not finish")
	}
}

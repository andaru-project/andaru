package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/securefs"
)

// Serve runs the root-local engine API without opening an IP listener.
func Serve(ctx context.Context, socketPath, stateDir string) (result error) {
	if os.Geteuid() != 0 {
		return errors.New("the engine requires root")
	}
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil && result == nil {
			result = err
		}
	}()
	lifetime, cancel := context.WithCancelCause(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case err := <-store.Failure():
			cancel(err)
		case <-lifetime.Done():
		}
	}()
	defer func() { cancel(context.Canceled); <-stopped }()
	err = serveUnix(lifetime, socketPath, newHandler(host.ReadLinks, newSavedHandler(store, host.ReadLinks, host.ReadRealizerFacts)))
	cause := context.Cause(lifetime)
	if cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return err
}

func serveUnix(ctx context.Context, socketPath string, handler http.Handler) (result error) {
	if os.Geteuid() != 0 {
		return errors.New("the engine requires root")
	}
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || len(socketPath) >= 108 {
		return errors.New("engine socket requires a canonical absolute path shorter than 108 bytes")
	}
	if ctx.Err() != nil {
		return nil
	}
	parent := filepath.Dir(socketPath)
	if err := securefs.CheckAncestors(filepath.Dir(parent)); err != nil {
		return err
	}
	if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create engine runtime directory: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect engine runtime directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || !securefs.RootOwned(info) {
		return errors.New("engine socket parent must be a root-owned directory with mode 0700, not a symlink")
	}
	if _, err := os.Lstat(socketPath); err == nil {
		return errors.New("engine socket path already exists; investigate it rather than replacing it")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect engine socket path: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on engine socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	owned, err := os.Lstat(socketPath)
	if err != nil || owned.Mode()&os.ModeSocket == 0 || !securefs.RootOwned(owned) {
		_ = listener.Close()
		return errors.New("cannot establish ownership of the bound engine socket")
	}
	defer func() {
		_ = listener.Close()
		result = errors.Join(result, removeOwnedSocket(socketPath, owned))
	}()
	if err := os.Chmod(socketPath, 0600); err != nil {
		return fmt.Errorf("protect engine socket: %w", err)
	}

	daemonContext, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return daemonContext },
		ConnContext: func(ctx context.Context, connection net.Conn) context.Context {
			unix, ok := connection.(*net.UnixConn)
			if !ok {
				return ctx
			}
			uid, err := peerUID(unix)
			if err != nil {
				slog.Error("read engine peer credentials", "error", err)
				return ctx
			}
			return context.WithValue(ctx, peerKey{}, peerIdentity{uid: uid, verified: true})
		},
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	var serveErr error
	select {
	case serveErr = <-served:
		cancel()
		_ = server.Close()
	case <-ctx.Done():
		cancel()
		shutdownContext, stop := context.WithTimeout(context.Background(), 3*time.Second)
		err := server.Shutdown(shutdownContext)
		stop()
		if err != nil {
			slog.Warn("forcing engine HTTP shutdown", "error", err)
			_ = server.Close()
		}
		serveErr = <-served
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}

func removeOwnedSocket(path string, owned os.FileInfo) error {
	current, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect engine socket on shutdown: %w", err)
	}
	if current.Mode()&os.ModeSocket == 0 || !securefs.RootOwned(current) || !os.SameFile(owned, current) {
		slog.Warn("engine socket endpoint changed; leaving replacement untouched", "path", path)
		return nil
	}
	// Unprivileged users cannot race this check in the verified root-controlled namespace.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove owned engine socket: %w", err)
	}
	return nil
}

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *syscall.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credentialErr != nil {
		return 0, credentialErr
	}
	if credentials == nil {
		return 0, errors.New("missing Unix peer credentials")
	}
	return credentials.Uid, nil
}

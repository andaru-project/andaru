package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/aliasfeed"
	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/delivery"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/identity"
	"github.com/andaru-project/andaru/internal/notify"
	"github.com/andaru-project/andaru/internal/securefs"
)

type WebSocketOptions struct {
	SocketPath string
	UID        uint32
	GID        uint32
	Origins    []string
}

// Serve runs the root-local engine API without opening an IP listener.
func Serve(ctx context.Context, socketPath, stateDir string) error {
	return serveEngine(ctx, socketPath, stateDir, nil)
}

// ServeWithWeb adds the separately gated socket used by the unprivileged HTTPS process.
func ServeWithWeb(ctx context.Context, socketPath, stateDir string, web WebSocketOptions) error {
	if web.UID == 0 || web.GID == 0 {
		return errors.New("web socket requires non-root static UID and GID")
	}
	if !filepath.IsAbs(web.SocketPath) || filepath.Clean(web.SocketPath) != web.SocketPath || len(web.SocketPath) >= 108 {
		return errors.New("web engine socket requires a canonical absolute path shorter than 108 bytes")
	}
	if _, _, err := managementOrigins(web.Origins); err != nil {
		return err
	}
	if web.SocketPath == socketPath || filepath.Dir(web.SocketPath) == filepath.Dir(socketPath) {
		return errors.New("root and web engine sockets must use separate parent directories")
	}
	return serveEngine(ctx, socketPath, stateDir, &web)
}

func serveEngine(ctx context.Context, socketPath, stateDir string, web *WebSocketOptions) (result error) {
	if os.Geteuid() != 0 {
		return errors.New("the engine requires root")
	}
	if err := validateRouteCapabilities(openAPIDocument, engineRoutes); err != nil {
		return fmt.Errorf("validate engine routes: %w", err)
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
	if err = initializeAuthorizationState(ctx, stateDir); err != nil {
		return fmt.Errorf("initialize live authorization state: %w", err)
	}
	identities, err := identity.Open(ctx, stateDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := identities.Close(); err != nil && result == nil {
			result = err
		}
	}()
	audits, err := audit.Open(ctx, stateDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := audits.Close(); err != nil && result == nil {
			result = err
		}
	}()
	busSocket := filepath.Join(filepath.Dir(socketPath), "bus.sock")
	events, closeEvents, err := openBus(stateDir, busSocket, ServicesSocket)
	if err != nil {
		return fmt.Errorf("start the event bus: %w", err)
	}
	defer func() {
		if err := closeEvents(); err != nil && result == nil {
			result = err
		}
	}()
	sources := newNotices(events, stateDir, func(ctx context.Context) (bool, error) {
		view, err := store.UpgradePreview(ctx, nil)
		return len(view.Questions) > 0, err
	})
	head := publishHead(events)
	// The engine serves for as long as lifetime: a store failure ends it.
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
	runner := newSystemdRunner(stateDir, busSocket)
	feeds := newAliasFeeds(lifetime, stateDir, events, savedAndLive(stateDir, store), func(ctx context.Context) error { return runner.Realize(ctx, "aliases", 0) })
	store.OnSave(func(v config.Version) { head(v); sources.nudge(); feeds.nudge() })
	// Without its store the layer does not read: notifications wait in the bus's log, for its retention, until a
	// start that opens it, and the API answers that they are unavailable.
	// A link in a notification is a path in the web UI: sent as a full URL from the first management origin.
	origin := ""
	if web != nil && len(web.Origins) > 0 { // an engine without a web socket has no origin
		origin = web.Origins[0]
	}
	notificationsHandler := notificationsAPI{channels: store.NotificationChannels, events: events, origin: origin}
	// andaru-outbound's consumer exists before the first job, which it would otherwise start after.
	if err := events.Declare(outboundUser, delivery.Consumer, delivery.Filter); err != nil {
		slog.Error("notification deliveries may be missed until andaru-outbound attaches", "error", err)
	}
	if err := events.Declare(outboundUser, aliasfeed.Consumer, aliasfeed.Filter); err != nil {
		slog.Error("alias fetches may be missed until andaru-outbound attaches", "error", err)
	}
	stopResults, err := events.Consume(aliasfeed.ResultConsumer, aliasfeed.ResultFilter, feeds.handle)
	if err != nil {
		return fmt.Errorf("read alias results: %w", err)
	}
	defer stopResults()
	// A load the consumer waits on stops before the consumer is waited for.
	defer cancel(context.Canceled)
	var active []notify.Notification
	notifications := slog.Default().With("component", "notifications")
	if layer, err := notify.Open(filepath.Join(stateDir, "notifications.db"), events, notifications, deliverNotifications(events, store.NotificationChannels, origin, notifications)); err != nil {
		slog.Error("notifications unavailable", "error", err)
	} else {
		defer func() {
			if err := layer.Close(); err != nil && result == nil {
				result = err
			}
		}()
		notificationsHandler.layer = layer
		if active, err = layer.Active(); err != nil {
			slog.Warn("notifications: read the active list", "error", err)
		}
	}
	coordinator := newSecurityCoordinator(identities, audits, stateDir)
	read := hostReader{Links: host.ReadLinks, Facts: host.ReadRealizerFacts, Managed: host.ReadManaged, BootUnit: host.ReadBootUnit}
	applies := newAppliesHandler(store.ApplyInput, store.Roles, read, runner, stateDir, identities, coordinator, publishApply(events, "engine"))
	identityAPI := newIdentityAPI(identities, store, coordinator)
	auditAPI := newAuditAPI(coordinator)
	backups := newBackupHandler(store, identities, coordinator, stateDir, host.ReadLinks)
	if err = backups.restores.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconcile portable restores: %w", err)
	}
	if err = audits.Reconcile(ctx, coordinator); err != nil && !errors.Is(err, audit.ErrReconciliationComplete) {
		return fmt.Errorf("reconcile identity audit operations: %w", err)
	}
	// A head of an older schema is saved in the current one once, so this Andaru loads its settings directly. It runs
	// after an interrupted restore is settled, which compares the head with its journal. A failure before the commit
	// leaves the head read through its migration, as before, and the next start tries again; one in the commit fences
	// the store and stops the engine, as any store failure does.
	if saved, err := store.MigrateHead(ctx); err != nil {
		slog.Error("settings not saved in the current schema", "error", err)
	} else if saved != "" {
		slog.Info("settings saved in the current schema", "version", saved)
	}
	// The engine states its notifications once the head is in the current schema, and again as applies and saves go.
	stopApplies, err := events.Subscribe("apply/+", func(string, []byte) { sources.nudge(); feeds.apply() })
	if err != nil {
		return fmt.Errorf("follow applies for notifications: %w", err)
	}
	defer stopApplies()
	noticed := make(chan struct{})
	go func() { defer close(noticed); sources.run(lifetime, active) }()
	defer func() { cancel(context.Canceled); <-noticed }()
	fed := make(chan struct{})
	go func() { defer close(fed); feeds.run(lifetime) }()
	defer func() { cancel(context.Canceled); <-fed }()
	// The traffic history fills for as long as the engine serves, and the sampler is gone before this returns.
	sampled := make(chan struct{})
	go func() { defer close(sampled); sampleTraffic(lifetime, traffic, systemRoot) }()
	defer func() { cancel(context.Canceled); <-sampled }()
	var fleetAPI http.Handler
	fleet, err := openFleetService(lifetime, stateDir, localHostSource{
		ReadLinks:  host.ReadLinks,
		ReadConfig: store.Snapshot,
	}, slog.Default())
	if err != nil {
		slog.Error("fleet service unavailable; continuing without fleet API", "error", err)
		fleetAPI = fleetUnavailableHandler()
	} else {
		fleet.Start(lifetime)
		defer func() {
			if err := fleet.Close(); err != nil && result == nil {
				result = err
			}
		}()
		fleetAPI = fleet
	}
	setupCodes := newSetupCodeStore(stateDir)
	setupCodeAPI := newSetupCodeHandler(identities, setupCodes)
	hostnameAPI := newHostnameHandler(nil)
	updateAPI := newUpdateAPI(stateDir, nil)
	rootAPI := newHandlerWithAllServices(host.ReadLinks, newSavedHandler(store, host.ReadLinks, host.ReadRealizerFacts, applies, backups), identityAPI, auditAPI, fleetAPI, updateAPI, setupCodeAPI, hostnameAPI, notificationsHandler, aliasesAPI{feeds: feeds})
	if web == nil {
		err = serveUnix(lifetime, socketPath, rootAPI)
	} else {
		var webAPI http.Handler
		webAPI, err = newWebAPIWithSetupCodes(identities, store, coordinator, rootAPI, events, setupCodes, stateDir, web.Origins)
		if err != nil {
			return err
		}
		served := make(chan error, 2)
		go func() { served <- serveUnix(lifetime, socketPath, rootAPI) }()
		go func() { served <- serveWebUnix(lifetime, *web, webAPI) }()
		first := <-served
		if lifetime.Err() == nil {
			if first == nil {
				first = errors.New("engine listener stopped unexpectedly")
			}
			cancel(first)
		}
		second := <-served
		err = errors.Join(first, second)
	}
	cause := context.Cause(lifetime)
	if cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(cause, err)
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
	listener, closeListener, err := listenRootSocket(socketPath, "engine")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeListener()) }()

	return serveHTTP(ctx, listener, handler, func(ctx context.Context, connection net.Conn) context.Context {
		unix, ok := connection.(*net.UnixConn)
		if !ok {
			return ctx
		}
		uid, err := bus.PeerUID(unix)
		if err != nil {
			slog.Error("read engine peer credentials", "error", err)
			return ctx
		}
		return context.WithValue(ctx, peerKey{}, peerIdentity{uid: uid, verified: true})
	})
}

// listenRootSocket binds a socket only root can reach, in a root-owned directory of mode 0700 it creates if needed.
// closeListener closes it and removes the socket file.
func listenRootSocket(socketPath, name string) (_ *net.UnixListener, closeListener func() error, _ error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || len(socketPath) >= 108 {
		return nil, nil, errors.New(name + " socket requires a canonical absolute path shorter than 108 bytes")
	}
	parent := filepath.Dir(socketPath)
	if err := securefs.CheckAncestors(filepath.Dir(parent)); err != nil {
		return nil, nil, err
	}
	if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return nil, nil, fmt.Errorf("create %s socket directory: %w", name, err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect %s socket directory: %w", name, err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || !securefs.RootOwned(info) {
		return nil, nil, errors.New(name + " socket parent must be a root-owned directory with mode 0700, not a symlink")
	}
	if _, err := os.Lstat(socketPath); err == nil {
		return nil, nil, errors.New(name + " socket path already exists; investigate it rather than replacing it")
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("inspect %s socket path: %w", name, err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s socket: %w", name, err)
	}
	listener.SetUnlinkOnClose(false)
	owned, err := os.Lstat(socketPath)
	if err != nil || owned.Mode()&os.ModeSocket == 0 || !securefs.RootOwned(owned) {
		_ = listener.Close()
		return nil, nil, errors.New("cannot establish ownership of the bound " + name + " socket")
	}
	closeListener = func() error {
		_ = listener.Close()
		return removeOwnedSocket(socketPath, owned)
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("protect %s socket: %w", name, err), closeListener())
	}
	return listener, closeListener, nil
}

// listenGroupSocket binds a socket of mode 0660 for one group, in a root-owned directory of mode 0710 with that group,
// which it creates if needed, and takes it over from a killed engine (claimWebSocket). closeListener closes it,
// removes the socket file and gives the claim back.
func listenGroupSocket(socketPath string, gid uint32, group, name string) (*net.UnixListener, func() error, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || len(socketPath) >= 108 {
		return nil, nil, errors.New(name + " socket requires a canonical absolute path shorter than 108 bytes")
	}
	parent := filepath.Dir(socketPath)
	if err := securefs.CheckAncestors(filepath.Dir(parent)); err != nil {
		return nil, nil, err
	}
	created := false
	if err := os.Mkdir(parent, 0710); err == nil {
		created = true
		if err = os.Chown(parent, 0, int(gid)); err != nil {
			_ = os.Remove(parent)
			return nil, nil, fmt.Errorf("set %s runtime directory group: %w", name, webGroupHint(err, gid, group))
		}
		if err = os.Chmod(parent, 0710); err != nil {
			_ = os.Remove(parent)
			return nil, nil, fmt.Errorf("protect %s runtime directory: %w", name, err)
		}
	} else if !os.IsExist(err) {
		return nil, nil, fmt.Errorf("create %s runtime directory: %w", name, err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect %s runtime directory: %w", name, err)
	}
	stat, statOK := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0710 || !securefs.RootOwned(info) || !statOK || stat.Gid != gid {
		if created {
			_ = os.Remove(parent)
		}
		return nil, nil, errors.New(name + " socket parent must be a root-owned 0710 directory with the configured group, not a symlink")
	}
	claim, err := claimWebSocket(socketPath, name)
	if err != nil {
		return nil, nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		_ = claim.Close()
		return nil, nil, fmt.Errorf("listen on %s socket: %w", name, err)
	}
	listener.SetUnlinkOnClose(false)
	bound, err := os.Lstat(socketPath)
	if err != nil || bound.Mode()&os.ModeSocket == 0 || !securefs.RootOwned(bound) {
		_ = listener.Close()
		_ = claim.Close()
		return nil, nil, errors.New("cannot establish ownership of the bound " + name + " socket")
	}
	closeListener := func() error {
		_ = listener.Close()
		return errors.Join(removeOwnedSocket(socketPath, bound), claim.Close())
	}
	fail := func(err error) (*net.UnixListener, func() error, error) {
		return nil, nil, errors.Join(err, closeListener())
	}
	if err = os.Chown(socketPath, 0, int(gid)); err != nil {
		return fail(fmt.Errorf("set %s socket group: %w", name, webGroupHint(err, gid, group)))
	}
	if err = os.Chmod(socketPath, 0660); err != nil {
		return fail(fmt.Errorf("protect %s socket: %w", name, err))
	}
	owned, err := os.Lstat(socketPath)
	if err != nil {
		return fail(fmt.Errorf("inspect bound %s socket: %w", name, err))
	}
	socketStat, statOK := owned.Sys().(*syscall.Stat_t)
	if owned.Mode()&os.ModeSocket == 0 || owned.Mode().Perm() != 0660 || !securefs.RootOwned(owned) || !os.SameFile(bound, owned) || !statOK || socketStat.Gid != gid {
		return fail(errors.New("cannot establish ownership and mode of the bound " + name + " socket"))
	}
	return listener, closeListener, nil
}

func serveWebUnix(ctx context.Context, web WebSocketOptions, handler http.Handler) (result error) {
	if os.Geteuid() != 0 {
		return errors.New("the engine requires root")
	}
	if ctx.Err() != nil {
		return nil
	}
	listener, closeListener, err := listenGroupSocket(web.SocketPath, web.GID, "andaru-web", "web engine")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeListener()) }()
	verified := &webPeerListener{UnixListener: listener, uid: web.UID}
	return serveHTTP(ctx, verified, handler, func(ctx context.Context, connection net.Conn) context.Context {
		peer, ok := connection.(*verifiedWebConn)
		if !ok {
			return ctx
		}
		return context.WithValue(ctx, peerKey{}, peerIdentity{uid: peer.uid, verified: true, web: true})
	})
}

type webPeerListener struct {
	*net.UnixListener
	uid uint32
}

func (l *webPeerListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, credentialErr := bus.PeerUID(connection)
		if credentialErr != nil || uid != l.uid {
			_ = connection.Close()
			continue
		}
		return &verifiedWebConn{UnixConn: connection, uid: uid}, nil
	}
}

type verifiedWebConn struct {
	*net.UnixConn
	uid uint32
}

func serveHTTP(ctx context.Context, listener net.Listener, handler http.Handler, connContext func(context.Context, net.Conn) context.Context) error {
	daemonContext, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
		BaseContext:       func(net.Listener) context.Context { return daemonContext },
		ConnContext:       connContext,
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

// webGroupHint explains a refused chgrp to group: without CAP_CHOWN the engine may hand files only to a group it is in.
func webGroupHint(err error, gid uint32, group string) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w (the engine must be a member of group %d: SupplementaryGroups=%s in its unit)", err, gid, group)
	}
	return err
}

// claimWebSocket takes a group socket's path for this engine and clears what a killed engine left there: systemd
// restarts the engine, but the socket's directory comes from tmpfiles.d, so nothing else removes the file.
// The lock beside the socket is held for the engine's life, so a socket found under it has no owner among engines
// that take the lock. The lock file stays; it is root's, 0600.
func claimWebSocket(path, name string) (_ *os.File, err error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open %s socket lock: %w", name, err)
	}
	// Every refusal below gives the lock back; a return sets the results before this runs, so it closes the file itself.
	defer func() {
		if err != nil {
			_ = lock.Close()
		}
	}()
	// An existing lock file must be what this engine would have made: one another user can open could be held against it.
	if info, statErr := lock.Stat(); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !securefs.RootOwned(info) {
		return nil, errors.New(name + " socket lock must be a root-owned 0600 regular file")
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another engine serves this " + name + " socket")
		}
		return nil, fmt.Errorf("lock %s socket: %w", name, err)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return lock, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect %s socket path: %w", name, err)
	}
	if !rootOwnedSocket(info) {
		return nil, errors.New(name + " socket path already exists and is not a root-owned socket; investigate it rather than replacing it")
	}
	// A listener that answers does not hold the lock (an engine from before the lock existed): leave it its socket.
	// Only a refused connection says nothing listens: a full backlog or another socket type must not read as dead.
	connection, dialErr := net.DialTimeout("unix", path, time.Second)
	if dialErr == nil {
		_ = connection.Close()
		return nil, errors.New(name + " socket path already exists and something listens on it without holding its lock")
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return nil, fmt.Errorf("%s socket path already exists and cannot be shown dead: %w", name, dialErr)
	}
	slog.Warn("removing the "+name+" socket a previous engine left behind", "path", path)
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale %s socket: %w", name, err)
	}
	return lock, nil
}

func rootOwnedSocket(info os.FileInfo) bool {
	return info.Mode()&os.ModeSocket != 0 && securefs.RootOwned(info)
}

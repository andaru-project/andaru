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

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/addon"
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
	catalog, err := addon.LoadCatalog(addon.DefaultCatalogDir)
	if err != nil {
		return fmt.Errorf("load add-on catalog: %w", err)
	}
	store.SetCatalog(catalog)
	if web != nil {
		store.SetManagementOrigins(web.Origins)
	}
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
	if web != nil {
		if _, err := reconcileManagementOrigins(ctx, stateDir, identities, web.Origins); err != nil {
			return fmt.Errorf("reconcile management origins: %w", err)
		}
	}
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
	events, closeEvents, err := openBus(stateDir, busSocket, ServicesSocket, AddonsBusSocket)
	if err != nil {
		return fmt.Errorf("start the event bus: %w", err)
	}
	defer func() {
		if err := closeEvents(); err != nil && result == nil {
			result = err
		}
	}()
	upgradePending := func(ctx context.Context) (bool, error) {
		view, err := store.UpgradePreview(ctx, nil)
		return len(view.Questions) > 0, err
	}
	sources := newNotices(events, stateDir, upgradePending)
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
	addonBus := newAddonBusSync(systemRoot, stateDir, filepath.Dir(busSocket), catalog, events, store.AddonBusGrantsAt, func(ctx context.Context, tokensFile string) error {
		return runner.Realize(ctx, "addon-bus-tokens", 0, tokensFile)
	})
	hold := newAddonsHold(systemRoot, func(ctx context.Context) (bool, error) {
		if !addonBus.isReady() {
			return false, errors.New("waiting for initial add-on event bus token sync")
		}
		return upgradePending(ctx)
	}, func(ctx context.Context) error { return runner.Realize(ctx, "addons-release-hold", 0) })
	addonBus.onReady = hold.nudge
	// The live generation's certificates, as the head holds them: a save may renew or replace one.
	certs := newCertSync(stateDir, filepath.Dir(busSocket), catalog, store.CertificateBindings, func(ctx context.Context, file string) error {
		return runner.Realize(ctx, "cert-sync", 0, file)
	})
	certs.events = events
	store.OnSave(func(v config.Version) { head(v); sources.nudge(); feeds.nudge(); hold.nudge(); certs.nudge() })
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
	readFacts := func() (host.RealizerFacts, error) { return host.ReadRealizerFactsFor(stateDir) }
	read := hostReader{Links: host.ReadLinks, Facts: readFacts, Managed: host.ReadManaged, BootUnit: host.ReadBootUnit, Catalog: catalog, StateDir: stateDir}
	applies := newAppliesHandler(store.ApplyInput, store.RealizationInput, store.Roles, read, runner, stateDir, identities, coordinator, publishApply(events, "engine"))
	applies.addonBus = addonBus
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
	// Results are delivered, and may load the lists, once an interrupted restore is settled.
	stopResults, err := events.Consume(aliasfeed.ResultConsumer, aliasfeed.ResultFilter, feeds.handle)
	if err != nil {
		return fmt.Errorf("read alias results: %w", err)
	}
	defer stopResults()
	// A load the consumer waits on stops before the consumer is waited for.
	defer cancel(context.Canceled)
	// The engine states its notifications once the head is in the current schema, and again as applies and saves go.
	stopApplies, err := events.Subscribe("apply/+", func(string, []byte) { sources.nudge(); feeds.apply(); addonBus.nudge(); certs.invalidate() })
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
	// Sync live add-on bus tokens and grants synchronously before releasing the boot add-on hold so
	// add-ons starting at boot find their runtime token file and registered bus client already in place.
	initialBusRetry := addonBus.sync(lifetime)
	busSynced := make(chan struct{})
	go func() { defer close(busSynced); addonBus.runWithInitial(lifetime, initialBusRetry) }()
	defer func() { cancel(context.Canceled); <-busSynced }()
	// The certificates too, so andaru-web and the add-ons the hold releases start on the current ones after a reboot
	// or a rollback that happened while the engine was down.
	initialCertErr := certs.sync(lifetime)
	if initialCertErr != nil {
		slog.Warn("cert-sync at startup; retrying", "error", initialCertErr)
	}
	certSynced := make(chan struct{})
	go func() { defer close(certSynced); certs.run(lifetime, initialCertErr != nil) }()
	defer func() { cancel(context.Canceled); <-certSynced }()
	// Evaluate and release the boot add-on hold synchronously before serving API requests so an early
	// Activate never mistakes boot startup for a pending settings hold.
	initialHoldRetry := hold.check(lifetime)
	held := make(chan struct{})
	go func() { defer close(held); hold.runWithInitial(lifetime, initialHoldRetry) }()
	defer func() { cancel(context.Canceled); <-held }()
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
	var webOrigins []string
	if web != nil {
		webOrigins = web.Origins
	}
	addonsHandler := newAddonsAPI(catalog, store, stateDir, systemRoot, webOrigins, runner, coordinator)
	pkiHandler := &pkiAPI{store: store, coordinator: coordinator, events: events, certs: certs}
	// ACME orders run through andaru-outbound, which has the network; the engine keeps each order's key.
	acmeOrders := newACMECoordinator(store, stateDir, systemRoot, events, certs, audits, func(ctx context.Context, verb string, args ...string) error {
		return runner.Realize(ctx, verb, 0, args...)
	})
	stopACME, err := events.Subscribe(acme.ResultFilter, acmeOrders.onResult)
	if err != nil {
		return fmt.Errorf("follow ACME results: %w", err)
	}
	defer stopACME()
	stopACMEApplies, err := events.Subscribe("apply/+", acmeOrders.onApply)
	if err != nil {
		return fmt.Errorf("follow applies for ACME HTTP-01: %w", err)
	}
	defer stopACMEApplies()
	acmeDone := make(chan struct{})
	go func() { defer close(acmeDone); acmeOrders.run(lifetime) }()
	defer func() { cancel(context.Canceled); <-acmeDone }()
	configuration := newSavedHandlerWithPKI(store, host.ReadLinks, readFacts, applies, backups, pkiHandler)
	rootAPI := newHandlerWithDeps(handlerDeps{
		readLinks:        host.ReadLinks,
		configuration:    configuration,
		identityAPI:      identityAPI,
		auditAPI:         auditAPI,
		fleetAPI:         fleetAPI,
		updateAPI:        updateAPI,
		setupCodeAPI:     setupCodeAPI,
		hostnameAPI:      hostnameAPI,
		notificationsAPI: notificationsHandler,
		aliasesAPI:       aliasesAPI{feeds: feeds},
		addonsAPI:        addonsHandler,
		pkiAPI:           pkiHandler,
		acmeAPI:          &acmeAPI{acme: acmeOrders, audits: audits},
		stateDir:         stateDir,
	})
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
	return bindClaimedSocket(socketPath, socketPath+".lock", name, 0o660,
		func(p string) error {
			if err := os.Chown(p, 0, int(gid)); err != nil {
				return fmt.Errorf("set %s socket group: %w", name, webGroupHint(err, gid, group))
			}
			return nil
		},
		func(owned os.FileInfo) bool {
			socketStat, ok := owned.Sys().(*syscall.Stat_t)
			return ok && socketStat.Gid == gid
		},
	)
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

// listenAddonsBusSocket binds the token-authenticated add-on event bus socket (mode 0666) inside a root-owned
// 0755 socket directory whose parent (/run/andaru-addons) is a root-owned 0700 directory so host non-root users
// cannot traverse it while containers mounting the socket directory can connect regardless of container UID.
// Its claim lock is stored at lockPath (in the engine's private /run/andaru dir), never inside the mounted socket dir.
func listenAddonsBusSocket(socketPath, lockPath string) (*net.UnixListener, func() error, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || len(socketPath) >= 108 {
		return nil, nil, errors.New("add-ons bus socket requires a canonical absolute path shorter than 108 bytes")
	}
	if !filepath.IsAbs(lockPath) || filepath.Clean(lockPath) != lockPath {
		return nil, nil, errors.New("add-ons bus socket lock requires a canonical absolute path")
	}
	parent := filepath.Dir(socketPath)
	grandparent := filepath.Dir(parent)
	if err := securefs.CheckAncestors(filepath.Dir(grandparent)); err != nil {
		return nil, nil, err
	}
	if err := securefs.CheckAncestors(filepath.Dir(lockPath)); err != nil {
		return nil, nil, err
	}
	if err := os.Mkdir(grandparent, 0o700); err != nil && !os.IsExist(err) {
		return nil, nil, fmt.Errorf("create add-ons runtime directory: %w", err)
	}
	gpInfo, err := os.Lstat(grandparent)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect add-ons runtime directory: %w", err)
	}
	if !gpInfo.IsDir() || gpInfo.Mode().Perm() != 0o700 || !securefs.RootOwned(gpInfo) {
		return nil, nil, errors.New("add-ons runtime directory must be a root-owned 0700 directory, not a symlink")
	}
	createdParent := false
	if err := os.Mkdir(parent, 0o755); err == nil {
		createdParent = true
		if err = os.Chmod(parent, 0o755); err != nil {
			_ = os.Remove(parent)
			return nil, nil, fmt.Errorf("protect add-ons bus socket directory: %w", err)
		}
	} else if !os.IsExist(err) {
		return nil, nil, fmt.Errorf("create add-ons bus socket directory: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect add-ons bus socket directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o755 || !securefs.RootOwned(info) {
		if createdParent {
			_ = os.Remove(parent)
		}
		return nil, nil, errors.New("add-ons bus socket directory must be a root-owned 0755 directory, not a symlink")
	}
	return bindClaimedSocket(socketPath, lockPath, "add-ons bus", 0o666, nil, nil)
}

func bindClaimedSocket(socketPath, lockPath, name string, mode os.FileMode, chgrp func(string) error, checkExtra func(os.FileInfo) bool) (*net.UnixListener, func() error, error) {
	claim, err := claimSocket(socketPath, lockPath, name)
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
	if chgrp != nil {
		if err = chgrp(socketPath); err != nil {
			return fail(err)
		}
	}
	if err = os.Chmod(socketPath, mode); err != nil {
		return fail(fmt.Errorf("protect %s socket: %w", name, err))
	}
	owned, err := os.Lstat(socketPath)
	if err != nil {
		return fail(fmt.Errorf("inspect bound %s socket: %w", name, err))
	}
	if owned.Mode()&os.ModeSocket == 0 || owned.Mode().Perm() != mode || !securefs.RootOwned(owned) || !os.SameFile(bound, owned) || (checkExtra != nil && !checkExtra(owned)) {
		return fail(errors.New("cannot establish ownership and mode of the bound " + name + " socket"))
	}
	return listener, closeListener, nil
}

// claimSocket takes a socket's path for this engine using lockPath and clears what a killed engine left there:
// systemd restarts the engine, but tmpfiles.d socket directories persist, so nothing else removes a stale socket.
// The lock file is held for the engine's life; it is root's, 0600.
func claimSocket(path, lockPath, name string) (_ *os.File, err error) {
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
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

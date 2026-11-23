// SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/netplane/api"
	"github.com/andaru-project/andaru/internal/netplane/config"
	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/driver/andaru"
	"github.com/andaru-project/andaru/internal/netplane/driver/openwrt"
	"github.com/andaru-project/andaru/internal/netplane/driver/opnsense"
	"github.com/andaru-project/andaru/internal/netplane/driver/pfsense"
	"github.com/andaru-project/andaru/internal/netplane/driver/rtlplayground"
	"github.com/andaru-project/andaru/internal/netplane/driver/snmp"
	"github.com/andaru-project/andaru/internal/netplane/driver/unifi"
	"github.com/andaru-project/andaru/internal/netplane/events"
	"github.com/andaru-project/andaru/internal/netplane/poller"
	"github.com/andaru-project/andaru/internal/netplane/store"
)

// Version is set at build time via -ldflags.
var Version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Println("netplane", Version)
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "netplane serve: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: netplane <serve|version> [flags]")
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "Path to netplane YAML configuration file")
	listen := fs.String("listen", "", "Override HTTP listen address (default :8099)")
	dbPath := fs.String("db", "", "Override SQLite database path")
	haOptions := fs.String("ha-options", "", "Home Assistant add-on options.json to export as environment (api_token, secrets)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *haOptions != "" {
		if err := config.ApplyHAOptions(*haOptions); err != nil {
			return err
		}
	}

	var cfg *config.Config
	var err error
	if *cfgPath != "" {
		cfg, err = config.LoadFile(*cfgPath)
		if err != nil {
			return err
		}
	} else {
		cfg = &config.Config{
			Server: config.ServerConfig{
				Listen:            ":8099",
				DBPath:            "netplane.db",
				TrustIngressProxy: true,
				ResolvedAPIToken:  os.Getenv(config.TokenEnv),
			},
			Retention: 24 * time.Hour,
		}
	}
	if *listen != "" {
		cfg.Server.Listen = *listen
	}
	if *dbPath != "" {
		cfg.Server.DBPath = *dbPath
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Server.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	st.SetRetention(cfg.Retention)

	enabled := map[string]bool{}
	for _, t := range cfg.Targets {
		enabled[t.ID] = t.Enabled
	}
	if err := st.PruneTargets(ctx, enabled); err != nil {
		return fmt.Errorf("prune stale targets: %w", err)
	}
	for _, t := range cfg.Targets {
		if !t.Enabled {
			continue // disabled targets keep no stored state
		}
		if err := st.UpsertTarget(ctx, t); err != nil {
			return fmt.Errorf("persist target %s: %w", t.ID, err)
		}
	}

	reg := driver.NewRegistry()
	registerDrivers(reg)

	bus := events.NewBus()
	sched := poller.New(reg, st, bus, logger)

	srv := api.New(api.Options{
		Version:             Version,
		Store:               st,
		Registry:            reg,
		Poller:              sched,
		Bus:                 bus,
		Targets:             cfg.Targets,
		APIToken:            cfg.Server.ResolvedAPIToken,
		TrustIngressProxy:   cfg.Server.TrustIngressProxy,
		TrustedIngressCIDRs: cfg.Server.TrustedIngressCIDRs,
		AllowedHosts:        cfg.Server.AllowedHosts,
		TrustedOrigins:      cfg.Server.TrustedOrigins,
		WebFS:               embeddedWebFS(),
		Logger:              logger,
	})

	httpSrv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		// Request contexts end on SIGTERM so open SSE streams don't stall Shutdown for its full timeout.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	pollDone := make(chan struct{})
	go func() {
		sched.Run(ctx, cfg.Targets)
		close(pollDone)
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("netplane server listening", "addr", cfg.Server.Listen, "version", Version, "targets", len(cfg.Targets), "drivers", reg.Kinds())
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down netplane")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		<-pollDone
		return nil
	case err := <-errCh:
		stop()
		<-pollDone
		return err
	}
}

func registerDrivers(reg *driver.Registry) {
	for _, d := range []driver.Driver{
		andaru.New(),
		openwrt.New(),
		opnsense.New(),
		pfsense.New(),
		rtlplayground.New(),
		snmp.New(),
		unifi.New(),
	} {
		_ = reg.Register(d)
	}
}

package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/andaru-project/andaru/internal/engine"
	"github.com/andaru-project/andaru/internal/outbound"
)

// outboundCommand runs andaru-outbound until it is stopped.
func outboundCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("outbound", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("bus-socket", engine.ServicesSocket, "")
	cacheDir := flags.String("cache-dir", cmp.Or(os.Getenv("CACHE_DIRECTORY"), "/var/cache/andaru-outbound"), "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, outboundHelp)
			return 0
		}
		return usageError(stderr, outboundUsage, "andaru outbound")
	}
	if flags.NArg() > 0 || !filepath.IsAbs(*socket) || !filepath.IsAbs(*cacheDir) {
		return usageError(stderr, outboundUsage, "andaru outbound")
	}
	s := outbound.Service{Socket: *socket, CacheDir: *cacheDir, Log: slog.New(slog.NewTextHandler(stderr, nil))}
	if err := s.Run(ctx); err != nil {
		fmt.Fprintln(stderr, "andaru outbound:", err)
		return 1
	}
	return 0
}

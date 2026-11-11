package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/andaru-project/andaru/internal/engine"
)

const defaultSocket = "/run/andaru/engine.sock"
const responseLimit = 8 * 1024 * 1024

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := "usage: andaru engine [--socket PATH] [--state-dir PATH] | andaru ctl [--socket PATH] links|openapi|config|versions|preview|apply|applies | andaru realize [--state-dir PATH] activate|rollback|confirm|boot"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	if args[0] == "realize" {
		return realizeCommand(ctx, args[1:], stderr, newRealizer)
	}
	if args[0] != "engine" && args[0] != "ctl" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("socket", defaultSocket, "absolute local engine socket path")
	stateDir := "/var/lib/andaru"
	if args[0] == "engine" {
		flags.StringVar(&stateDir, "state-dir", stateDir, "absolute private state directory")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !filepath.IsAbs(*socket) || filepath.Clean(*socket) != *socket || len(*socket) >= 108 {
		fmt.Fprintln(stderr, "socket must be a canonical absolute path shorter than 108 bytes")
		return 2
	}
	if args[0] == "engine" {
		if flags.NArg() != 0 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
			fmt.Fprintln(stderr, "state-dir must be a canonical absolute path")
			return 2
		}
		if err := engine.Serve(ctx, *socket, stateDir); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	return runCtl(ctx, *socket, flags.Args(), stdin, stdout, stderr)
}

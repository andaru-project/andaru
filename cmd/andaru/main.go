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
	"strings"
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
	usage := "usage: andaru engine [--socket PATH] [--state-dir PATH] [--web-socket PATH --web-uid UID --web-gid GID --web-origin HTTPS_ORIGIN ...] | andaru ctl [--socket PATH] links|prefixes|openapi|config|versions|preview|apply|applies|takeover|backup|users|tokens|audit | andaru realize [--state-dir PATH] activate|rollback|confirm|boot|leases|prefixes | andaru upgrade check|prepare"
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
	if args[0] == "upgrade" {
		return upgradeCommand(ctx, args[1:], stdout, stderr)
	}
	if args[0] != "engine" && args[0] != "ctl" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("socket", defaultSocket, "absolute local engine socket path")
	stateDir := "/var/lib/andaru"
	webSocket := ""
	var webUID, webGID uint64
	var webOrigins repeatedFlags
	if args[0] == "engine" {
		flags.StringVar(&stateDir, "state-dir", stateDir, "absolute private state directory")
		flags.StringVar(&webSocket, "web-socket", "", "absolute dedicated web engine socket path")
		flags.Uint64Var(&webUID, "web-uid", 0, "exact non-root static web process UID")
		flags.Uint64Var(&webGID, "web-gid", 0, "exact non-root static web socket group GID")
		flags.Var(&webOrigins, "web-origin", "exact HTTPS management origin; repeat for each origin")
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
		webConfigured := webSocket != "" || webUID != 0 || webGID != 0 || len(webOrigins) != 0
		var err error
		if !webConfigured {
			err = engine.Serve(ctx, *socket, stateDir)
		} else if webSocket == "" || webUID == 0 || webGID == 0 || webUID > 1<<32-1 || webGID > 1<<32-1 || len(webOrigins) == 0 ||
			!filepath.IsAbs(webSocket) || filepath.Clean(webSocket) != webSocket || len(webSocket) >= 108 {
			fmt.Fprintln(stderr, "web-socket, non-root 32-bit web-uid/web-gid, and at least one web-origin must be supplied together")
			return 2
		} else {
			err = engine.ServeWithWeb(ctx, *socket, stateDir, engine.WebSocketOptions{
				SocketPath: webSocket, UID: uint32(webUID), GID: uint32(webGID), Origins: []string(webOrigins),
			})
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	return runCtl(ctx, *socket, flags.Args(), stdin, stdout, stderr)
}

type repeatedFlags []string

func (f *repeatedFlags) String() string {
	return strings.Join(*f, ",")
}

func (f *repeatedFlags) Set(value string) error {
	if value == "" {
		return errors.New("value cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

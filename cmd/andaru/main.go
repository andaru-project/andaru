package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/andaru-project/andaru/internal/engine"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	usage := "usage: andaru engine [--socket PATH] [--state-dir PATH] [--web-socket PATH --web-uid UID --web-gid GID --web-origin HTTPS_ORIGIN ...] | andaru ctl [--socket PATH] links|prefixes|openapi|config|versions|preview|apply|applies|takeover|backup|kept|users|tokens|audit|factory-reset | andaru realize [--state-dir PATH] activate|rollback|confirm|boot|leases|prefixes|factory-reset"
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
		if webSocket != "" && (webUID == 0 || webGID == 0) {
			peerUID, peerGID, peerErr := readWebPeer("/etc/andaru/engine-web-peer")
			if peerErr == nil {
				if webUID == 0 {
					webUID = peerUID
				}
				if webGID == 0 {
					webGID = peerGID
				}
			}
		}
		if blocked, _ := resetBlocks(); blocked {
			// Exit 0, so systemd neither restarts it nor spends its start limit; the reset starts it on the fresh state.
			fmt.Fprintln(stderr, "a factory reset is in progress; the engine starts when it has the fresh state")
			return 0
		}
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

func readWebPeer(path string) (uint64, uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, 0, errors.New("web peer file must contain uid and gid")
	}
	uid, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil || uid == 0 {
		return 0, 0, errors.New("web peer uid must be a non-root 32-bit id")
	}
	gid, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil || gid == 0 {
		return 0, 0, errors.New("web peer gid must be a non-root 32-bit id")
	}
	return uid, gid, nil
}

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

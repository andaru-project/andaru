package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/andaru-project/andaru/internal/engine"
)

const defaultSocket = "/run/andaru/engine.sock"
const responseLimit = 8 * 1024 * 1024

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	usage := "usage: andaru engine [--socket PATH] [--state-dir PATH] | andaru ctl [--socket PATH] links|openapi"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, usage)
		return 0
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
	if flags.NArg() != 1 || (flags.Arg(0) != "links" && flags.Arg(0) != "openapi") {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	operation := flags.Arg(0)
	path := "/api/v1/status/links"
	if operation == "openapi" {
		path = "/api/v1/openapi.json"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", *socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	response, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(body) > responseLimit {
		fmt.Fprintln(stderr, "engine response could not be read within its size limit")
		return 1
	}
	if response.StatusCode != http.StatusOK {
		if utf8.Valid(body) && json.Valid(body) {
			fmt.Fprintln(stderr, string(body))
		} else {
			fmt.Fprintf(stderr, "engine returned HTTP %d\n", response.StatusCode)
		}
		return 1
	}
	if !utf8.Valid(body) || !validEnvelope(operation, body) {
		fmt.Fprintln(stderr, "engine returned an invalid "+operation+" response")
		return 1
	}
	if n, err := stdout.Write(body); err != nil || n != len(body) {
		fmt.Fprintln(stderr, "could not write engine response")
		return 1
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		if _, err := io.WriteString(stdout, "\n"); err != nil {
			fmt.Fprintln(stderr, "could not finish engine response")
			return 1
		}
	}
	return 0
}

func validEnvelope(operation string, body []byte) bool {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil || document == nil {
		return false
	}
	if operation == "links" {
		var items []json.RawMessage
		return json.Unmarshal(document["items"], &items) == nil && items != nil
	}
	var version string
	var paths map[string]json.RawMessage
	return json.Unmarshal(document["openapi"], &version) == nil && version != "" && json.Unmarshal(document["paths"], &paths) == nil && paths != nil
}

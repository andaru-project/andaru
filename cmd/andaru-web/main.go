package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	andaruweb "github.com/andaru-project/andaru/internal/web"
)

const usage = "usage: andaru-web --listen ADDR --tls-cert FILE --tls-key FILE --origin HTTPS_ORIGIN [--origin HTTPS_ORIGIN ...] --backend-socket PATH --assets-dir DIR"

type originFlags []string

func (f *originFlags) String() string { return strings.Join(*f, ",") }
func (f *originFlags) Set(value string) error {
	if value == "" {
		return errors.New("origin cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("andaru-web", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	var origins originFlags
	listen := flags.String("listen", "", "explicit non-wildcard IP address and port")
	certificate := flags.String("tls-cert", "", "absolute TLS certificate path")
	key := flags.String("tls-key", "", "absolute TLS private key path")
	backend := flags.String("backend-socket", "", "absolute dedicated engine web socket path")
	assets := flags.String("assets-dir", "", "absolute prebuilt static asset directory")
	flags.Var(&origins, "origin", "exact HTTPS management origin; repeat for each origin")
	flags.Usage = func() { fmt.Fprintln(flags.Output(), usage) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, usage)
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *listen == "" || *certificate == "" || *key == "" || *backend == "" || *assets == "" || len(origins) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err := andaruweb.Serve(ctx, andaruweb.Config{
		ListenAddress: *listen, TLSCertificate: *certificate, TLSKey: *key, Origins: []string(origins),
		BackendSocket: *backend, AssetsDirectory: *assets,
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

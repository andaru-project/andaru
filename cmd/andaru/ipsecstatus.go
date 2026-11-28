package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/andaru-project/andaru/internal/generation"
)

var ipsecStatusFilename = regexp.MustCompile(`^ipsec-status-[A-Za-z0-9._-]+\.json$`)

func ipsecStatusVerb(ctx context.Context, stateDir, outPath string, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "realize requires root")
		return 1
	}
	if err := writeIPsecStatus(ctx, stateDir, outPath, runBounded); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// writeIPsecStatus replaces outPath, an ipsec-status-*.json file directly inside stateDir, with strongSwan's state.
func writeIPsecStatus(ctx context.Context, stateDir, outPath string, run func(context.Context, string, ...string) ([]byte, error)) error {
	return writeStatusFile(stateDir, outPath, ipsecStatusFilename, func() (any, error) { return generation.ReadIPsecStatus(ctx, run) })
}

// ipsecControlVerb initiates or terminates one connection. It takes no lock: strongSwan's state is all it changes.
func ipsecControlVerb(ctx context.Context, action, name string, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "realize requires root")
		return 1
	}
	if err := generation.ControlIPsec(ctx, runBounded, action, name); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

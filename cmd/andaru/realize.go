package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
)

const realizeUsage = "usage: andaru realize [--state-dir PATH] activate N | rollback N REASON | confirm N | boot | leases | prefixes"

var generationID = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

type realizeVerbs interface {
	Activate(ctx context.Context, id int) error
	Rollback(ctx context.Context, id int, reason string) error
	Confirm(ctx context.Context, id int) error
	Boot(ctx context.Context) error
	SyncLeases(ctx context.Context) error
	SyncPrefixes(ctx context.Context) error
}

// newRealizer builds the privileged actor for the real host.
func newRealizer(stateDir string) (realizeVerbs, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("realize requires root")
	}
	return generation.Realizer{
		StateDir: stateDir,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// One stuck tool must not hold the generations lock.
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, name, args...).Output()
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return out, fmt.Errorf("%s %v: %w: %s", name, args, err, exit.Stderr)
			}
			return out, err
		},
		Chown: os.Chown, Now: time.Now,
		VerifyWait: 30 * time.Second, Poll: time.Second, LockWait: 70 * time.Second, LeaseSettle: time.Second, DeprecateAfter: 5 * time.Second,
	}, nil
}

// realizeCommand runs one privileged verb. Usage is checked before privilege so a typo never looks like a permission problem.
func realizeCommand(ctx context.Context, args []string, stderr io.Writer, build func(stateDir string) (realizeVerbs, error)) int {
	flags := flag.NewFlagSet("realize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", "/var/lib/andaru", "absolute private state directory")
	usage := func() int { fmt.Fprintln(stderr, realizeUsage); return 2 }
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 || !filepath.IsAbs(*stateDir) || filepath.Clean(*stateDir) != *stateDir {
		return usage()
	}
	verb, rest := flags.Arg(0), flags.Args()[1:]
	want := map[string]int{"activate": 1, "rollback": 2, "confirm": 1, "boot": 0, "leases": 0, "prefixes": 0}
	if n, known := want[verb]; !known || len(rest) != n || (n > 0 && !generationID.MatchString(rest[0])) {
		return usage()
	}
	id := 0
	if len(rest) > 0 {
		id, _ = strconv.Atoi(rest[0])
	}
	verbs, err := build(*stateDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch verb {
	case "activate":
		err = verbs.Activate(ctx, id)
	case "rollback":
		err = verbs.Rollback(ctx, id, rest[1])
	case "confirm":
		err = verbs.Confirm(ctx, id)
	case "boot":
		err = verbs.Boot(ctx)
	case "leases":
		err = verbs.SyncLeases(ctx)
	case "prefixes":
		err = verbs.SyncPrefixes(ctx)
	}
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, err)
	// A verb that found nothing to do (a late timer) succeeded; boot must never hold up the boot.
	if errors.Is(err, generation.ErrWrongState) || verb == "boot" {
		return 0
	}
	return 1
}

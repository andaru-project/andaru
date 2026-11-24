//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/andaru-project/andaru/internal/reset"
	"github.com/andaru-project/andaru/internal/update"
)

func updateVerb(ctx context.Context, verb, stateDir string, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "realize requires root")
		return 1
	}
	mgr := update.Manager{
		StateDir: stateDir,
		Run:      update.DefaultCommandRunner,
		CheckIdle: func(ctx context.Context) error {
			return reset.CheckApplyIdle(ctx, stateDir, update.DefaultCommandRunner)
		},
	}
	var err error
	switch verb {
	case "update-check":
		_, err = mgr.Check(ctx)
	case "update-stage":
		_, err = mgr.Stage(ctx)
	case "update-rollback":
		_, err = mgr.Rollback(ctx)
	case "update-reboot":
		err = mgr.Reboot(ctx)
	default:
		err = fmt.Errorf("unknown update verb %q", verb)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

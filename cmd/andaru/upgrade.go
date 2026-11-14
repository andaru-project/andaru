package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/andaru-project/andaru/internal/upgrade"
)

func upgradeCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "check" {
		fmt.Fprintln(stderr, "usage: andaru upgrade check")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "upgrade commands require root")
		return 1
	}
	result, err := upgrade.Check(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

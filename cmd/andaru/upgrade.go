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
	if len(args) != 1 || args[0] != "check" && args[0] != "prepare" {
		fmt.Fprintln(stderr, "usage: andaru upgrade check|prepare")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "upgrade commands require root")
		return 1
	}
	var result any
	var err error
	if args[0] == "check" {
		result, err = upgrade.Check(ctx)
	} else {
		result, err = upgrade.Prepare(ctx)
	}
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

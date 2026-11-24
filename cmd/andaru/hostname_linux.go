//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/andaru-project/andaru/internal/engine"
)

func setHostnameVerb(ctx context.Context, raw string, stderr io.Writer) int {
	name, err := engine.ValidateHostname(raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/hostnamectl", "set-hostname", "--static", "--", name).CombinedOutput()
	if err != nil {
		fmt.Fprintf(stderr, "hostnamectl set-hostname: %v: %s\n", err, strings.TrimSpace(string(out)))
		return 1
	}
	return 0
}

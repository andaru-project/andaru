//go:build !linux

package main

import (
	"context"
	"fmt"
	"io"
)

func updateVerb(_ context.Context, verb, _ string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "%s is only supported on Linux\n", verb)
	return 1
}

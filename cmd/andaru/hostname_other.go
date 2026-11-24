//go:build !linux

package main

import (
	"context"
	"fmt"
	"io"
)

func setHostnameVerb(_ context.Context, _ string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "set-hostname is only supported on Linux")
	return 1
}

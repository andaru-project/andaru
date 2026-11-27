//go:build !linux

package addon

import (
	"context"
	"os"
)

func lockFile(ctx context.Context, _ *os.File) error { return ctx.Err() }

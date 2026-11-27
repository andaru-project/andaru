//go:build linux

package addon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func lockFile(ctx context.Context, f *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for staged lock: %w", err)
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for staged lock: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

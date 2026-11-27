package engine

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
)

const (
	addonsHoldRetryDelay     = 5 * time.Second
	addonsHoldPendingTimeout = 15 * time.Second
	addonsHoldReleaseTimeout = 60 * time.Second
)

// addonsHold watches the fail-closed /run/andaru-boot/addons-held flag and releases it through realize once the saved
// configuration has no pending upgrade decisions (at startup, after answering upgrade questions, or after restoring
// a backup that clears them).
type addonsHold struct {
	root       string
	pending    func(context.Context) (bool, error)
	release    func(context.Context) error
	retryDelay time.Duration
	wake       chan struct{}
}

func newAddonsHold(root string, pending func(context.Context) (bool, error), release func(context.Context) error) *addonsHold {
	return &addonsHold{
		root:       root,
		pending:    pending,
		release:    release,
		retryDelay: addonsHoldRetryDelay,
		wake:       make(chan struct{}, 1),
	}
}

func (h *addonsHold) nudge() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *addonsHold) run(ctx context.Context) {
	h.runWithInitial(ctx, h.check(ctx))
}

func (h *addonsHold) runWithInitial(ctx context.Context, initialRetry bool) {
	var retry *time.Timer
	var retryCh <-chan time.Time
	schedule := func(needRetry bool) {
		if retry != nil {
			retry.Stop()
			retry = nil
			retryCh = nil
		}
		if needRetry {
			delay := h.retryDelay
			if delay <= 0 {
				delay = addonsHoldRetryDelay
			}
			retry = time.NewTimer(delay)
			retryCh = retry.C
		}
	}
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()
	schedule(initialRetry)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
			schedule(h.check(ctx))
		case <-retryCh:
			schedule(h.check(ctx))
		}
	}
}

func (h *addonsHold) check(ctx context.Context) (retry bool) {
	if _, err := os.Lstat(h.root + generation.AddonsHeldFile); errors.Is(err, os.ErrNotExist) {
		return false
	}
	pendingCtx, cancelPending := context.WithTimeout(ctx, addonsHoldPendingTimeout)
	needDecision, err := h.pending(pendingCtx)
	cancelPending()
	if err != nil {
		slog.Warn("safe mode: check pending upgrade decisions before releasing add-on hold", "error", err)
		return true
	}
	if needDecision {
		return false
	}
	releaseCtx, cancelRelease := context.WithTimeout(ctx, addonsHoldReleaseTimeout)
	defer cancelRelease()
	if err := h.release(releaseCtx); err != nil {
		slog.Warn("safe mode: release add-on hold", "error", err)
		return true
	}
	return false
}

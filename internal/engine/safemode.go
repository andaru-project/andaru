package engine

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
)

// addonsHold watches the fail-closed /run/andaru/addons-held flag and releases it through realize once the saved
// configuration has no pending upgrade decisions (at startup, after answering upgrade questions, or after restoring
// a backup that clears them).
type addonsHold struct {
	root     string
	stateDir string
	pending  func(context.Context) (bool, error)
	release  func(context.Context) error
	wake     chan struct{}
}

func newAddonsHold(root, stateDir string, pending func(context.Context) (bool, error), release func(context.Context) error) *addonsHold {
	return &addonsHold{
		root:     root,
		stateDir: stateDir,
		pending:  pending,
		release:  release,
		wake:     make(chan struct{}, 1),
	}
}

func (h *addonsHold) nudge() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *addonsHold) run(ctx context.Context) {
	h.check(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
			h.check(ctx)
		}
	}
}

func (h *addonsHold) check(ctx context.Context) {
	status := generation.ReadSafeMode(h.root, h.stateDir)
	if !slices.Contains(status.Reasons, "settingsHold") {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	needDecision, err := h.pending(checkCtx)
	if err != nil {
		slog.Warn("safe mode: check pending upgrade decisions before releasing add-on hold", "error", err)
		return
	}
	if needDecision {
		return
	}
	if err := h.release(checkCtx); err != nil {
		slog.Warn("safe mode: release add-on hold", "error", err)
	}
}

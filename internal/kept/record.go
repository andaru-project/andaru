package kept

import (
	"context"
	"log/slog"
	"strings"

	"github.com/andaru-project/andaru/internal/authz"
)

// RecordLiveLAN stores the closure of the configuration that was already live.
// The caller computes that closure with the store it already has open.
func RecordLiveLAN(ctx context.Context, root, stateDir string, actor authz.Actor, body []byte, closureErr error) error {
	if actor.Kind != authz.ActorAccount && !(actor.Kind == authz.ActorSystem && actor.ID == "root") {
		return nil
	}
	if closureErr != nil {
		if strings.Contains(closureErr.Error(), "UPLINK_MANAGEMENT") || strings.Contains(closureErr.Error(), "NO_MANAGEMENT") {
			return nil
		}
		return closureErr
	}
	return put(ctx, root, stateDir, "last-lan", body, true)
}

// CopyFirst writes the first LAN once. An existing copy is left unchanged.
func CopyFirst(ctx context.Context, root, stateDir string, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	return put(ctx, root, stateDir, "first", body, false)
}

func put(ctx context.Context, root, stateDir string, name Name, body []byte, replace bool) error {
	store, err := openAt(root, stateDir, root == Root)
	if err != nil {
		return err
	}
	err = store.Put(ctx, name, body, replace)
	if err != nil && !replace && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// Log records a kept-copy failure without failing the save or confirm that caused it.
func Log(err error) {
	if err != nil {
		slog.Error("kept copy", "error", err)
	}
}

package kept

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
)

var errUplink = errors.New("UPLINK_MANAGEMENT")

var extractLAN = extractFromStore

func extractFromStore(ctx context.Context, stateDir, version string) ([]byte, error) {
	if fn, ok := ctx.Value(extractorKey{}).(func(context.Context, string, string) ([]byte, error)); ok {
		return fn(ctx, stateDir, version)
	}
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.ManagementLAN(ctx, version)
}

type extractorKey struct{}

// WithExtractor lets a caller that already holds the state lock supply the closure.
func WithExtractor(ctx context.Context, fn func(context.Context, string, string) ([]byte, error)) context.Context {
	return context.WithValue(ctx, extractorKey{}, fn)
}

func RecordLastLAN(ctx context.Context, stateDir, savedVersion string, actor authz.Actor) error {
	if actor.Kind != authz.ActorAccount && !(actor.Kind == authz.ActorSystem && actor.ID == "root") {
		return nil
	}
	live, err := liveSavedVersion(stateDir)
	if err != nil || live != savedVersion {
		return err
	}
	return putClosure(ctx, stateDir, "last-lan", savedVersion, true)
}

func RecordFirst(ctx context.Context, stateDir, savedVersion string) error {
	return putClosure(ctx, stateDir, "first", savedVersion, false)
}

func putClosure(ctx context.Context, stateDir, name, version string, replace bool) error {
	body, err := extractLAN(ctx, stateDir, version)
	if err != nil {
		if strings.Contains(err.Error(), "UPLINK_MANAGEMENT") || strings.Contains(err.Error(), "NO_MANAGEMENT") {
			return nil
		}
		return err
	}
	store, err := Open(stateDir)
	if err != nil {
		return err
	}
	err = store.Put(ctx, Name(name), body, replace)
	if err != nil && !replace && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

func liveSavedVersion(stateDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, "generations", "pointers.json"))
	if err != nil {
		return "", err
	}
	var pointers struct {
		Live *int `json:"live"`
	}
	if err = json.Unmarshal(raw, &pointers); err != nil || pointers.Live == nil {
		return "", err
	}
	raw, err = os.ReadFile(filepath.Join(stateDir, "generations", itoa(*pointers.Live), "manifest.json"))
	if err != nil {
		return "", err
	}
	var manifest struct {
		SavedVersion string `json:"savedVersion"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return "", err
	}
	return manifest.SavedVersion, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

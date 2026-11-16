package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

var errLiveAuthorizationUnavailable = errors.New("live authorization is unavailable")

func initializeAuthorizationState(ctx context.Context, stateDir string) error {
	return generation.Observe(ctx, generation.Dir(stateDir), func(observed generation.Observation) error {
		digest := ""
		if observed.Live != nil {
			live, err := generation.Read(generation.Dir(stateDir), *observed.Live)
			if err != nil {
				return err
			}
			digest = live.AuthorizationDigest
		}
		lock, err := authz.Acquire(ctx, stateDir)
		if err != nil {
			return err
		}
		defer lock.Close()
		state, err := lock.Read()
		if err == nil {
			if state.LiveDigest != digest {
				return fmt.Errorf("%w: live authorization digest mismatch", errLiveAuthorizationUnavailable)
			}
			return nil
		}
		if !errors.Is(err, authz.ErrStateUnavailable) {
			return err
		}
		_, err = lock.Advance(digest)
		return err
	})
}

// LiveAuthorization returns the role snapshot named by Pointers.Live and its
// monotonic epoch. It acquires the authorization lock; callers must not already hold it.
func LiveAuthorization(ctx context.Context, stateDir string, configuration *config.Store) (authz.Snapshot, uint64, error) {
	if configuration == nil {
		return authz.Snapshot{}, 0, errLiveAuthorizationUnavailable
	}
	lock, err := authz.Acquire(ctx, stateDir)
	if err != nil {
		return authz.Snapshot{}, 0, err
	}
	defer lock.Close()
	return liveAuthorizationLocked(ctx, stateDir, configuration, lock)
}

func liveRoleSnapshot(ctx context.Context, stateDir string, configuration *config.Store) (authz.Snapshot, generation.Generation, error) {
	if configuration == nil {
		return authz.Snapshot{}, generation.Generation{}, errLiveAuthorizationUnavailable
	}
	pointers, err := generation.ReadPointers(generation.Dir(stateDir))
	if err != nil || pointers.Live == nil {
		return authz.Snapshot{}, generation.Generation{}, errors.Join(errLiveAuthorizationUnavailable, err)
	}
	live, err := generation.Read(generation.Dir(stateDir), *pointers.Live)
	if err != nil {
		return authz.Snapshot{}, generation.Generation{}, errors.Join(errLiveAuthorizationUnavailable, err)
	}
	version := config.Version(live.SavedVersion)
	saved, err := configuration.Version(ctx, version)
	if err != nil {
		return authz.Snapshot{}, generation.Generation{}, errors.Join(errLiveAuthorizationUnavailable, err)
	}
	var declaration struct {
		Roles []authz.Role `json:"roles"`
	}
	if err = json.Unmarshal(saved.Value, &declaration); err != nil {
		return authz.Snapshot{}, generation.Generation{}, errors.Join(errLiveAuthorizationUnavailable, err)
	}
	custom := make([]authz.Role, 0, len(declaration.Roles))
	for _, role := range declaration.Roles {
		if role.BuiltIn == "" {
			custom = append(custom, role)
		}
	}
	snapshot, err := authz.NewSnapshot(custom)
	if err != nil || snapshot.Digest != live.AuthorizationDigest {
		return authz.Snapshot{}, generation.Generation{}, errors.Join(errLiveAuthorizationUnavailable, err)
	}
	return snapshot, live, nil
}

func liveAuthorizationLocked(ctx context.Context, stateDir string, configuration *config.Store, lock *authz.StateLock) (authz.Snapshot, uint64, error) {
	snapshot, live, err := liveRoleSnapshot(ctx, stateDir, configuration)
	if err != nil {
		return authz.Snapshot{}, 0, err
	}
	state, err := lock.Read()
	if err != nil {
		return authz.Snapshot{}, 0, errors.Join(errLiveAuthorizationUnavailable, err)
	}
	if state.LiveDigest != live.AuthorizationDigest {
		return authz.Snapshot{}, 0, fmt.Errorf("%w: live authorization digest mismatch", errLiveAuthorizationUnavailable)
	}
	return snapshot, state.AuthorizationEpoch, nil
}

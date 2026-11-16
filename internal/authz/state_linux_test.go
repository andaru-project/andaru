package authz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func privateStateDir(t *testing.T) string {
	t.Helper()
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

func TestAuthorizationStateAdvancesEveryDifferentDigest(t *testing.T) {
	stateDir := privateStateDir(t)
	a, err := Digest(nil)
	if err != nil {
		t.Fatal(err)
	}
	b := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	lock, err := Acquire(t.Context(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	state, err := lock.Advance(a)
	if err != nil || state.LiveDigest != a || state.AuthorizationEpoch != 1 {
		t.Fatalf("initial state = %+v, error=%v", state, err)
	}
	state, err = lock.Advance(a)
	if err != nil || state.AuthorizationEpoch != 1 {
		t.Fatalf("unchanged state = %+v, error=%v", state, err)
	}
	state, err = lock.Advance(b)
	if err != nil || state.AuthorizationEpoch != 2 {
		t.Fatalf("A to B state = %+v, error=%v", state, err)
	}
	state, err = lock.Advance(a)
	if err != nil || state.LiveDigest != a || state.AuthorizationEpoch != 3 {
		t.Fatalf("A to B to A state = %+v, error=%v", state, err)
	}
}

func TestAuthorizationLockAndPendingTransitionAreCrossProcessState(t *testing.T) {
	stateDir := privateStateDir(t)
	digest, err := Digest(nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Acquire(t.Context(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	pending := PendingTransition{GenerationID: 7, Phase: TransitionActivate, IdentityRevision: 11, AuthorizationDigest: digest}
	if err = first.SetPending(pending); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := first.Pending(); err != nil || !ok || got != pending {
		t.Fatalf("pending = %+v, found=%t, error=%v", got, ok, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err = Acquire(ctx, stateDir); err == nil {
		t.Fatal("second authorization lock did not wait")
	}
	if err = first.ClearPending(7, TransitionActivate); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := first.Pending(); err != nil || ok {
		t.Fatalf("pending survived clear: found=%t error=%v", ok, err)
	}
}

func TestMissingAuthorizationStateDoesNotResetEpoch(t *testing.T) {
	stateDir := privateStateDir(t)
	digest, err := Digest(nil)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(t.Context(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lock.Advance(digest); err != nil {
		t.Fatal(err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(stateDir, authorizationStateName)); err != nil {
		t.Fatal(err)
	}
	lock, err = Acquire(t.Context(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err = lock.Advance(digest); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("missing state reset the epoch: %v", err)
	}
}

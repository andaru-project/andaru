//go:build integration

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

func TestLiveAuthorizationUsesPointersLiveNotSavedHead(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned stores require root")
	}
	ctx := context.Background()
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	firstChange, err := config.ObjectChange("create", "roles", "", nil, []byte(`{"name":"Live operator","capabilities":["configuration.confirm"]}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Save(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, firstChange)
	if err != nil {
		t.Fatal(err)
	}
	liveInput, err := store.RealizationInput(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := generation.Create(generation.Dir(stateDir), generation.Manifest{
		SavedVersion: string(first.SavedVersion), AuthorizationDigest: liveInput.Authorization.Digest,
		IdentityRevision: 1, ConfirmTimeoutSeconds: 120,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(generation.Dir(stateDir), "pointers.json"), fmt.Appendf(nil, `{"confirmed":%d,"lastKnownGood":null,"pending":null,"live":%d,"takeover":false}`, id, id), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := authz.Acquire(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := lock.Advance(liveInput.Authorization.Digest)
	if closeErr := lock.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	secondChange, err := config.ObjectChange("create", "roles", "", nil, []byte(`{"name":"Saved only","capabilities":["users.manage"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, secondChange); err != nil {
		t.Fatal(err)
	}
	snapshot, epoch, err := LiveAuthorization(ctx, stateDir, store)
	if err != nil {
		t.Fatal(err)
	}
	if epoch != state.AuthorizationEpoch || snapshot.Digest != liveInput.Authorization.Digest || len(snapshot.Roles) != 1 || snapshot.Roles[0].Name != "Live operator" {
		t.Fatalf("live authorization = %+v epoch=%d", snapshot, epoch)
	}
	api := identityAPI{configuration: store, coordinator: &securityCoordinator{stateDir: stateDir}}
	roles, err := api.listRoles(ctx, false)
	if err != nil || len(roles) != 3 {
		t.Fatalf("live role API = %+v error=%v", roles, err)
	}
	for _, role := range roles {
		if role.Name == "Saved only" {
			t.Fatal("saved-head role leaked into live role API")
		}
	}
}

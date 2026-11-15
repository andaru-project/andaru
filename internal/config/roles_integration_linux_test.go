//go:build integration

package config

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestRoleHistoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ownedDir(t))
	change, err := ObjectChange("create", "roles", "", nil, []byte(`{"name":"Operator","capabilities":["configuration.write","status.read"]}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Save(ctx, "unix:uid:0", change)
	if err != nil {
		t.Fatal(err)
	}
	id := result.Created["object"]
	roles, err := store.List(ctx, "roles")
	if err != nil || len(roles.Items) != 3 {
		t.Fatalf("role list did not include built-ins and custom role: %+v %v", roles, err)
	}
	var custom authz.Role
	if err = json.Unmarshal(roles.Items[2].Value, &custom); err != nil || custom.ID != id || custom.BuiltIn != "" {
		t.Fatalf("wrong custom role view: %+v %v", custom, err)
	}
	version, err := store.Version(ctx, result.SavedVersion)
	if err != nil || len(version.Changes) != 1 || version.Changes[0].Collection != "roles" || version.Changes[0].ID != id || !version.Changes[0].Created {
		t.Fatalf("role save was not retained as ordinary history: %+v %v", version, err)
	}
	var declarationView struct {
		SchemaVersion int          `json:"schemaVersion"`
		Roles         []authz.Role `json:"roles"`
	}
	if err = json.Unmarshal(version.Value, &declarationView); err != nil || declarationView.SchemaVersion != 3 || len(declarationView.Roles) != 3 {
		t.Fatalf("history role view did not round-trip: %+v %v", declarationView, err)
	}
}

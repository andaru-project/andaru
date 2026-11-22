//go:build integration

package config

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestRoleLifecycleHistory(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ownedDir(t))
	change, err := ObjectChange("create", "roles", "", nil, []byte(`{"name":"Operator","capabilities":["configuration.write","status.read"]}`))
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Save(ctx, systemRootActor, change)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Created["object"]
	roles, err := store.List(ctx, "roles")
	if err != nil || len(roles.Items) != 3 {
		t.Fatalf("role list did not include built-ins and custom role: %+v %v", roles, err)
	}
	var custom authz.Role
	if err = json.Unmarshal(roles.Items[2].Value, &custom); err != nil || custom.ID != id || custom.BuiltIn != "" {
		t.Fatalf("wrong custom role view: %+v %v", custom, err)
	}

	roleAt := func(saved Version) (authz.Role, bool) {
		t.Helper()
		historical, err := store.Version(ctx, saved)
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			SchemaVersion int          `json:"schemaVersion"`
			Roles         []authz.Role `json:"roles"`
		}
		if err = json.Unmarshal(historical.Value, &view); err != nil || view.SchemaVersion != schemaVersion {
			t.Fatalf("invalid historical role view at %s: %+v %v", saved, view, err)
		}
		for _, role := range view.Roles {
			if role.ID == id {
				return role, true
			}
		}
		return authz.Role{}, false
	}
	if historical, ok := roleAt(created.SavedVersion); !ok || historical.Name != "Operator" || len(historical.Capabilities) != 2 {
		t.Fatalf("create history lost role value: %+v %v", historical, ok)
	}
	createdVersion, err := store.Version(ctx, created.SavedVersion)
	if err != nil || len(createdVersion.Changes) != 1 || createdVersion.Changes[0].Collection != "roles" || createdVersion.Changes[0].ID != id || !createdVersion.Changes[0].Created {
		t.Fatalf("role create was not retained as ordinary history: %+v %v", createdVersion, err)
	}

	createVersion := created.SavedVersion
	replace, err := ObjectChange("replace", "roles", id, &createVersion, []byte(`{"name":"Auditor","description":"Read configuration","capabilities":["configuration.read"]}`))
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := store.Save(ctx, systemRootActor, replace)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ObjectChange("replace", "roles", id, &createVersion, []byte(`{"name":"Stale write","capabilities":["status.read"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(ctx, systemRootActor, stale); err == nil {
		t.Fatal("stale role replacement succeeded")
	} else {
		fault := requireFault(t, err, "PRECONDITION_FAILED")
		if fault.Current == nil || fault.Current.Version != replaced.SavedVersion || fault.Current.Deleted {
			t.Fatalf("stale replacement did not return the current role version: %+v", fault.Current)
		}
	}
	current, head, err := store.Get(ctx, "roles", id)
	if err != nil || current.Version != replaced.SavedVersion || head != replaced.SavedVersion {
		t.Fatalf("stale replacement changed role/version: %+v %s %v", current, head, err)
	}
	if err = json.Unmarshal(current.Value, &custom); err != nil || custom.Name != "Auditor" || len(custom.Capabilities) != 1 || custom.Capabilities[0] != authz.ConfigurationRead {
		t.Fatalf("replacement value was not retained: %+v %v", custom, err)
	}
	if historical, ok := roleAt(replaced.SavedVersion); !ok || historical.Name != "Auditor" || historical.Description != "Read configuration" {
		t.Fatalf("replace history lost role value: %+v %v", historical, ok)
	}

	replaceVersion := replaced.SavedVersion
	remove, err := ObjectChange("delete", "roles", id, &replaceVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Save(ctx, systemRootActor, remove)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Get(ctx, "roles", id); err == nil {
		t.Fatal("deleted role remains current")
	} else {
		requireFault(t, err, "NOT_FOUND")
	}
	if roles, err = store.List(ctx, "roles"); err != nil || len(roles.Items) != 2 {
		t.Fatalf("deleted role remains in list: %+v %v", roles, err)
	}
	if historical, ok := roleAt(created.SavedVersion); !ok || historical.Name != "Operator" || len(historical.Capabilities) != 2 ||
		historical.Capabilities[0] != authz.ConfigurationWrite || historical.Capabilities[1] != authz.StatusRead {
		t.Fatalf("later mutations changed create history: %+v %v", historical, ok)
	}
	if historical, ok := roleAt(replaced.SavedVersion); !ok || historical.Name != "Auditor" || historical.Description != "Read configuration" ||
		len(historical.Capabilities) != 1 || historical.Capabilities[0] != authz.ConfigurationRead {
		t.Fatalf("deletion changed replacement history: %+v %v", historical, ok)
	}
	if historical, ok := roleAt(deleted.SavedVersion); ok {
		t.Fatalf("delete history snapshot retained current role: %+v", historical)
	}
	deletedVersion, err := store.Version(ctx, deleted.SavedVersion)
	if err != nil || len(deletedVersion.Changes) != 1 || deletedVersion.Changes[0].ID != id || !deletedVersion.Changes[0].Deleted {
		t.Fatalf("role deletion was not retained as ordinary history: %+v %v", deletedVersion, err)
	}
	revive, err := ObjectChange("replace", "roles", id, &replaceVersion, []byte(`{"name":"Revived","capabilities":["status.read"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(ctx, systemRootActor, revive); err == nil {
		t.Fatal("replacement with pre-delete version revived deleted role")
	} else {
		fault := requireFault(t, err, "PRECONDITION_FAILED")
		if fault.Current == nil || fault.Current.Version != deleted.SavedVersion || !fault.Current.Deleted {
			t.Fatalf("deleted role did not expose its tombstone version: %+v", fault.Current)
		}
	}
}

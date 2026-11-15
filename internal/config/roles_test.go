package config

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestSchema3RolesCanonicalAndCompatible(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("{\"links\":[],\"schemaVersion\":1,\"segments\":[],\"zones\":[]}\n"),
		[]byte("{\"links\":[],\"schemaVersion\":2,\"segments\":[],\"zones\":[]}\n"),
	} {
		d, err := decodeDeclaration(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := canonical(d)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("legacy canonical bytes changed: %v\nwant %s\n got %s", err, raw, got)
		}
	}

	raw := []byte(`{"schemaVersion":3,"links":[],"segments":[],"zones":[],"roles":[{"id":"rol_00000000-0000-4000-8000-000000000001","name":"Operator","capabilities":["status.read","configuration.read"]}]}`)
	d, err := decodeDeclaration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Roles) != 1 || !slices.Equal(d.Roles[0].Capabilities, []authz.Capability{authz.ConfigurationRead, authz.StatusRead}) {
		t.Fatalf("roles were not canonically ordered: %+v", d.Roles)
	}
	canonicalBody, err := canonical(d)
	if err != nil || !bytes.Contains(canonicalBody, []byte(`"capabilities":["configuration.read","status.read"]`)) {
		t.Fatalf("canonical role bytes are not sorted: %v %s", err, canonicalBody)
	}
}

func TestSchema3RoleRefusesBuiltInsAndUnknownCapabilities(t *testing.T) {
	for name, role := range map[string]string{
		"built-in id":   `{"id":"administrator","name":"Custom","capabilities":["status.read"]}`,
		"built-in name": `{"id":"rol_00000000-0000-4000-8000-000000000002","name":"Administrator","capabilities":["status.read"]}`,
		"unknown":       `{"id":"rol_00000000-0000-4000-8000-000000000003","name":"Custom","capabilities":["unknown.read"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(`{"schemaVersion":3,"links":[],"segments":[],"zones":[],"roles":[` + role + `]}`)
			if _, err := decodeDeclaration(raw); err == nil {
				t.Fatal("accepted invalid role declaration")
			}
		})
	}
	if _, err := decodeDeclaration([]byte(`{"schemaVersion":1,"links":[],"segments":[],"zones":[],"roles":[]}`)); err == nil {
		t.Fatal("schema 1 accepted serialized roles")
	}
}

func TestRoleViewsAndChanges(t *testing.T) {
	state := initialState()
	view, err := publicSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		SchemaVersion int          `json:"schemaVersion"`
		Roles         []authz.Role `json:"roles"`
	}
	if err = json.Unmarshal(view.Value, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 || len(snapshot.Roles) != 2 || snapshot.Roles[0].BuiltIn != authz.AdministratorRoleID || snapshot.Roles[1].BuiltIn != authz.ReadOnlyRoleID {
		t.Fatalf("legacy view did not synthesize immutable built-ins: %+v", snapshot)
	}

	change, err := ObjectChange("create", "roles", "", nil, []byte(`{"name":"Operator","description":"Saved role","capabilities":["status.read","configuration.write"]}`))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareChanges(&state, change, "1", fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State.Config.SchemaVersion != 3 || len(prepared.State.Config.Roles) != 1 || prepared.State.Versions[objectKey{"roles", prepared.State.Config.Roles[0].ID}].Version != "1" {
		t.Fatalf("role edit was not an ordinary schema-3 configuration change: %+v", prepared)
	}
	if prepared.State.Config.Roles[0].BuiltIn != "" || len(prepared.Changed) != 1 || prepared.Changed[0].Collection != "roles" {
		t.Fatalf("built-in or history metadata was serialized incorrectly: %+v", prepared)
	}

	bad, _ := ObjectChange("create", "roles", "", nil, []byte(`{"name":"Read-only","capabilities":["status.read"]}`))
	if _, err = prepareChanges(&state, bad, "1", fixedTime); err == nil {
		t.Fatal("change API allowed a built-in role shadow")
	}
}

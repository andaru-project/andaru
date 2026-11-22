package config

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestRolesCanonicalAndCompatible(t *testing.T) {
	for stored, raw := range map[int][]byte{
		1: []byte("{\"links\":[],\"schemaVersion\":1,\"segments\":[],\"zones\":[]}\n"),
		2: []byte("{\"links\":[],\"schemaVersion\":2,\"segments\":[],\"zones\":[]}\n"),
		3: []byte("{\"links\":[],\"roles\":[],\"schemaVersion\":3,\"segments\":[],\"zones\":[]}\n"),
	} {
		d, was, _, err := decodeSaved(raw)
		if err != nil || was != stored {
			t.Fatalf("form %d: read as %d: %v", stored, was, err)
		}
		got, err := canonical(d)
		if want := "{\"links\":[],\"schemaVersion\":4,\"segments\":[],\"zones\":[]}\n"; err != nil || string(got) != want {
			t.Fatalf("form %d reads now as %s (%v), want %s", stored, got, err, want)
		}
	}

	raw := []byte(`{"schemaVersion":4,"links":[],"segments":[],"zones":[],"roles":[{"id":"rol_00000000-0000-4000-8000-000000000001","name":"Operator","capabilities":["status.read","configuration.read"]}]}`)
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

func TestRoleRefusesBuiltInsAndUnknownCapabilities(t *testing.T) {
	for name, role := range map[string]string{
		"built-in id":   `{"id":"administrator","name":"Custom","capabilities":["status.read"]}`,
		"built-in name": `{"id":"rol_00000000-0000-4000-8000-000000000002","name":"Administrator","capabilities":["status.read"]}`,
		"unknown":       `{"id":"rol_00000000-0000-4000-8000-000000000003","name":"Custom","capabilities":["unknown.read"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(`{"schemaVersion":4,"links":[],"segments":[],"zones":[],"roles":[` + role + `]}`)
			if _, err := decodeDeclaration(raw); err == nil {
				t.Fatal("accepted invalid role declaration")
			}
		})
	}
	if _, _, _, err := decodeSaved([]byte(`{"schemaVersion":1,"links":[],"segments":[],"zones":[],"roles":[]}`)); err == nil {
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
	if snapshot.SchemaVersion != schemaVersion || len(snapshot.Roles) != 2 || snapshot.Roles[0].BuiltIn != authz.AdministratorRoleID || snapshot.Roles[1].BuiltIn != authz.ReadOnlyRoleID {
		t.Fatalf("view did not synthesize immutable built-ins: %+v", snapshot)
	}

	ordinary, err := DecodeChanges([]byte(`{"operations":[{"op":"create","collection":"zones","localId":"zone","value":{"name":"Extra"}},{"op":"create","collection":"aliases.ports","localId":"service","value":{"name":"DNS","protocol":"udp","ports":["53"]}},{"op":"create","collection":"rules","localId":"rule","value":{"name":"Allow DNS","enabled":true,"action":"accept","from":{"zones":[],"addresses":[]},"to":{"zones":[],"addresses":[]},"ports":[],"log":false}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ordinaryPrepared, err := prepareChanges(&state, ordinary, "1", fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if ordinaryPrepared.State.Config.SchemaVersion != schemaVersion || ordinaryPrepared.Created["zone"] == "" || ordinaryPrepared.Created["service"] == "" || ordinaryPrepared.Created["rule"] == "" {
		t.Fatalf("ordinary operation: %+v", ordinaryPrepared)
	}
	state = ordinaryPrepared.State

	change, err := DecodeChanges([]byte(`{"operations":[{"op":"create","collection":"roles","localId":"role","value":{"name":"Operator","description":"Saved role","capabilities":["status.read","configuration.write"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareChanges(&state, change, "1", fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State.Config.SchemaVersion != schemaVersion || len(prepared.State.Config.Roles) != 1 || prepared.State.Versions[objectKey{"roles", prepared.State.Config.Roles[0].ID}].Version != "1" {
		t.Fatalf("role edit was not an ordinary configuration change: %+v", prepared)
	}
	if prepared.State.Config.Roles[0].BuiltIn != "" || prepared.Created["role"] != prepared.State.Config.Roles[0].ID || len(prepared.Changed) != 1 || prepared.Changed[0].Collection != "roles" {
		t.Fatalf("built-in or history metadata was serialized incorrectly: %+v", prepared)
	}

	bad, _ := ObjectChange("create", "roles", "", nil, []byte(`{"name":"Read-only","capabilities":["status.read"]}`))
	if _, err = prepareChanges(&state, bad, "1", fixedTime); err == nil {
		t.Fatal("change API allowed a built-in role shadow")
	}
}

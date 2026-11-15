//go:build integration

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestBackupExportScopes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, ownedDir(t))
	initial, err := s.Save(ctx, "unix:uid:0", networkInput(t))
	if err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(fmt.Sprintf(`{"name":"unused-ppp","kind":"pppoe","mtu":1492,"enabled":true,"parent":%q,"username":"TEST_ONLY","password":{"secret":"UNUSED","set":"TEST_ONLY_UNUSED","expectedVersion":"0"}}`, initial.Created["l1"]))
	create, err := ObjectChange("create", "links", "", nil, body)
	if err != nil {
		t.Fatal(err)
	}
	made, err := s.Save(ctx, "unix:uid:0", create)
	if err != nil {
		t.Fatal(err)
	}
	remove, err := ObjectChange("delete", "links", made.Created["object"], &made.SavedVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "unix:uid:0", remove); err != nil {
		t.Fatal(err)
	}
	ordinary, err := s.Export(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.SavedVersion != "3" || ordinary.ConfigurationSecrets != nil {
		t.Fatal("default export included a secret scope or wrong saved head")
	}
	for _, forbidden := range []string{"TEST_ONLY_FIRST", "TEST_ONLY_UNUSED", `"isSet"`, `"updatedAt"`, `"expectedVersion"`} {
		if bytes.Contains(ordinary.Configuration, []byte(forbidden)) {
			t.Fatalf("declaration export included %s", forbidden)
		}
	}
	exported, err := s.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(exported.Configuration, ordinary.Configuration) {
		t.Fatal("secret selection changed the declaration")
	}
	var portable struct {
		SchemaVersion int
		Secrets       struct{ Core map[string]map[string]string }
	}
	if err = json.Unmarshal(exported.ConfigurationSecrets, &portable); err != nil {
		t.Fatal(err)
	}
	if portable.SchemaVersion != 1 || portable.Secrets.Core["WAN_PASSWORD"]["value"] != "TEST_ONLY_FIRST" || portable.Secrets.Core["UNUSED"]["value"] != "TEST_ONLY_UNUSED" {
		t.Fatal("explicit export lost referenced or unused core secrets")
	}
	for _, value := range portable.Secrets.Core {
		if len(value) != 2 || value["kind"] != "password" {
			t.Fatal("portable secrets include local history metadata")
		}
	}
}

func TestBackupDocumentsRejectMalformedScopes(t *testing.T) {
	for _, input := range []RestoreDocuments{
		{},
		{Configuration: json.RawMessage(`{"schemaVersion":3,"links":[],"segments":[],"zones":[]}`)},
		{Configuration: json.RawMessage(`{"schemaVersion":1,"schemaVersion":2,"links":[],"segments":[],"zones":[]}`)},
		{ConfigurationSecrets: json.RawMessage(`{"schemaVersion":1,"secrets":{"core":{},"extension":{}}}`)},
		{ConfigurationSecrets: json.RawMessage(`{"schemaVersion":1,"secrets":{"core":{"BAD":{"kind":"password","value":"x","version":"99"}}}}`)},
		{ConfigurationSecrets: json.RawMessage(`{"schemaVersion":1,"secrets":{"core":{"BAD":{"kind":"token","value":"x"}}}}`)},
		{ConfigurationSecrets: json.RawMessage(`{"schemaVersion":1,"secrets":{"core":{"BAD":{"kind":"password","value":"\u0000"}}}}`)},
	} {
		if err := ValidateBackupDocuments(input); err == nil {
			t.Fatal("accepted malformed portable documents")
		}
	}
}

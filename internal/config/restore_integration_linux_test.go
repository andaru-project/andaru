//go:build integration

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
)

const portableEmptySecrets = `{"schemaVersion":1,"secrets":{"core":{}}}`
const portableRestoredSecret = `{"schemaVersion":1,"secrets":{"core":{"RESTORED":{"kind":"password","value":"TEST_ONLY_RESTORED"}}}}`

func TestRestoreIndependentStores(t *testing.T) {
	ctx := context.Background()
	sourceStore := openTestStore(t, ownedDir(t))
	sourceSaved, err := sourceStore.Save(ctx, "unix:uid:0", networkInput(t))
	if err != nil {
		t.Fatal(err)
	}
	source, err := sourceStore.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	root := ownedDir(t)
	destination := openTestStore(t, root)
	local, err := destination.Save(ctx, "unix:uid:0", networkInput(t))
	if err != nil {
		t.Fatal(err)
	}
	object, _, err := destination.Get(ctx, "links", local.Created["l2"])
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(object.Value, &value); err != nil {
		t.Fatal(err)
	}
	value["password"] = map[string]any{"secret": "WAN_PASSWORD", "set": "TEST_ONLY_LOCAL", "expectedVersion": "1"}
	raw, _ := json.Marshal(value)
	change, err := ObjectChange("replace", "links", local.Created["l2"], &object.Version, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = destination.Save(ctx, "unix:uid:0", change); err != nil {
		t.Fatal(err)
	}
	before, err := destination.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	input := RestoreDocuments{Configuration: source.Configuration}
	preview, err := destination.PreviewRestore(ctx, input)
	if err != nil || !slices.Contains(preview.UnresolvedSecrets, "WAN_PASSWORD") || preview.ExpectedSavedVersion != "2" {
		t.Fatal("missing unresolved preview", err)
	}
	result, err := destination.Restore(ctx, "unix:uid:0", before.SavedVersion, input)
	if err != nil || result.SavedVersion != "3" {
		t.Fatal("configuration restore did not publish one local version", err)
	}
	after, err := destination.Export(ctx, true)
	if err != nil || !bytes.Equal(after.ConfigurationSecrets, before.ConfigurationSecrets) {
		t.Fatal("unselected local secrets changed", err)
	}
	if _, err = destination.RealizationInput(ctx, nil); err == nil {
		t.Fatal("same-name local secret silently reused")
	}
	if err = destination.Close(); err != nil {
		t.Fatal(err)
	}
	destination = openTestStore(t, root)
	if _, err = destination.RealizationInput(ctx, nil); err == nil {
		t.Fatal("restart rebound unresolved reference")
	}
	object, _, err = destination.Get(ctx, "links", sourceSaved.Created["l2"])
	if err != nil {
		t.Fatal("import changed object identity", err)
	}
	if err = json.Unmarshal(object.Value, &value); err != nil {
		t.Fatal(err)
	}
	value["password"] = map[string]any{"secret": "WAN_PASSWORD"}
	raw, _ = json.Marshal(value)
	change, err = ObjectChange("replace", "links", sourceSaved.Created["l2"], &object.Version, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = destination.Save(ctx, "unix:uid:0", change); err != nil {
		t.Fatal(err)
	}
	realized, err := destination.RealizationInput(ctx, nil)
	if err != nil || realized.Secrets["WAN_PASSWORD"] != "TEST_ONLY_LOCAL" {
		t.Fatal("explicit binding failed to retain local value", err)
	}
	current, err := destination.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	paired := RestoreDocuments{Configuration: source.Configuration, ConfigurationSecrets: source.ConfigurationSecrets}
	if _, err = destination.Restore(ctx, "unix:uid:0", current.SavedVersion, paired); err != nil {
		t.Fatal(err)
	}
	realized, err = destination.RealizationInput(ctx, nil)
	if err != nil || realized.Secrets["WAN_PASSWORD"] != "TEST_ONLY_FIRST" {
		t.Fatal("paired restore lost source secret", err)
	}
}

func TestRestorePreservesCompatibleSchema(t *testing.T) {
	ctx := context.Background()
	schemaVersion := func(raw json.RawMessage) int {
		t.Helper()
		var document struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		return document.SchemaVersion
	}
	schemaTwo := func(raw json.RawMessage) json.RawMessage {
		t.Helper()
		var document declaration
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		document.SchemaVersion = 2
		body, err := canonical(document)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	restore := func(destination *Store, input RestoreDocuments, wantSchema int, wantVersion Version) RestoreResult {
		t.Helper()
		before, err := destination.Export(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		result, err := destination.Restore(ctx, "unix:uid:0", before.SavedVersion, input)
		if err != nil || result.SavedVersion != wantVersion {
			t.Fatal("restore saved the wrong version", err)
		}
		after, err := destination.Export(ctx, true)
		if err != nil || after.SavedVersion != result.SavedVersion || schemaVersion(after.Configuration) != wantSchema {
			t.Fatal("restore saved the wrong declaration schema or version", err)
		}
		return result
	}

	plainSource := openTestStore(t, ownedDir(t))
	if _, err := plainSource.Save(ctx, "unix:uid:0", createZone("portable")); err != nil {
		t.Fatal(err)
	}
	plain, err := plainSource.Export(ctx, true)
	if err != nil || schemaVersion(plain.Configuration) != 1 {
		t.Fatal("wrong compatible schema fixture", err)
	}
	plainDestination := openTestStore(t, ownedDir(t))
	restore(plainDestination, RestoreDocuments{Configuration: plain.Configuration, ConfigurationSecrets: plain.ConfigurationSecrets}, 1, "1")
	restore(plainDestination, RestoreDocuments{Configuration: plain.Configuration}, 1, "2")

	credentialSource := openTestStore(t, ownedDir(t))
	if _, err = credentialSource.Save(ctx, "unix:uid:0", networkInput(t)); err != nil {
		t.Fatal(err)
	}
	withCredentials, err := credentialSource.Export(ctx, true)
	if err != nil || schemaVersion(withCredentials.Configuration) != 1 {
		t.Fatal("wrong credential schema fixture", err)
	}
	pairedDestination := openTestStore(t, ownedDir(t))
	restore(pairedDestination, RestoreDocuments{Configuration: withCredentials.Configuration, ConfigurationSecrets: withCredentials.ConfigurationSecrets}, 1, "1")
	realized, err := pairedDestination.RealizationInput(ctx, nil)
	if err != nil || realized.Secrets["WAN_PASSWORD"] != "TEST_ONLY_FIRST" {
		t.Fatal("paired restore lost included credentials", err)
	}

	unresolvedDestination := openTestStore(t, ownedDir(t))
	unresolved := restore(unresolvedDestination, RestoreDocuments{Configuration: withCredentials.Configuration}, 2, "1")
	if !slices.Equal(unresolved.UnresolvedSecrets, []string{"WAN_PASSWORD"}) {
		t.Fatal("configuration-only restore did not report its unresolved credential")
	}
	unresolvedExport, err := unresolvedDestination.Export(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var unresolvedDocument declaration
	if err = json.Unmarshal(unresolvedExport.Configuration, &unresolvedDocument); err != nil {
		t.Fatal(err)
	}
	foundUnresolved := false
	for _, link := range unresolvedDocument.Links {
		if link.Password != nil && link.Password.Secret == "WAN_PASSWORD" && link.Password.Unresolved {
			foundUnresolved = true
		}
	}
	if !foundUnresolved {
		t.Fatal("configuration-only restore did not persist its unresolved credential marker")
	}

	schemaTwoDestination := openTestStore(t, ownedDir(t))
	restore(schemaTwoDestination, RestoreDocuments{Configuration: schemaTwo(plain.Configuration), ConfigurationSecrets: plain.ConfigurationSecrets}, 2, "1")
	restore(schemaTwoDestination, RestoreDocuments{Configuration: plain.Configuration}, 2, "2")

	importedSchemaTwoDestination := openTestStore(t, ownedDir(t))
	restore(importedSchemaTwoDestination, RestoreDocuments{Configuration: schemaTwo(plain.Configuration)}, 2, "1")
}

func TestSecretsOnlyMissingBoundKeyRefused(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, ownedDir(t))
	if _, err := s.Save(ctx, "unix:uid:0", networkInput(t)); err != nil {
		t.Fatal(err)
	}
	before, err := s.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	input := RestoreDocuments{ConfigurationSecrets: json.RawMessage(portableEmptySecrets)}
	preview, err := s.PreviewRestore(ctx, input)
	if err != nil || !slices.Contains(preview.BlockingSecrets, "WAN_PASSWORD") {
		t.Fatal("missing bound-key warning", err)
	}
	_, err = s.Restore(ctx, "unix:uid:0", before.SavedVersion, input)
	requireFault(t, err, "VALIDATION_FAILED")
	after, err := s.Export(ctx, true)
	if err != nil || after.SavedVersion != before.SavedVersion || !bytes.Equal(after.Configuration, before.Configuration) || !bytes.Equal(after.ConfigurationSecrets, before.ConfigurationSecrets) {
		t.Fatal("refused secrets replacement changed state", err)
	}
}

func TestSecretsOnlyPreservesDeclarationSchema(t *testing.T) {
	for _, schemaVersion := range []int{1, 2} {
		ctx := context.Background()
		root := ownedDir(t)
		s := openTestStore(t, root)
		if _, err := s.Save(ctx, "unix:uid:0", createZone("retained")); err != nil {
			t.Fatal(err)
		}
		if schemaVersion == 2 {
			one, err := s.Export(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			var document declaration
			if err = json.Unmarshal(one.Configuration, &document); err != nil {
				t.Fatal(err)
			}
			document.SchemaVersion = 2
			configuration, err := canonical(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Restore(ctx, "unix:uid:0", one.SavedVersion, RestoreDocuments{Configuration: configuration}); err != nil {
				t.Fatal(err)
			}
		}
		before, err := s.Export(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		var declaration struct{ SchemaVersion int }
		if err = json.Unmarshal(before.Configuration, &declaration); err != nil || declaration.SchemaVersion != schemaVersion {
			t.Fatal("wrong schema fixture", err)
		}
		result, err := s.Restore(ctx, "unix:uid:0", before.SavedVersion, RestoreDocuments{ConfigurationSecrets: json.RawMessage(portableRestoredSecret)})
		if err != nil || result.SavedVersion == before.SavedVersion {
			t.Fatal("secrets restore did not advance local version", err)
		}
		after, err := s.Export(ctx, true)
		if err != nil || !bytes.Equal(after.Configuration, before.Configuration) {
			t.Fatal("secrets-only restore changed declaration or schema", err)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		s = openTestStore(t, root)
		reopened, err := s.Export(ctx, true)
		if err != nil || !bytes.Equal(reopened.Configuration, before.Configuration) {
			t.Fatal("schema metadata did not survive restart", err)
		}
	}
}

func TestRestoreRetainsIdentityAndHistory(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, ownedDir(t))
	zone, err := s.Save(ctx, "unix:uid:0", createZone("revive"))
	if err != nil {
		t.Fatal(err)
	}
	id := zone.Items[0].ID
	archived, err := s.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := DecodeChanges([]byte(`{"operations":[` + ruleOp("allow", "") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "unix:uid:0", rules); err != nil {
		t.Fatal(err)
	}
	remove, err := ObjectChange("delete", "zones", id, &zone.SavedVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "unix:uid:0", remove); err != nil {
		t.Fatal(err)
	}
	result, err := s.Restore(ctx, "unix:uid:0", "3", RestoreDocuments{Configuration: archived.Configuration, ConfigurationSecrets: archived.ConfigurationSecrets})
	if err != nil || result.SavedVersion != "4" {
		t.Fatal("restore did not revive historical identity", err)
	}
	revived, _, err := s.Get(ctx, "zones", id)
	if err != nil || revived.Version != "4" {
		t.Fatal("restored identity unavailable", err)
	}
	list, err := s.List(ctx, "policy.forward")
	if err != nil || len(list.Items) != 0 || list.Version != "4" {
		t.Fatal("empty restored order lost list version", err)
	}
	if _, err = s.Version(ctx, "1"); err != nil {
		t.Fatal("restore lost prior history", err)
	}
	rename, err := ObjectChange("replace", "zones", id, &revived.Version, []byte(`{"name":"revived"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "unix:uid:0", rename); err != nil {
		t.Fatal("ordinary edit after identity revival failed", err)
	}
}

func TestRestoreReplay(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, ownedDir(t))
	if _, err := s.Save(ctx, "unix:uid:0", createZone("once")); err != nil {
		t.Fatal(err)
	}
	exported, err := s.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	input := RestoreDocuments{Configuration: exported.Configuration, ConfigurationSecrets: exported.ConfigurationSecrets}
	if _, err = s.Restore(ctx, "unix:uid:0", "1", input); err != nil {
		t.Fatal(err)
	}
	_, err = s.Restore(ctx, "unix:uid:0", "1", input)
	requireFault(t, err, "PRECONDITION_FAILED")
	after, err := s.Export(ctx, true)
	if err != nil || after.SavedVersion != "2" {
		t.Fatal("replay published twice", err)
	}
	same := RestoreDocuments{Configuration: after.Configuration, ConfigurationSecrets: after.ConfigurationSecrets}
	result, err := s.Restore(ctx, "unix:uid:0", "2", same)
	if err != nil || result.SavedVersion != "3" {
		t.Fatal("explicit identical restore did not record a history event", err)
	}
	_, err = s.Restore(ctx, "unix:uid:0", "", same)
	requireFault(t, err, "PRECONDITION_REQUIRED")
}

func TestRestoreConcurrentSavePrecondition(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, ownedDir(t))
	if _, err := s.Save(ctx, "unix:uid:0", createZone("original")); err != nil {
		t.Fatal(err)
	}
	exported, err := s.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	expected := Version("1")
	batch := createZone("parallel")
	batch.ExpectedSavedVersion = &expected
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := s.Save(ctx, "unix:uid:0", batch); results <- err }()
	go func() {
		<-start
		_, err := s.Restore(ctx, "unix:uid:0", "1", RestoreDocuments{Configuration: exported.Configuration, ConfigurationSecrets: exported.ConfigurationSecrets})
		results <- err
	}()
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else {
			requireFault(t, err, "PRECONDITION_FAILED")
		}
	}
	if success != 1 {
		t.Fatal("concurrent operations did not enforce one saved-head winner")
	}
	head, err := s.Snapshot(ctx)
	if err != nil || head.SavedVersion != "2" {
		t.Fatal("concurrent operations advanced or lost the head", err)
	}
}

func TestRestoreRejectsHistoricalCollectionCollision(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, ownedDir(t))
	create, err := DecodeChanges([]byte(`{"operations":[` + ruleOp("historical", "") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	made, err := s.Save(ctx, "unix:uid:0", create)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := s.Export(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var d declaration
	if err = json.Unmarshal(exported.Configuration, &d); err != nil {
		t.Fatal(err)
	}
	id := d.Policy.Forward[0].ID
	d.Policy.Input, d.Policy.Forward = d.Policy.Forward, []rule{}
	input, _ := json.Marshal(d)
	remove, err := ObjectChange("delete", "policy.forward", id, &made.SavedVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "unix:uid:0", remove); err != nil {
		t.Fatal(err)
	}
	_, err = s.Restore(ctx, "unix:uid:0", "2", RestoreDocuments{Configuration: input})
	requireFault(t, err, "VALIDATION_FAILED")
	head, err := s.Snapshot(ctx)
	if err != nil || head.SavedVersion != "2" {
		t.Fatal("identity collision fenced or changed the store", err)
	}
}

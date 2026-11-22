//go:build integration

package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// legacyStore is a store whose two versions were written by an engine before schema 4: the document in that
// form, and its object versions in the tables and under the names that engine used, without the tables added
// since (its tables as 0.5.0-a.4 left them), reopened by this engine. Version 1 created everything; version 2, the saved
// head, reordered the forward rules, leaving the document as it was.
func legacyStore(t *testing.T) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	root := ownedDir(t)
	s := openTestStore(t, root)
	body := legacyDocument(t)
	hash := sha256.Sum256(body)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.configDB.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(query, err)
		}
	}
	exec("INSERT INTO documents VALUES(?,?)", hash[:], body)
	exec("INSERT INTO versions SELECT 1,0,1,?,secrets_sha256,created_at,actor,7 FROM versions WHERE id=0", hash[:])
	exec("INSERT INTO object_changes VALUES('zones',?,1,1,0)", legacyZone)
	for _, row := range [][2]string{{"addresses", adrHost}, {"services", svcDNS}, {"policy.input", rulA}, {"policy.forward", rulB}, {"policy.forward", rulC}} {
		exec("INSERT INTO policy_changes VALUES(?,?,1,1,0)", row[0], row[1])
	}
	exec("INSERT INTO policy_changes VALUES('policy.input','policy.input',1,0,0)")
	exec("INSERT INTO policy_changes VALUES('policy.forward','policy.forward',1,0,0)")
	exec("INSERT INTO versions SELECT 2,1,1,?,secrets_sha256,created_at,actor,1 FROM versions WHERE id=0", hash[:])
	exec("INSERT INTO policy_changes VALUES('policy.forward','policy.forward',2,0,0)")
	exec("UPDATE pointers SET version_id=2 WHERE name='saved'")
	exec("DROP TABLE firewall_changes")
	exec("DROP TABLE version_writers")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return openTestStore(t, root), root
}

func TestLegacyStoreReadsAndThenSavesInTheCurrentForm(t *testing.T) {
	ctx := context.Background()
	s, root := legacyStore(t)

	rules, err := s.List(ctx, "rules")
	// The one list changed last when either of the two lists did.
	if err != nil || len(rules.Items) != 7 || rules.Version != "2" {
		t.Fatalf("rules of a legacy head: %+v %v", rules, err)
	}
	for _, item := range rules.Items {
		// A rule split from another has the version of the rule it came from.
		if item.Version != "1" {
			t.Fatalf("rule version %s: %s", item.Version, item.Value)
		}
	}
	if alias, _, err := s.Get(ctx, "aliases.addresses", adrHost); err != nil || alias.Version != "1" {
		t.Fatalf("address alias: %+v %v", alias, err)
	}
	if _, _, err := s.Get(ctx, "aliases.ports", svcDNS); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(ctx, "policy.forward"); err == nil {
		t.Fatal("the collection of the form before is still served")
	}
	history, err := s.Version(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	var changed []string
	for _, c := range history.Changes {
		changed = append(changed, c.Collection+" "+c.ID)
	}
	// A rule the upgrade split changed as every rule it became, which is what the version's value shows.
	want := []string{"aliases.addresses " + adrHost, "aliases.ports " + svcDNS, "rules " + rulA, "rules " + rulB, "rules " + rulC,
		"rules " + derivedRuleID(rulA, 1), "rules " + derivedRuleID(rulB, 1), "rules " + derivedRuleID(rulB, 2), "rules " + derivedRuleID(rulB, 3), "rules rules", "zones " + legacyZone}
	slices.Sort(want)
	if !slices.Equal(changed, want) {
		t.Fatalf("what version 1 changed, in today's names:\n got %v\nwant %v", changed, want)
	}

	// A batch that changes nothing saves nothing, although the head's bytes are of another form.
	one := Version("1")
	same, err := ObjectChange("replace", "zones", legacyZone, &one, json.RawMessage(`{"name":"lan"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := s.Save(ctx, systemRootActor, same); err != nil || result.SavedVersion != "2" {
		t.Fatalf("an unchanged batch on a legacy head: %+v %v", result, err)
	}

	// The first save writes the current form, and records every alias and rule with it.
	derived := derivedRuleID(rulA, 1)
	order, err := json.Marshal([]string{derived, rulA, rulB, derivedRuleID(rulB, 1), derivedRuleID(rulB, 2), derivedRuleID(rulB, 3), rulC})
	if err != nil {
		t.Fatal(err)
	}
	two := Version("2")
	result, err := s.Save(ctx, systemRootActor, Changes{Operations: []Operation{{Op: "order", Collection: "rules", ExpectedVersion: &two, Value: order}}})
	if err != nil || result.SavedVersion != "3" {
		t.Fatalf("reordering a legacy head: %+v %v", result, err)
	}
	exported, err := s.Export(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if d, was, _, err := decodeSaved(exported.Configuration); err != nil || was != schemaVersion || d.Rules[0].ID != derived {
		t.Fatalf("saved form %d, first rule %v: %v", was, d.Rules, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, root)
	rules, err = s.List(ctx, "rules")
	if err != nil || len(rules.Items) != 7 || rules.Version != "3" {
		t.Fatalf("rules after the first save and a restart: %+v %v", rules, err)
	}
	for _, item := range rules.Items {
		if item.Version != "3" {
			t.Fatalf("a rule was not carried into the current form's history: %s %s", item.Version, item.Value)
		}
	}
	if alias, _, err := s.Get(ctx, "aliases.ports", svcDNS); err != nil || alias.Version != "3" {
		t.Fatalf("port alias after the first save: %+v %v", alias, err)
	}
	// The version of the form before still loads, for the history and for a rollback to it.
	if old, err := s.Version(ctx, "1"); err != nil || len(old.Changes) != 11 {
		t.Fatalf("version 1 after the first save: %+v %v", old, err)
	}
	if err = VerifyRecoveryState(ctx, root); err != nil {
		t.Fatalf("recovery check of a store that crossed forms: %v", err)
	}
}

// A store that never saved since the upgrade is still one the engine before can open, and its recovery copy checks out.
func TestLegacyStoreIsLeftAsItWasUntilASave(t *testing.T) {
	ctx := context.Background()
	s, root := legacyStore(t)
	if _, err := s.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	var schema, rows int
	if err := s.configDB.QueryRowContext(ctx, "SELECT v.schema_version FROM versions v JOIN pointers p ON p.version_id=v.id").Scan(&schema); err != nil || schema != 1 {
		t.Fatalf("head form %d %v", schema, err)
	}
	if err := s.configDB.QueryRowContext(ctx, "SELECT count(*) FROM firewall_changes").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("reading wrote %d rows %v", rows, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryState(ctx, root); err != nil {
		t.Fatalf("recovery check of a legacy store: %v", err)
	}
}

// A restore onto a head of the form before saves the current form too, with every rule and alias recorded, and
// the engine that wrote a restore journal before the upgrade still finds its document in the store.
func TestRestoreOntoALegacyHead(t *testing.T) {
	ctx := context.Background()
	s, root := legacyStore(t)
	before, err := s.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	var secrets []byte
	if err = s.secretDB.QueryRowContext(ctx, "SELECT body FROM documents").Scan(&secrets); err != nil {
		t.Fatal(err)
	}
	if matches, err := s.RestoreMatches(ctx, "2", RestoreDocuments{Configuration: legacyDocument(t), ConfigurationSecrets: secrets}); err != nil || !matches {
		t.Fatalf("the head no longer matches the document it was saved as: %t %v", matches, err)
	}
	result, err := s.Restore(ctx, rootRestoreActor, before.SavedVersion, RestoreDocuments{Configuration: legacyDocument(t)})
	if err != nil || result.SavedVersion != "3" {
		t.Fatalf("restore: %+v %v", result, err)
	}
	// The journal of a restore the engine before began names the document in its form; the store now holds it
	// as it reads today.
	if matches, err := s.RestoreMatches(ctx, "3", RestoreDocuments{Configuration: legacyDocument(t), ConfigurationSecrets: secrets}); err != nil || !matches {
		t.Fatalf("the restored head does not match the document it was restored from: %t %v", matches, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, root)
	rules, err := s.List(ctx, "rules")
	if err != nil || len(rules.Items) != 7 {
		t.Fatalf("rules after a restore and a restart: %+v %v", rules, err)
	}
	for _, item := range rules.Items {
		if item.Version != "3" {
			t.Fatalf("a rule was not carried into the current form's history: %s %s", item.Version, item.Value)
		}
	}
	if err = VerifyRecoveryState(ctx, root); err != nil {
		t.Fatal(err)
	}
}

// Every saved version says which Andaru wrote it, so an engine that finds versions a newer Andaru saved can tell
// them from its own. A version whose Andaru did not record it says nothing.
func TestSavedVersionsNameTheirWriter(t *testing.T) {
	ctx := context.Background()
	old := Writer
	t.Cleanup(func() { Writer = old })
	// A store an engine before left has no table of writers at all; opening it adds the table.
	s, _ := legacyStore(t)
	Writer = "0.5.0-a.5"
	if _, err := s.Save(ctx, systemRootActor, createZone("guest")); err != nil {
		t.Fatal(err)
	}
	// A build without a version still saves, and reads as one that did not record its writer.
	Writer = ""
	if _, err := s.Save(ctx, systemRootActor, createZone("iot")); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range history.Items {
		got = append(got, string(item.ID)+"="+item.WrittenBy)
	}
	if want := []string{"4=", "3=0.5.0-a.5", "2=", "1=", "0="}; !slices.Equal(got, want) {
		t.Fatalf("writers %v, want %v", got, want)
	}
	if one, err := s.Version(ctx, "3"); err != nil || one.WrittenBy != "0.5.0-a.5" {
		t.Fatalf("version 3: %+v %v", one.VersionInfo, err)
	}
	// A store made now names the writer of its first version too.
	Writer = "0.5.0-a.5"
	fresh, err := openTestStore(t, ownedDir(t)).History(ctx, nil, 10)
	if err != nil || len(fresh.Items) != 1 || fresh.Items[0].WrittenBy != "0.5.0-a.5" {
		t.Fatalf("a new store's history: %+v %v", fresh.Items, err)
	}
}

// A version that needs a decision says so in its view, with the questions it holds.
func TestVersionViewListsItsQuestions(t *testing.T) {
	withDecisions(t, zoneDecision("rename", 1, func(map[string]any) bool { return true }))
	s, _ := legacyStore(t)
	v, err := s.Version(context.Background(), "2")
	if err != nil || len(v.Questions) != 1 || v.Questions[0].ID != "rename" || v.Questions[0].Answered {
		t.Fatalf("version view: %+v %v", v.Questions, err)
	}
}

// On start, a head of an older schema is saved as a new version of the current one; the version it came from keeps
// its bytes for a rollback, and a head already current is left alone.
func TestMigrateHeadSavesTheCurrentSchema(t *testing.T) {
	ctx := context.Background()
	s, root := legacyStore(t)
	saved, err := s.MigrateHead(ctx)
	if err != nil || saved != "3" {
		t.Fatalf("migrate: %q %v", saved, err)
	}
	var schema, parent int
	if err = s.configDB.QueryRowContext(ctx, "SELECT v.schema_version,v.parent_id FROM versions v JOIN pointers p ON p.version_id=v.id").Scan(&schema, &parent); err != nil || schema != schemaVersion || parent != 2 {
		t.Fatalf("head schema %d parent %d %v", schema, parent, err)
	}
	var old []byte
	if err = s.configDB.QueryRowContext(ctx, "SELECT d.body FROM versions v JOIN documents d ON d.sha256=v.config_sha256 WHERE v.id=2").Scan(&old); err != nil || string(old) != string(legacyDocument(t)) {
		t.Fatalf("version 2 changed: %v", err)
	}
	rules, err := s.List(ctx, "rules")
	if err != nil || len(rules.Items) != 7 {
		t.Fatalf("rules after the migration: %+v %v", rules, err)
	}
	if again, err := s.MigrateHead(ctx); err != nil || again != "" {
		t.Fatalf("a current head was saved again: %q %v", again, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = VerifyRecoveryState(ctx, root); err != nil {
		t.Fatal(err)
	}
}

// newerHead makes the store's head a version of a schema newer than this Andaru knows, as a later Andaru leaves it:
// version 2, version 1's document marked with the next schema, which created a zone.
func newerHead(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		"INSERT INTO versions SELECT 2,1,schema_version+1,config_sha256,secrets_sha256,created_at,actor,1 FROM versions WHERE id=1",
		"INSERT INTO object_changes VALUES('zones','zon_0000000f-0000-4000-8000-00000000000f',2,1,0)",
		"UPDATE pointers SET version_id=2 WHERE name='saved'",
	} {
		if _, err := s.configDB.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
}

// After a rollback to an older Andaru, the store opens on a copy of the newest version this Andaru can read, saved
// as the new head; the newer version stays in the history, and saves carry on from the copy.
func TestNewerHeadOpensOnTheNewestReadableVersion(t *testing.T) {
	ctx := context.Background()
	root := ownedDir(t)
	s := openTestStore(t, root)
	if _, err := s.Save(ctx, systemRootActor, createZone("guest")); err != nil {
		t.Fatal(err)
	}
	want, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newerHead(t, s)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, root)
	got, err := s.Snapshot(ctx)
	if err != nil || got.SavedVersion != "3" || string(got.Value) != string(want.Value) {
		t.Fatalf("snapshot after a rollback: %s %s %v\nwant %s", got.SavedVersion, got.Value, err, want.Value)
	}
	var parent, schema int
	if err = s.configDB.QueryRowContext(ctx, "SELECT parent_id,schema_version FROM versions WHERE id=3").Scan(&parent, &schema); err != nil || parent != 2 || schema != schemaVersion {
		t.Fatalf("copy parent %d schema %d %v", parent, schema, err)
	}
	zones, err := s.List(ctx, "zones")
	if err != nil || len(zones.Items) == 0 {
		t.Fatalf("zones: %+v %v", zones, err)
	}
	for _, z := range zones.Items {
		if z.Version != "3" {
			t.Fatalf("a zone reads its version from elsewhere than the copy: %+v", z)
		}
	}
	// The zone the newer version created is deleted in the copy, and that version reads as one this Andaru cannot.
	var deleted bool
	if err = s.configDB.QueryRowContext(ctx, "SELECT deleted FROM object_changes WHERE object_id='zon_0000000f-0000-4000-8000-00000000000f' AND version_id=3").Scan(&deleted); err != nil || !deleted {
		t.Fatalf("no deletion of the newer zone: %t %v", deleted, err)
	}
	var f *Fault
	if _, err = s.Version(ctx, "2"); !errors.As(err, &f) || f.Code != "NOT_FOUND" {
		t.Fatalf("reading the newer version: %v", err)
	}
	// Its roles still read, for the live generation that names it; the caller checks them against its digest.
	if roles, err := s.Roles(ctx, "2"); err != nil || len(roles) != 0 {
		t.Fatalf("roles of the newer version: %+v %v", roles, err)
	}
	if rules, err := s.List(ctx, "rules"); err != nil || rules.Version != "3" {
		t.Fatalf("the rule list reads its version from elsewhere than the copy: %+v %v", rules.Version, err)
	}
	if _, err = s.Save(ctx, systemRootActor, createZone("iot")); err != nil {
		t.Fatal(err)
	}
}

// A store with no version this Andaru can read is refused, as before.
func TestNewerOnlyStoreIsRefused(t *testing.T) {
	ctx := context.Background()
	root := ownedDir(t)
	s := openTestStore(t, root)
	if _, err := s.configDB.ExecContext(ctx, "UPDATE versions SET schema_version=schema_version+1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(ctx, root); err == nil {
		s.Close()
		t.Fatal("a store of a newer schema alone opened")
	}
}

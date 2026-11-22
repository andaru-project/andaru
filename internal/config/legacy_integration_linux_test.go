//go:build integration

package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"testing"
)

// legacyStore is a store whose saved head, version 1, was written by an engine before schema 4: the document in
// that form, and its object versions in the tables and under the names that engine used.
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
	for _, row := range [][2]string{{"addresses", adrHost}, {"services", svcDNS}, {"policy.input", rulA}, {"policy.forward", rulB}, {"policy.forward", "rul_00000003-0000-4000-8000-000000000003"}} {
		exec("INSERT INTO policy_changes VALUES(?,?,1,1,0)", row[0], row[1])
	}
	exec("INSERT INTO policy_changes VALUES('policy.input','policy.input',1,0,0)")
	exec("INSERT INTO policy_changes VALUES('policy.forward','policy.forward',1,0,0)")
	exec("UPDATE pointers SET version_id=1 WHERE name='saved'")
	return s, root
}

func TestLegacyStoreReadsAndThenSavesInTheCurrentForm(t *testing.T) {
	ctx := context.Background()
	s, root := legacyStore(t)

	rules, err := s.List(ctx, "rules")
	if err != nil || len(rules.Items) != 5 || rules.Version != "1" {
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
	if want := []string{"aliases.addresses " + adrHost, "aliases.ports " + svcDNS, "rules " + rulA, "rules " + rulB, "rules rul_00000003-0000-4000-8000-000000000003", "rules rules", "zones " + legacyZone}; !slices.Equal(changed, want) {
		t.Fatalf("what version 1 changed, in today's names:\n got %v\nwant %v", changed, want)
	}

	// A batch that changes nothing saves nothing, although the head's bytes are of another form.
	one := Version("1")
	same, err := ObjectChange("replace", "zones", legacyZone, &one, json.RawMessage(`{"name":"lan"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := s.Save(ctx, systemRootActor, same); err != nil || result.SavedVersion != "1" {
		t.Fatalf("an unchanged batch on a legacy head: %+v %v", result, err)
	}

	// The first save writes the current form, and records every alias and rule with it.
	derived := derivedRuleID(rulA, 1)
	order, err := json.Marshal([]string{derived, rulA, rulB, derivedRuleID(rulB, 1), "rul_00000003-0000-4000-8000-000000000003"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Save(ctx, systemRootActor, Changes{Operations: []Operation{{Op: "order", Collection: "rules", ExpectedVersion: &one, Value: order}}})
	if err != nil || result.SavedVersion != "2" {
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
	if err != nil || len(rules.Items) != 5 || rules.Version != "2" {
		t.Fatalf("rules after the first save and a restart: %+v %v", rules, err)
	}
	for _, item := range rules.Items {
		if item.Version != "2" {
			t.Fatalf("a rule was not carried into the current form's history: %s %s", item.Version, item.Value)
		}
	}
	if alias, _, err := s.Get(ctx, "aliases.ports", svcDNS); err != nil || alias.Version != "2" {
		t.Fatalf("port alias after the first save: %+v %v", alias, err)
	}
	// The version of the form before still loads, for the history and for a rollback to it.
	if old, err := s.Version(ctx, "1"); err != nil || len(old.Changes) != 7 {
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

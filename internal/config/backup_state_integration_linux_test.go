//go:build integration

package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnresolvedReferencePersists(t *testing.T) {
	ctx := context.Background()
	root := ownedDir(t)
	s, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Save(ctx, "unix:uid:0", networkInput(t))
	if err != nil {
		t.Fatal(err)
	}
	object, _, err := s.Get(ctx, "links", saved.Created["l2"])
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(object.Value, &value); err != nil {
		t.Fatal(err)
	}
	value["password"] = map[string]any{"secret": "WAN_PASSWORD", "unresolved": true}
	raw, _ := json.Marshal(value)
	change, err := ObjectChange("replace", "links", saved.Created["l2"], &object.Version, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "unix:uid:0", change); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = VerifyRecoveryState(ctx, root); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snapshot, err := s.Snapshot(ctx)
	if err != nil || snapshot.SavedVersion != "2" {
		t.Fatalf("unresolved saved state unreadable: %v", err)
	}
	if strings.Contains(string(snapshot.Value), "TEST_ONLY_FIRST") {
		t.Fatal("snapshot exposed a secret")
	}
	if _, err = s.History(ctx, nil, 10); err != nil {
		t.Fatal(err)
	}
	_, err = s.RealizationInput(ctx, nil)
	fault := requireFault(t, err, "VALIDATION_FAILED")
	details, _ := json.Marshal(fault.Details)
	if !strings.Contains(string(details), "WAN_PASSWORD") || !strings.Contains(string(details), "/password") {
		t.Fatal("unresolved reference error is not actionable")
	}
	if _, err = s.Save(ctx, "unix:uid:0", createZone("while-unresolved")); err != nil {
		t.Fatal("unrelated edit could not preserve unresolved state", err)
	}
	_, err = s.Draft(ctx, createZone("unrealizable"))
	requireFault(t, err, "VALIDATION_FAILED")
}

func TestMissingBoundSecretIsStillCorruption(t *testing.T) {
	ctx := context.Background()
	root := ownedDir(t)
	s, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "unix:uid:0", networkInput(t)); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := canonical(emptySecrets())
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	secretDB, err := openDatabase(ctx, filepath.Join(root, "configuration", "secrets.db"), "rw", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = secretDB.ExecContext(ctx, "INSERT OR IGNORE INTO documents(sha256,body) VALUES(?,?)", hash[:], body); err != nil {
		t.Fatal(err)
	}
	if err = secretDB.Close(); err != nil {
		t.Fatal(err)
	}
	configDB, err := openDatabase(ctx, filepath.Join(root, "configuration", "config.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configDB.ExecContext(ctx, "UPDATE versions SET secrets_sha256=? WHERE id=1", hash[:]); err != nil {
		t.Fatal(err)
	}
	if err = configDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err = VerifyRecoveryState(ctx, root); err == nil {
		t.Fatal("recovery accepted missing bound secret")
	}
	if reopened, err := Open(ctx, root); err == nil {
		reopened.Close()
		t.Fatal("store accepted missing bound secret")
	}
}

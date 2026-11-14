//go:build integration

package config

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRecoveryWALHelper(t *testing.T) {
	root := os.Getenv("ANDARU_RECOVERY_WAL_ROOT")
	if root == "" {
		t.Skip("subprocess fixture only")
	}
	s, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(context.Background(), "unix:uid:0", createZone("wal-zone")); err != nil {
		t.Fatal(err)
	}
	// Leave committed WAL records without checkpointing: a crash before an upgrade must remain recoverable.
	os.Exit(0)
}

func recoveryCopy(t *testing.T, source string) string {
	t.Helper()
	target := ownedDir(t)
	if err := os.Mkdir(filepath.Join(target, "configuration"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(source, "configuration"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(source, "configuration", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(target, "configuration", entry.Name()), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func TestVerifyRecoveryStateWALPair(t *testing.T) {
	root := ownedDir(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryWALHelper$")
	cmd.Env = append(os.Environ(), "ANDARU_RECOVERY_WAL_ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	wal := filepath.Join(root, "configuration", "config.db-wal")
	raw, err := os.ReadFile(wal)
	if err != nil || len(raw) == 0 {
		t.Fatalf("no committed WAL: %v", err)
	}
	before := sha256.Sum256(raw)
	copy := recoveryCopy(t, root)
	if err = VerifyRecoveryState(context.Background(), copy); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(context.Background(), filepath.Join(copy, "configuration", "config.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var saved int
	if err = db.QueryRow("SELECT version_id FROM pointers WHERE name='saved'").Scan(&saved); err != nil || saved != 1 {
		t.Fatalf("lost committed WAL version: %d %v", saved, err)
	}
	after, err := os.ReadFile(wal)
	if err != nil || sha256.Sum256(after) != before {
		t.Fatal("verification changed source WAL")
	}
}

func recoveryStore(t *testing.T) string {
	t.Helper()
	root := ownedDir(t)
	s, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRecoveryStateMismatch(t *testing.T) {
	root := recoveryStore(t)
	db, err := openDatabase(context.Background(), filepath.Join(root, "configuration", "config.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE versions SET secrets_sha256=zeroblob(32)")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = VerifyRecoveryState(context.Background(), root); err == nil {
		t.Fatal("accepted mismatched configuration/secret pair")
	}
}

func TestRecoveryStateCorruption(t *testing.T) {
	for _, file := range []string{"config.db", "secrets.db"} {
		t.Run(file, func(t *testing.T) {
			root := recoveryStore(t)
			if err := os.WriteFile(filepath.Join(root, "configuration", file), []byte("not sqlite"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := VerifyRecoveryState(context.Background(), root); err == nil {
				t.Fatalf("accepted corrupt %s", file)
			}
		})
	}
}

func TestRecoveryDoesNotMigrate(t *testing.T) {
	root := recoveryStore(t)
	path := filepath.Join(root, "configuration", "config.db")
	db, err := openDatabase(context.Background(), path, "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TABLE vip_changes"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(raw)
	if err = VerifyRecoveryState(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(raw) != before {
		t.Fatal("recovery verification migrated the database")
	}
	db, err = openDatabase(context.Background(), path, "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='vip_changes'").Scan(&count); err != nil || count != 0 {
		t.Fatal(fmt.Sprintf("created missing table: %d %v", count, err))
	}
}

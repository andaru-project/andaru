//go:build integration

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

const crashRestoreConfiguration = `{"schemaVersion":1,"links":[],"segments":[],"zones":[{"id":"zon_00000000-0000-4000-8000-000000000099","name":"restored"}]}`

func TestRestoreCrashHelper(t *testing.T) {
	root := os.Getenv("ANDARU_RESTORE_CRASH_ROOT")
	if root == "" {
		t.Skip("subprocess fixture only")
	}
	ctx := context.Background()
	s, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBackupDocuments(RestoreDocuments{Configuration: json.RawMessage(crashRestoreConfiguration), ConfigurationSecrets: json.RawMessage(portableRestoredSecret)})
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.prepareRestore(ctx, systemRootActor, "1", decoded)
	if err != nil {
		t.Fatal(err)
	}
	phase := os.Getenv("ANDARU_RESTORE_CRASH_PHASE")
	if phase == "before-secrets" {
		os.Exit(0)
	}
	if err = p.commitSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	if phase == "after-secrets" {
		os.Exit(0)
	}
	if phase != "after-pointer" {
		t.Fatal("unknown fixture phase")
	}
	if _, err = p.commitConfiguration(ctx); err != nil {
		t.Fatal(err)
	}
	// Exit without closing SQLite or returning the committed restore to a caller.
	os.Exit(0)
}

func TestRestoreCrashPublicationPair(t *testing.T) {
	for _, phase := range []string{"before-secrets", "after-secrets", "after-pointer"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			root := ownedDir(t)
			s := openTestStore(t, root)
			if _, err := s.Save(ctx, systemRootActor, createZone("original")); err != nil {
				t.Fatal(err)
			}
			before, err := s.Export(ctx, true)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestRestoreCrashHelper$", "-test.count=1")
			child.Dir = "/"
			child.Env = append(os.Environ(), "ANDARU_RESTORE_CRASH_ROOT="+root, "ANDARU_RESTORE_CRASH_PHASE="+phase)
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("crash fixture failed: %v %s", err, output)
			}
			s = openTestStore(t, root)
			after, err := s.Export(ctx, true)
			if err != nil {
				t.Fatal("crash left an unreadable pair", err)
			}
			if phase == "after-pointer" {
				if after.SavedVersion != "2" || !bytes.Contains(after.Configuration, []byte(`"name":"restored"`)) || !bytes.Contains(after.ConfigurationSecrets, []byte("TEST_ONLY_RESTORED")) {
					t.Fatal("committed restore lost its paired state")
				}
			} else if after.SavedVersion != "1" || !bytes.Equal(after.Configuration, before.Configuration) || !bytes.Equal(after.ConfigurationSecrets, before.ConfigurationSecrets) {
				t.Fatal("uncommitted restore changed the saved pair")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if err = VerifyRecoveryState(ctx, root); err != nil {
				t.Fatal("crash left invalid recoverable state", err)
			}
		})
	}
}

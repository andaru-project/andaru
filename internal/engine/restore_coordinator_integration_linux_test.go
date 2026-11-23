//go:build integration

package engine

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/identity"
)

func init() {
	for i, argument := range os.Args {
		if argument != "--restore-crash-helper" || i+3 >= len(os.Args) {
			continue
		}
		stateDir, sourceDir, crashPoint := os.Args[i+1], os.Args[i+2], os.Args[i+3]
		ctx := context.Background()
		configuration, err := config.Open(ctx, stateDir)
		if err != nil {
			panic(err)
		}
		defer configuration.Close()
		identities, err := identity.Open(ctx, stateDir)
		if err != nil {
			panic(err)
		}
		defer identities.Close()
		audits, err := audit.Open(ctx, stateDir)
		if err != nil {
			panic(err)
		}
		defer audits.Close()
		source, err := identity.Open(ctx, sourceDir)
		if err != nil {
			panic(err)
		}
		defer source.Close()
		configurationDocuments, err := configuration.Export(ctx, true)
		if err != nil {
			panic(err)
		}
		identityDocuments, err := source.ExportBackup(ctx, true)
		if err != nil {
			panic(err)
		}
		revision, err := identities.Revision(ctx)
		if err != nil {
			panic(err)
		}
		security := newSecurityCoordinator(identities, audits, stateDir)
		coordinator := newRestoreCoordinator(configuration, identities, security, stateDir)
		coordinator.crash = func(point string) error {
			if point == crashPoint {
				os.Exit(79)
			}
			return nil
		}
		_, err = coordinator.Execute(ctx, coordinatedRestoreRequest{
			OperationID: "restore-crash-operation", Fingerprint: []byte("fixed-restore-request"), Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "unix:uid:0", Target: "0123456789abcdef0123456789abcdef",
			ExpectedSavedVersion: configurationDocuments.SavedVersion, ExpectedIdentityRevision: revision,
			Configuration: config.RestoreDocuments{Configuration: configurationDocuments.Configuration, ConfigurationSecrets: configurationDocuments.ConfigurationSecrets},
			Identity:      identity.RestoreDocuments{Users: identityDocuments.Users, UserCredentials: identityDocuments.UserCredentials},
		})
		if err != nil {
			panic(err)
		}
		os.Exit(78)
	}
}

func TestRestoreCoordinatorCrashRecoveryAndReplay(t *testing.T) {
	requireRoot(t)
	for _, crashPoint := range []string{"after-journal", "before-config-publication", "after-config-publication", "before-identity-publication", "after-identity-publication", "after-journal-then-rollback", "after-config-publication-then-rollback", "after-identity-publication-then-rollback"} {
		t.Run(crashPoint, func(t *testing.T) {
			// A rollback before the restore settled: the head then (the expected or the restored version) is of a schema
			// newer than this Andaru knows, and the store opens on a copy of an older version.
			rollback := strings.HasSuffix(crashPoint, "-then-rollback")
			crashPoint = strings.TrimSuffix(crashPoint, "-then-rollback")
			root := privateTestDir(t)
			stateDir, sourceDir := filepath.Join(root, "state"), filepath.Join(root, "source")
			ctx := context.Background()
			configuration, err := config.Open(ctx, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			identities, err := identity.Open(ctx, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = identities.BootstrapAdmin(ctx, "destinationadmin", "Destination", []byte("correct-horse-battery-9")); err != nil {
				t.Fatal(err)
			}
			audits, err := audit.Open(ctx, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if err = errors.Join(configuration.Close(), identities.Close(), audits.Close()); err != nil {
				t.Fatal(err)
			}
			source, err := identity.Open(ctx, sourceDir)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = source.BootstrapAdmin(ctx, "sourceadmin", "Source", []byte("staple-battery-horse-9")); err != nil {
				t.Fatal(err)
			}
			if err = source.Close(); err != nil {
				t.Fatal(err)
			}

			if rollback {
				// A version before the one the restore expects, for the older Andaru to fall back to.
				configuration, err = config.Open(ctx, stateDir)
				if err != nil {
					t.Fatal(err)
				}
				zone, err := config.DecodeChanges([]byte(`{"operations":[{"op":"create","collection":"zones","localId":"z","value":{"name":"guest"}}]}`))
				if err == nil {
					_, err = configuration.Save(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, zone)
				}
				if err = errors.Join(err, configuration.Close()); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(os.Args[0], "-test.run=^$", "--restore-crash-helper", stateDir, sourceDir, crashPoint)
			output, runErr := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) || exit.ExitCode() != 79 {
				t.Fatalf("helper exit=%v output=%s", runErr, output)
			}

			if rollback {
				db, err := sql.Open("sqlite", "file:"+filepath.Join(stateDir, "configuration", "config.db")+"?mode=rw")
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("UPDATE versions SET schema_version=schema_version+1 WHERE id=(SELECT version_id FROM pointers WHERE name='saved')")
				if err = errors.Join(err, db.Close()); err != nil {
					t.Fatal(err)
				}
			}
			configuration, err = config.Open(ctx, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer configuration.Close()
			identities, err = identity.Open(ctx, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer identities.Close()
			audits, err = audit.Open(ctx, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer audits.Close()
			security := newSecurityCoordinator(identities, audits, stateDir)
			coordinator := newRestoreCoordinator(configuration, identities, security, stateDir)
			if err = coordinator.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			journal, found, err := coordinator.readJournal("restore-crash-operation")
			if rollback && crashPoint != "after-journal" {
				// The configuration was published, then set aside with every newer version: that half is done, and the
				// identities are still restored.
				if err != nil || !found || journal.State != "committed" {
					t.Fatalf("journal=%+v found=%v err=%v", journal, found, err)
				}
				if newer, err := configuration.Newer(ctx, journal.TargetSavedVersion); err != nil || !newer {
					t.Fatalf("restored version not set aside: %v %v", newer, err)
				}
				if matches, err := identities.RestoreMatches(ctx, identity.BackupDocuments{IdentityRevision: journal.TargetIdentityRevision, Users: journal.Identity.Users, UserCredentials: journal.Identity.UserCredentials}); err != nil || !matches {
					t.Fatalf("identity target=%v err=%v", matches, err)
				}
				return
			}
			if rollback {
				// The configuration must still be published, against a version a rollback set aside: abandoned, and its
				// replay says so.
				if err != nil || !found || journal.State != "abandoned" {
					t.Fatalf("journal=%+v found=%v err=%v", journal, found, err)
				}
				_, err = coordinator.Execute(ctx, coordinatedRestoreRequest{OperationID: journal.OperationID, Fingerprint: []byte("fixed-restore-request"), Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "unix:uid:0", Target: "0123456789abcdef0123456789abcdef", Configuration: journal.Configuration, Identity: journal.Identity})
				if !errors.Is(err, errRestoreAbandoned) {
					t.Fatalf("replay of an abandoned restore: %v", err)
				}
				// The next start leaves an abandoned journal alone.
				if err = coordinator.Reconcile(ctx); err != nil {
					t.Fatalf("a second start: %v", err)
				}
				return
			}
			if err != nil || !found || journal.State != "committed" {
				t.Fatalf("journal=%+v found=%v err=%v", journal, found, err)
			}
			if matches, err := configuration.RestoreMatches(ctx, journal.TargetSavedVersion, journal.Configuration); err != nil || !matches {
				t.Fatalf("config target=%v err=%v", matches, err)
			}
			if matches, err := identities.RestoreMatches(ctx, identity.BackupDocuments{IdentityRevision: journal.TargetIdentityRevision, Users: journal.Identity.Users, UserCredentials: journal.Identity.UserCredentials}); err != nil || !matches {
				t.Fatalf("identity target=%v err=%v", matches, err)
			}
			if _, err := os.Stat(filepath.Join(generation.Dir(stateDir), "pointers.json")); !os.IsNotExist(err) {
				t.Fatalf("restore activated networking: %v", err)
			}

			result, err := coordinator.Execute(ctx, coordinatedRestoreRequest{OperationID: journal.OperationID, Fingerprint: []byte("fixed-restore-request"), Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "unix:uid:0", Target: "0123456789abcdef0123456789abcdef", Configuration: journal.Configuration, Identity: journal.Identity})
			if err != nil || !result.Replayed {
				t.Fatalf("replay=%+v err=%v", result, err)
			}
			if _, err = coordinator.Execute(ctx, coordinatedRestoreRequest{OperationID: journal.OperationID, Fingerprint: []byte("changed-restore-request"), Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "unix:uid:0", Target: "0123456789abcdef0123456789abcdef", Configuration: journal.Configuration}); !errors.Is(err, errRestoreOperationConflict) && !errors.Is(err, audit.ErrOperationConflict) {
				t.Fatalf("changed replay: %v", err)
			}
		})
	}
}

// A journal of a format a newer Andaru wrote is left for that Andaru: it neither stops this one's start nor is
// changed.
func TestNewerRestoreJournalIsLeftAlone(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()
	stateDir := filepath.Join(privateTestDir(t), "state")
	configuration, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer configuration.Close()
	identities, err := identity.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer identities.Close()
	audits, err := audit.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer audits.Close()
	coordinator := newRestoreCoordinator(configuration, identities, newSecurityCoordinator(identities, audits, stateDir), stateDir)
	if err = coordinator.ensureJournalDir(); err != nil {
		t.Fatal(err)
	}
	// The newer Andaru began its audit operation before it wrote the journal, and was interrupted.
	if _, err = audits.Begin(ctx, audit.Intent{OperationID: "restore-newer-operation", Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "unix:uid:0", Action: audit.ActionBackupRestored, Target: "0123456789abcdef0123456789abcdef", RequestHash: strings.Repeat("a", 64), Category: audit.CategoryProtected}); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(coordinator.journalDir(), "restore-newer-operation.json")
	body := []byte(`{"schemaVersion":2,"operationId":"restore-newer-operation","state":"something-new"}`)
	if err = os.WriteFile(newer, body, 0o600); err != nil {
		t.Fatal(err)
	}
	// The start after the rollback: a fresh audit store and coordinator.
	if err = audits.Close(); err != nil {
		t.Fatal(err)
	}
	if audits, err = audit.Open(ctx, stateDir); err != nil {
		t.Fatal(err)
	}
	coordinator = newRestoreCoordinator(configuration, identities, newSecurityCoordinator(identities, audits, stateDir), stateDir)
	if err = coordinator.Reconcile(ctx); err != nil {
		t.Fatalf("a newer journal stopped reconciliation: %v", err)
	}
	if kept, err := os.ReadFile(newer); err != nil || string(kept) != string(body) {
		t.Fatalf("the newer journal changed: %s %v", kept, err)
	}
}

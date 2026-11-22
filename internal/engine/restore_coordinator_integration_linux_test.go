//go:build integration

package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, crashPoint := range []string{"after-journal", "before-config-publication", "after-config-publication", "before-identity-publication", "after-identity-publication"} {
		t.Run(crashPoint, func(t *testing.T) {
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

			command := exec.Command(os.Args[0], "-test.run=^$", "--restore-crash-helper", stateDir, sourceDir, crashPoint)
			output, runErr := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) || exit.ExitCode() != 79 {
				t.Fatalf("helper exit=%v output=%s", runErr, output)
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

//go:build integration

package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/config"
)

func snapshotSource(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root ownership checks")
	}
	root, err := os.MkdirTemp("/var/tmp", "andaru-recovery-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	s, err := config.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "generations", "1"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "generations", "1", "manifest.json"), []byte(`{"id":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func recoveryRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/var/tmp", "andaru-upgrade-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

func stagedSnapshot(t *testing.T, root, source string, complete bool) (string, recoveryManifest) {
	t.Helper()
	stage, err := newRecoveryStage(root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := copyState(context.Background(), source, filepath.Join(stage, "state"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := recoveryManifest{FormatVersion: 1, PreparedAt: time.Now().UTC(), Package: packageInfo{"0.4.0~rc2-1", "amd64"}, Files: files, PackageRetained: complete}
	if complete {
		raw := testDeb(t, "0.4.0~rc2-1", "amd64")
		if err = os.WriteFile(filepath.Join(stage, "package.deb"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		manifest.PackageSHA256 = hex.EncodeToString(sum[:])
	}
	return stage, manifest
}

func TestSnapshotIncludesGenerationAndWAL(t *testing.T) {
	source := snapshotSource(t)
	root := recoveryRoot(t)
	// A cold copy includes all SQLite sidecars; validation of real committed WAL is config's root test.
	sidecar := filepath.Join(source, "configuration", "config.db-journal")
	if err := os.WriteFile(sidecar, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	stage, manifest := stagedSnapshot(t, root, source, true)
	result, err := publishRecovery(context.Background(), root, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"configuration/config.db", "configuration/secrets.db", "configuration/config.db-journal", "generations/1/manifest.json", "package.deb", "manifest.json"} {
		path := filepath.Join(result.Directory, "state", name)
		if name == "package.deb" || name == "manifest.json" {
			path = filepath.Join(result.Directory, name)
		}
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("snapshot lost %s: %v", name, err)
		}
	}
}

func TestSnapshotRefusesLinksAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			source := snapshotSource(t)
			bad := filepath.Join(source, "bad")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink("/etc/shadow", bad)
			case "hardlink":
				err = os.Link(filepath.Join(source, "configuration", "config.db"), bad)
			case "fifo":
				err = syscall.Mkfifo(bad, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = copyState(context.Background(), source, filepath.Join(recoveryRoot(t), "state")); err == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
	source := snapshotSource(t)
	if _, err := copyState(context.Background(), source, filepath.Join(source, "nested")); err == nil {
		t.Fatal("copied recovery recursively into its source")
	}
}

func TestSnapshotModes(t *testing.T) {
	stage, manifest := stagedSnapshot(t, recoveryRoot(t), snapshotSource(t), true)
	root := filepath.Dir(stage)
	result, err := publishRecovery(context.Background(), root, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(result.Directory, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		want := os.FileMode(0600)
		if d.IsDir() {
			want = 0700
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s mode=%o", path, st.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publishedSnapshot(t *testing.T, root, source string) Preparation {
	t.Helper()
	stage, manifest := stagedSnapshot(t, root, source, true)
	got, err := publishRecovery(context.Background(), root, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSnapshotCancelledBeforePublish(t *testing.T) {
	root := recoveryRoot(t)
	source := snapshotSource(t)
	old := publishedSnapshot(t, root, source)
	stage, manifest := stagedSnapshot(t, root, source, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := publishRecovery(ctx, root, stage, manifest); err == nil {
		t.Fatal("cancelled snapshot published")
	}
	if _, err := os.Stat(old.Directory); err != nil {
		t.Fatal("cancelled snapshot removed the previous recovery")
	}
}

func TestSnapshotFailureKeepsPrevious(t *testing.T) {
	root := recoveryRoot(t)
	source := snapshotSource(t)
	old := publishedSnapshot(t, root, source)
	before, err := os.ReadFile(filepath.Join(root, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	stage, manifest := stagedSnapshot(t, root, source, true)
	if err = os.WriteFile(filepath.Join(stage, "state", "configuration", "secrets.db"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = publishRecovery(context.Background(), root, stage, manifest); err == nil {
		t.Fatal("published corrupt snapshot")
	}
	after, err := os.ReadFile(filepath.Join(root, "current.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("failed snapshot changed current recovery")
	}
	if _, err = os.Stat(old.Directory); err != nil {
		t.Fatal("failed snapshot pruned previous set")
	}
}

func TestStateOnlyKeepsCompleteRecovery(t *testing.T) {
	root := recoveryRoot(t)
	source := snapshotSource(t)
	old := publishedSnapshot(t, root, source)
	stage, manifest := stagedSnapshot(t, root, source, false)
	got, err := publishRecovery(context.Background(), root, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got.PackageRetained || len(got.Warnings) == 0 {
		t.Fatal("state-only snapshot claimed software recovery")
	}
	if _, err = os.Stat(old.Directory); err != nil {
		t.Fatal("state-only snapshot evicted software recovery")
	}
	current, err := os.ReadFile(filepath.Join(root, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), filepath.Base(old.Directory)) {
		t.Fatal("state-only snapshot replaced complete pointer")
	}
}

func TestRecoveryPruneOwnership(t *testing.T) {
	root := recoveryRoot(t)
	source := snapshotSource(t)
	unrelated := filepath.Join(root, "set-unrelated")
	if err := os.Mkdir(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	first := publishedSnapshot(t, root, source)
	second := publishedSnapshot(t, root, source)
	if _, err := os.Stat(first.Directory); !os.IsNotExist(err) {
		t.Fatal("previous owned set not pruned after successful replacement")
	}
	if _, err := os.Stat(second.Directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("unrelated directory removed")
	}
	pointer, _ := json.Marshal(map[string]string{"id": "../../outside"})
	if err := os.WriteFile(filepath.Join(root, "current.json"), pointer, 0600); err != nil {
		t.Fatal(err)
	}
	stage, manifest := stagedSnapshot(t, root, source, true)
	if _, err := publishRecovery(context.Background(), root, stage, manifest); err == nil {
		t.Fatal("unsafe prune pointer accepted")
	}
}

func TestSnapshotRefusesOwnershipLinks(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(map[bool]string{false: "staging", true: "previous"}[previous], func(t *testing.T) {
			root := recoveryRoot(t)
			source := snapshotSource(t)
			stage, manifest := stagedSnapshot(t, root, source, true)
			owner := filepath.Join(stage, "owner")
			if previous {
				current, err := publishRecovery(context.Background(), root, stage, manifest)
				if err != nil {
					t.Fatal(err)
				}
				owner = filepath.Join(current.Directory, "owner")
				stage, manifest = stagedSnapshot(t, root, source, true)
			}
			external := filepath.Join(root, "external-marker")
			if err := os.WriteFile(external, []byte(recoveryMarker), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(owner); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, owner); err != nil {
				t.Fatal(err)
			}
			if _, err := publishRecovery(context.Background(), root, stage, manifest); err == nil {
				t.Fatal("accepted ownership evidence through a symlink")
			}
		})
	}
}

func TestSnapshotWALWriter(t *testing.T) {
	root := os.Getenv("ANDARU_SNAPSHOT_WAL_ROOT")
	if root == "" {
		t.Skip("subprocess fixture only")
	}
	store, err := config.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Save(context.Background(), "unix:uid:0", config.Changes{Operations: []config.Operation{{Op: "create", Collection: "zones", Value: json.RawMessage(`{"name":"wal-zone"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestSnapshotFromCrashedStore(t *testing.T) {
	source := snapshotSource(t)
	child := exec.Command(os.Args[0], "-test.run=^TestSnapshotWALWriter$")
	child.Env = append(os.Environ(), "ANDARU_SNAPSHOT_WAL_ROOT="+source)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("WAL fixture: %v %s", err, out)
	}
	root := recoveryRoot(t)
	stage, manifest := stagedSnapshot(t, root, source, true)
	result, err := publishRecovery(context.Background(), root, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	var stored recoveryManifest
	if err = privateJSON(filepath.Join(result.Directory, "manifest.json"), &stored); err != nil {
		t.Fatal(err)
	}
	if err = verifyCopiedFiles(context.Background(), filepath.Join(result.Directory, "state"), stored.Files); err != nil {
		for _, file := range stored.Files {
			got, readErr := hashFile(context.Background(), filepath.Join(result.Directory, "state", file.Path))
			if readErr != nil || got.SHA256 != file.SHA256 {
				t.Logf("changed after SQLite verification: %s (%v)", file.Path, readErr)
			}
		}
		t.Fatalf("published manifest no longer matches state: %v", err)
	}
	if err = config.VerifyRecoveryState(context.Background(), filepath.Join(result.Directory, "state")); err != nil {
		t.Fatal(err)
	}
}

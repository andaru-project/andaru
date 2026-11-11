//go:build integration

package config

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

//go:embed testdata/changes.json
var requestFixture []byte

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--store-child" {
		if e := protocolChild(os.Args[2], os.Args[3]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "--store-enosp" {
		if e := enospChild(); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "--store-unprivileged" {
		s, e := Open(context.Background(), os.Args[2])
		if e == nil {
			s.Close()
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "integration suite requires root")
		os.Exit(1)
	}
	os.Exit(m.Run())
}
func ownedDir(t *testing.T) string {
	t.Helper()
	p, e := os.MkdirTemp("/var/tmp", "andaru-store-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(p) })
	return p
}
func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, e := Open(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func networkInput(t *testing.T) Changes {
	t.Helper()
	c, e := DecodeChanges(requestFixture)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func createZone(name string) Changes {
	return Changes{Operations: []Operation{{Op: "create", Collection: "zones", Value: json.RawMessage(`{"name":"` + name + `"}`)}}}
}

func TestStorePreparationCancellation(t *testing.T) {
	s := openTestStore(t, ownedDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Save(ctx, "unix:uid:0", createZone("cancelled")); err == nil {
		t.Fatal("cancelled preparation published")
	}
	head, err := s.Snapshot(context.Background())
	if err != nil || head.SavedVersion != "0" || s.fenced.Load() {
		t.Fatalf("cancelled preparation changed state or fenced: %v %+v", err, head)
	}
	if result, err := s.Save(context.Background(), "unix:uid:0", createZone("healthy")); err != nil || result.SavedVersion != "1" {
		t.Fatalf("cancelled preparation poisoned next save: %v %+v", err, result)
	}
}

func TestStorePublicationOutlivesRequest(t *testing.T) {
	s := openTestStore(t, ownedDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := s.prepareSave(ctx, "unix:uid:0", networkInput(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cancel()
	result, err := p.publish(ctx)
	if err != nil || result.SavedVersion != "1" {
		t.Fatalf("request cancellation interrupted prepared publication: %v %+v", err, result)
	}
	p.Close()
	head, err := s.Snapshot(context.Background())
	if err != nil || head.SavedVersion != "1" {
		t.Fatalf("detached publication not durable: %v %+v", err, head)
	}
}

func TestInitializationInterruptedMarker(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint("extra-data-", foreign), func(t *testing.T) {
			root := ownedDir(t)
			stage := filepath.Join(root, ".configuration-init")
			if err := os.Mkdir(stage, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stage, "init.marker"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if foreign {
				if err := os.WriteFile(filepath.Join(stage, "config.db"), []byte("DO_NOT_DELETE"), 0600); err != nil {
					t.Fatal(err)
				}
				if store, err := Open(context.Background(), root); err == nil {
					store.Close()
					t.Fatal("accepted unmarked data")
				}
				raw, err := os.ReadFile(filepath.Join(stage, "config.db"))
				if err != nil || string(raw) != "DO_NOT_DELETE" {
					t.Fatal("modified unmarked data")
				}
			} else {
				store := openTestStore(t, root)
				head, err := store.Snapshot(context.Background())
				if err != nil || head.SavedVersion != "0" {
					t.Fatalf("marker-only interruption did not recover: %v %+v", err, head)
				}
			}
		})
	}
}

func TestCollectionReadsOneSavedVersion(t *testing.T) {
	s := openTestStore(t, ownedDir(t))
	initial, err := s.Save(context.Background(), "unix:uid:0", Changes{Operations: []Operation{
		{Op: "create", Collection: "zones", LocalID: "a", Value: json.RawMessage(`{"name":"a-0"}`)},
		{Op: "create", Collection: "zones", LocalID: "b", Value: json.RawMessage(`{"name":"b-0"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	writes := make(chan error, 1)
	go func() {
		<-start
		version := initial.SavedVersion
		for i := 1; i <= 20; i++ {
			result, err := s.Save(context.Background(), "unix:uid:0", Changes{Operations: []Operation{
				{Op: "replace", Collection: "zones", ID: initial.Created["a"], ExpectedVersion: &version, Value: json.RawMessage(fmt.Sprintf(`{"name":"a-%d"}`, i))},
				{Op: "replace", Collection: "zones", ID: initial.Created["b"], ExpectedVersion: &version, Value: json.RawMessage(fmt.Sprintf(`{"name":"b-%d"}`, i))},
			}})
			if err != nil {
				writes <- err
				return
			}
			version = result.SavedVersion
		}
		writes <- nil
	}()
	close(start)
	for i := 0; i < 30; i++ {
		view, err := s.List(context.Background(), "zones")
		if err != nil || len(view.Items) != 2 {
			t.Fatalf("collection read: %v %+v", err, view)
		}
		for _, object := range view.Items {
			if object.Version != view.SavedVersion {
				t.Fatalf("mixed object/head versions: %+v", view)
			}
			var zone struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(object.Value, &zone); err != nil {
				t.Fatal(err)
			}
			var n int
			if _, err := fmt.Sscanf(zone.Name[2:], "%d", &n); err != nil || fmt.Sprint(n+1) != string(view.SavedVersion) {
				t.Fatalf("mixed object contents: %+v", view)
			}
		}
	}
	if err := <-writes; err != nil {
		t.Fatal(err)
	}
}

func TestStoreInitializationAndPaths(t *testing.T) {
	root := ownedDir(t)
	path := filepath.Join(root, "state")
	s := openTestStore(t, path)
	v, e := s.Snapshot(context.Background())
	if e != nil || v.SavedVersion != "0" {
		t.Fatalf("initial state: %v %+v", e, v)
	}
	for _, p := range []string{path, filepath.Join(path, "configuration")} {
		st, e := os.Stat(p)
		if e != nil || st.Mode().Perm() != 0700 {
			t.Fatalf("unsafe dir %s: %v", p, e)
		}
	}
	for _, p := range []string{"engine.lock", "configuration/init.marker", "configuration/config.db", "configuration/secrets.db"} {
		st, e := os.Stat(filepath.Join(path, p))
		if e != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("unsafe file %s: %v", p, e)
		}
	}
	if other, e := Open(context.Background(), path); e == nil {
		other.Close()
		t.Fatal("second Store acquired same state")
	}
	s.Close()
	s = openTestStore(t, path)
	if _, e = s.Snapshot(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.Close()
	t.Run("existing empty published directory", func(t *testing.T) {
		p := filepath.Join(ownedDir(t), "state")
		os.Mkdir(p, 0700)
		os.Mkdir(filepath.Join(p, "configuration"), 0700)
		if v, e := Open(context.Background(), p); e == nil {
			v.Close()
			t.Fatal("overwrote empty published directory")
		}
		entries, _ := os.ReadDir(filepath.Join(p, "configuration"))
		if len(entries) != 0 {
			t.Fatal("mutated refused directory")
		}
	})
	t.Run("permissive state root", func(t *testing.T) {
		p := ownedDir(t)
		os.Chmod(p, 0755)
		if v, e := Open(context.Background(), p); e == nil {
			v.Close()
			t.Fatal("accepted unsafe root")
		}
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0755 {
			t.Fatal("changed existing permissions")
		}
	})
	t.Run("symlink state", func(t *testing.T) {
		r := ownedDir(t)
		p := filepath.Join(r, "link")
		os.Symlink(filepath.Join(r, "target"), p)
		if v, e := Open(context.Background(), p); e == nil {
			v.Close()
			t.Fatal("accepted symlink")
		}
	})
	t.Run("missing member", func(t *testing.T) {
		p := ownedDir(t)
		v := openTestStore(t, p)
		v.Close()
		target := filepath.Join(p, "configuration/secrets.db")
		if e := os.Remove(target); e != nil {
			t.Fatal(e)
		}
		if v, e := Open(context.Background(), p); e == nil {
			v.Close()
			t.Fatal("reinitialized missing member")
		}
		if _, e := os.Stat(target); !os.IsNotExist(e) {
			t.Fatal("recreated missing secret store")
		}
	})
	t.Run("corrupt head", func(t *testing.T) {
		p := ownedDir(t)
		v := openTestStore(t, p)
		if _, e := v.configDB.Exec("UPDATE documents SET body='corrupt'"); e != nil {
			t.Fatal(e)
		}
		v.Close()
		if v, e := Open(context.Background(), p); e == nil {
			v.Close()
			t.Fatal("accepted corrupt document")
		}
	})
	t.Run("future layout remains untouched", func(t *testing.T) {
		p := ownedDir(t)
		v := openTestStore(t, p)
		if _, e := v.configDB.Exec("PRAGMA user_version=2"); e != nil {
			t.Fatal(e)
		}
		v.Close()
		if other, e := Open(context.Background(), p); e == nil {
			other.Close()
			t.Fatal("accepted future layout")
		}
		db, e := openDatabase(context.Background(), filepath.Join(p, "configuration/config.db"), "ro", false)
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close()
		var version int
		if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 2 {
			t.Fatal("rewrote future layout")
		}
	})
	t.Run("foreign application is not converted", func(t *testing.T) {
		p := ownedDir(t)
		v := openTestStore(t, p)
		v.Close()
		file := filepath.Join(p, "configuration/config.db")
		db, e := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: file, RawQuery: "mode=rw"}).String())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec("PRAGMA application_id=123"); e != nil {
			t.Fatal(e)
		}
		var mode string
		if e = db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); e != nil {
			t.Fatal(e)
		}
		db.Close()
		if other, e := Open(context.Background(), p); e == nil {
			other.Close()
			t.Fatal("accepted foreign app")
		}
		db, e = openDatabase(context.Background(), file, "ro", false)
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close()
		if e = db.QueryRow("PRAGMA journal_mode").Scan(&mode); e != nil || mode != "delete" {
			t.Fatal("converted foreign journal mode")
		}
	})
	t.Run("unexpected member survives", func(t *testing.T) {
		p := ownedDir(t)
		v := openTestStore(t, p)
		v.Close()
		file := filepath.Join(p, "configuration/foreign")
		if e := os.WriteFile(file, []byte("KEEP"), 0600); e != nil {
			t.Fatal(e)
		}
		if other, e := Open(context.Background(), p); e == nil {
			other.Close()
			t.Fatal("accepted unknown member")
		}
		if raw, e := os.ReadFile(file); e != nil || string(raw) != "KEEP" {
			t.Fatal("removed unrelated state")
		}
	})
	t.Run("hardlink refused", func(t *testing.T) {
		p := ownedDir(t)
		v := openTestStore(t, p)
		v.Close()
		if e := os.Link(filepath.Join(p, "configuration/config.db"), filepath.Join(p, "alias")); e != nil {
			t.Fatal(e)
		}
		if other, e := Open(context.Background(), p); e == nil {
			other.Close()
			t.Fatal("accepted aliased database")
		}
	})
	t.Run("nonroot has no filesystem side effect", func(t *testing.T) {
		r := ownedDir(t)
		if err := os.Chmod(r, os.ModeSticky|0777); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(r, "new")
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		input, err := os.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		helperDir := ownedDir(t)
		if err := os.Chmod(helperDir, 0711); err != nil {
			t.Fatal(err)
		}
		helper := filepath.Join(helperDir, "unprivileged-helper")
		output, err := os.OpenFile(helper, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(output, input); err != nil {
			output.Close()
			t.Fatal(err)
		}
		if err := output.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(helper, 0755); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, helper, "--store-unprivileged", p)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}}}
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("child %v %s", e, out)
		}
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			t.Fatal("nonroot touched state")
		}
	})
}

func TestStorePublicationAndConcurrency(t *testing.T) {
	s := openTestStore(t, ownedDir(t))
	r, e := s.Save(context.Background(), "unix:uid:0", networkInput(t))
	if e != nil || r.SavedVersion != "1" {
		t.Fatalf("initial network save: %v", e)
	}
	id := r.Created["l2"]
	got, version, e := s.Get(context.Background(), "links", id)
	if e != nil || version != "1" || got.Version != "1" || strings.Contains(string(got.Value), "TEST_ONLY_FIRST") {
		t.Fatalf("saved object: %v %s", e, got.Value)
	}
	h, e := s.History(context.Background(), nil, 1)
	if e != nil || len(h.Items) != 1 || h.Items[0].Actor != "unix:uid:0" || h.NextBefore == nil {
		t.Fatalf("history: %v %+v", e, h)
	}
	snapshot, e := s.Version(context.Background(), "1")
	if e != nil || strings.Contains(string(snapshot.Value), "TEST_ONLY_FIRST") {
		t.Fatal("history exposed password")
	}
	t.Run("reversed publication", func(t *testing.T) {
		p, e := s.prepareSave(context.Background(), "unix:uid:0", createZone("reverse"))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.commitConfiguration(context.Background()); e == nil {
			t.Fatal("published before secret commit")
		}
		p.Close()
		v, e := s.Snapshot(context.Background())
		if e != nil || v.SavedVersion != "1" {
			t.Fatalf("reversed order changed head: %v", e)
		}
	})
	parent := r.Created["l1"]
	shareBody := []byte(`{"name":"shared-ppp","kind":"pppoe","mtu":1492,"enabled":true,"parent":"` + parent + `","username":"TEST_ONLY","password":{"secret":"WAN_PASSWORD"}}`)
	share, err := ObjectChange("create", "links", "", nil, shareBody)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := s.Save(context.Background(), "unix:uid:0", share)
	if err != nil {
		t.Fatal(err)
	}
	sharedID := shared.Items[0].ID
	var value map[string]any
	if err = json.Unmarshal(got.Value, &value); err != nil {
		t.Fatal(err)
	}
	value["password"] = map[string]any{"secret": "WAN_PASSWORD", "set": "TEST_ONLY_SECOND", "expectedVersion": "1"}
	body, _ := json.Marshal(value)
	change, err := ObjectChange("replace", "links", id, &got.Version, body)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Save(context.Background(), "unix:uid:0", change)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{id, sharedID} {
		view, _, err := s.Get(context.Background(), "links", target)
		if err != nil || view.Version != updated.SavedVersion {
			t.Fatalf("shared referrer version: %v %+v", err, view)
		}
	}
	for version, want := range map[Version]string{"1": "TEST_ONLY_FIRST", updated.SavedVersion: "TEST_ONLY_SECOND"} {
		var hash, raw []byte
		if err = s.configDB.QueryRow("SELECT secrets_sha256 FROM versions WHERE id=?", string(version)).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		if err = s.secretDB.QueryRow("SELECT body FROM documents WHERE sha256=?", hash).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var secrets secretDocument
		if err = json.Unmarshal(raw, &secrets); err != nil {
			t.Fatal(err)
		}
		if secrets.Scopes["core"]["WAN_PASSWORD"].Value != want {
			t.Fatal("history no longer resolves its own password")
		}
	}
	zoneIDs := []string{r.Created["z0"], r.Created["z1"]}
	errs := make(chan error, 2)
	var concurrent sync.WaitGroup
	for i, target := range zoneIDs {
		concurrent.Add(1)
		go func(i int, target string) {
			defer concurrent.Done()
			version := Version("1")
			c, err := ObjectChange("replace", "zones", target, &version, []byte(fmt.Sprintf(`{"name":"independent-%d"}`, i)))
			if err == nil {
				_, err = s.Save(context.Background(), "unix:uid:0", c)
			}
			errs <- err
		}(i, target)
	}
	concurrent.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("independent edit lost: %v", err)
		}
	}
	r, e = s.Save(context.Background(), "unix:uid:0", createZone("race"))
	if e != nil {
		t.Fatal(e)
	}
	var zoneID string
	for _, v := range r.Items {
		if v.Collection == "zones" {
			zoneID = v.ID
		}
	}
	v := r.SavedVersion
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"race-a", "race-b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			c, e := ObjectChange("replace", "zones", zoneID, &v, []byte(`{"name":"`+name+`"}`))
			if e == nil {
				_, e = s.Save(context.Background(), "unix:uid:0", c)
			}
			results <- e
		}(name)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, stales := 0, 0
	for e := range results {
		if e == nil {
			wins++
		} else {
			requireFault(t, e, "PRECONDITION_FAILED")
			stales++
		}
	}
	if wins != 1 || stales != 1 {
		t.Fatalf("lost-update race wins=%d stale=%d", wins, stales)
	}
	current, e := s.Snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	path := s.root
	s.Close()
	s = openTestStore(t, path)
	reopened, e := s.Snapshot(context.Background())
	if e != nil || reopened.SavedVersion != current.SavedVersion {
		t.Fatal("restart lost acknowledged state")
	}
}

func TestStoreConnectionPragmas(t *testing.T) {
	s := openTestStore(t, ownedDir(t))
	for _, entry := range []struct{ secret bool }{{false}, {true}} {
		db := s.configDB
		if entry.secret {
			db = s.secretDB
		}
		db.SetConnMaxLifetime(time.Nanosecond)
		for range 2 {
			c, e := db.Conn(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "2000"} {
				var got string
				if e = c.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); e != nil || got != want {
					t.Fatalf("%s=%s want %s err=%v", pragma, got, want, e)
				}
			}
			if entry.secret {
				var got int
				if e = c.QueryRowContext(context.Background(), "PRAGMA secure_delete").Scan(&got); e != nil || got != 1 {
					t.Fatalf("secret deletion flag %d %v", got, e)
				}
			}
			c.Close()
		}
		db.SetConnMaxLifetime(0)
	}
}

func protocolChild(path, phase string) error {
	ctx := context.Background()
	if strings.HasPrefix(phase, "init-") {
		lock, e := lockStateDir(path)
		if e != nil {
			return e
		}
		defer lock.Close()
		stage, e := newStaging(path)
		if e != nil {
			return e
		}
		if phase != "init-marker" {
			if e = initializePair(ctx, stage); e != nil {
				return e
			}
		}
		if phase == "init-published" {
			if e = publishInitial(stage, filepath.Join(path, "configuration")); e != nil {
				return e
			}
		}
		fmt.Println("READY")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return nil
	}
	s, e := Open(ctx, path)
	if e != nil {
		return e
	}
	defer s.Close()
	c, e := DecodeChanges(requestFixture)
	if e != nil {
		return e
	}
	p, e := s.prepareSave(ctx, "unix:uid:0", c)
	if e != nil {
		return e
	}
	defer p.Close()
	if phase == "secret" || phase == "config" {
		if e = p.commitSecrets(ctx); e != nil {
			return e
		}
	}
	if phase == "config" {
		if _, e = p.commitConfiguration(ctx); e != nil {
			return e
		}
	}
	fmt.Println("READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
	return nil
}
func TestStoreCrashBoundaries(t *testing.T) {
	for _, phase := range []string{"prepare", "secret", "config", "init-marker", "init-staging", "init-published"} {
		t.Run(phase, func(t *testing.T) {
			p := filepath.Join(ownedDir(t), "state")
			if !strings.HasPrefix(phase, "init-") {
				s := openTestStore(t, p)
				s.Close()
			}
			exe, _ := os.Executable()
			cmd := exec.Command(exe, "--store-child", p, phase)
			out, e := cmd.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			in, e := cmd.StdinPipe()
			if e != nil {
				t.Fatal(e)
			}
			defer in.Close()
			cmd.Stderr = os.Stderr
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			scanner := bufio.NewScanner(out)
			if !scanner.Scan() || scanner.Text() != "READY" {
				t.Fatal("child failed before phase acknowledgement")
			}
			if e = cmd.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			if e = cmd.Wait(); e == nil {
				t.Fatal("child was not killed")
			}
			s := openTestStore(t, p)
			v, e := s.Snapshot(context.Background())
			want := Version("0")
			if phase == "config" {
				want = "1"
			}
			if e != nil || v.SavedVersion != want {
				t.Fatalf("crash %s head=%s want=%s err=%v", phase, v.SavedVersion, want, e)
			}
		})
	}
}

func TestStoreENOSPCAndFencing(t *testing.T) {
	exe, _ := os.Executable()
	cmd := exec.Command("unshare", "--mount", "--propagation", "private", exe, "--store-enosp")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("ENOSPC proof: %v\n%s", e, out)
	}
}
func enospChild() error {
	for _, phase := range []string{"secret", "config"} {
		if e := enospPhase(phase); e != nil {
			return fmt.Errorf("%s publication: %w", phase, e)
		}
	}
	return nil
}
func enospPhase(phase string) error {
	root, e := os.MkdirTemp("/var/tmp", "andaru-store-full-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(root)
	if e = syscall.Mount("tmpfs", root, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=8m,mode=0700"); e != nil {
		return e
	}
	defer syscall.Unmount(root, 0)
	path := filepath.Join(root, "state")
	s, e := Open(context.Background(), path)
	if e != nil {
		return e
	}
	defer s.Close()
	c, e := DecodeChanges(requestFixture)
	if e != nil {
		return e
	}
	p, e := s.prepareSave(context.Background(), "unix:uid:0", c)
	if e != nil {
		return e
	}
	if phase == "config" {
		if e = p.commitSecrets(context.Background()); e != nil {
			return e
		}
	}
	filler := filepath.Join(root, "filler")
	f, e := os.Create(filler)
	if e != nil {
		return e
	}
	chunk := make([]byte, 65536)
	for {
		if _, e = f.Write(chunk); e != nil {
			break
		}
	}
	f.Close()
	if !os.IsTimeout(e) && !strings.Contains(e.Error(), "no space left") {
		return e
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	if phase == "secret" {
		e = p.commitSecrets(context.Background())
	} else {
		_, e = p.commitConfiguration(context.Background())
	}
	if e == nil {
		return fmt.Errorf("full filesystem became success")
	}
	p.Close()
	if !strings.Contains(logs.String(), "sqlite_code=13") || !strings.Contains(logs.String(), "stage=") || strings.Contains(logs.String(), "TEST_ONLY_FIRST") {
		return fmt.Errorf("missing safe storage diagnosis: %s", logs.String())
	}
	select {
	case <-s.Failure():
	default:
		return fmt.Errorf("missing failure notification")
	}
	select {
	case <-s.Failure():
		return fmt.Errorf("duplicate failure notification")
	default:
	}
	if _, e = s.Snapshot(context.Background()); e == nil {
		return fmt.Errorf("fenced reads succeeded")
	}
	if _, e = s.Save(context.Background(), "unix:uid:0", createZone("blocked")); e == nil {
		return fmt.Errorf("fenced writes succeeded")
	}
	os.Remove(filler)
	s.Close()
	again, e := Open(context.Background(), path)
	if e != nil {
		return e
	}
	defer again.Close()
	v, e := again.Snapshot(context.Background())
	if e != nil {
		return e
	}
	if v.SavedVersion != "0" && v.SavedVersion != "1" {
		return fmt.Errorf("invalid recovered head")
	}
	return nil
}

func TestStoreRealizationInput(t *testing.T) {
	s := openTestStore(t, ownedDir(t))
	ctx := context.Background()
	r, err := s.Save(ctx, "unix:uid:0", networkInput(t))
	if err != nil {
		t.Fatal(err)
	}
	id := r.Created["l2"]
	got, _, err := s.Get(ctx, "links", id)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(got.Value, &value); err != nil {
		t.Fatal(err)
	}
	value["password"] = map[string]any{"secret": "WAN_PASSWORD", "set": "TEST_ONLY_SECOND", "expectedVersion": "1"}
	body, _ := json.Marshal(value)
	change, err := ObjectChange("replace", "links", id, &got.Version, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, "unix:uid:0", change); err != nil {
		t.Fatal(err)
	}
	head, err := s.RealizationInput(ctx, nil)
	if err != nil || head.SavedVersion != "2" || len(head.Secrets) != 1 || head.Secrets["WAN_PASSWORD"] != "TEST_ONLY_SECOND" {
		t.Fatalf("head input: %v version=%s names=%d", err, head.SavedVersion, len(head.Secrets))
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil || !bytes.Equal(head.Value, snapshot.Value) || bytes.Contains(head.Value, []byte("TEST_ONLY_SECOND")) {
		t.Fatalf("declaration is not the public snapshot: %v", err)
	}
	first := Version("1")
	old, err := s.RealizationInput(ctx, &first)
	if err != nil || old.SavedVersion != "1" || old.Secrets["WAN_PASSWORD"] != "TEST_ONLY_FIRST" {
		t.Fatalf("version 1 did not keep its own secret: %v", err)
	}
	missing := Version("9")
	_, err = s.RealizationInput(ctx, &missing)
	var f *Fault
	if !errors.As(err, &f) || f.Code != "NOT_FOUND" {
		t.Fatalf("unknown version: %v", err)
	}
}

// A store made before the filter gains its table on open; filter objects then persist with their versions and order.
func TestFilterObjectsPersist(t *testing.T) {
	path := ownedDir(t)
	ctx := context.Background()
	s := openTestStore(t, path)
	if _, e := s.Save(ctx, "unix:uid:0", networkInput(t)); e != nil {
		t.Fatal(e)
	}
	s.Close()
	db, e := openDatabase(ctx, filepath.Join(path, "configuration", "config.db"), "rw", false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, "DROP TABLE policy_changes"); e != nil {
		t.Fatal(e)
	}
	db.Close()

	s = openTestStore(t, path)
	zero := Version("0")
	if _, e = s.Save(ctx, "unix:uid:0", Changes{Operations: []Operation{
		{Op: "create", Collection: "policy.forward", LocalID: "a", Value: json.RawMessage(`{"name":"a","enabled":true,"action":"accept","from":[],"to":[],"services":[],"log":false}`)},
		{Op: "create", Collection: "policy.forward", LocalID: "b", Value: json.RawMessage(`{"name":"b","enabled":true,"action":"drop","from":[],"to":[],"services":[],"log":false}`)},
		{Op: "replace", Collection: "nat.outbound", ExpectedVersion: &zero, Value: json.RawMessage(`{"mode":"automatic"}`)},
	}}); e != nil {
		t.Fatalf("save after the table was added: %v", e)
	}
	s.Close()
	s = openTestStore(t, path)
	list, e := s.List(ctx, "policy.forward")
	if e != nil || len(list.Items) != 2 {
		t.Fatalf("rules %+v %v", list, e)
	}
	var r rule
	_ = json.Unmarshal(list.Items[0].Value, &r)
	if r.Name != "a" || list.Items[0].Version == "" {
		t.Fatalf("first rule %+v version %q", r, list.Items[0].Version)
	}
}

//go:build integration

package config

import (
	"bufio"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
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
		os.Chmod(r, 01777)
		p := filepath.Join(r, "new")
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "--store-unprivileged", p)
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
	if phase == "secret" {
		e = p.commitSecrets(context.Background())
	} else {
		_, e = p.commitConfiguration(context.Background())
	}
	if e == nil {
		return fmt.Errorf("full filesystem became success")
	}
	p.Close()
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

package config

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/securefs"
	"golang.org/x/sys/unix"
)

const markerText = "ANDARU_CONFIGURATION_V1\n"

type Store struct {
	root               string
	configDB, secretDB *sql.DB
	lock               *os.File
	gate               chan struct{}
	failure            chan error
	fenced, closed     atomic.Bool
	closeOnce          sync.Once
	closeErr           error
}

func unavailable() *Fault {
	return fault("STORE_UNAVAILABLE", "", "Configuration storage is unavailable; inspect the engine journal.")
}
func (s *Store) Failure() <-chan error { return s.failure }
func (s *Store) fail(stage string, cause error) error {
	if s.fenced.CompareAndSwap(false, true) {
		diagnostic(stage, cause)
		s.failure <- unavailable()
	}
	return unavailable()
}
func (s *Store) acquire(ctx context.Context) (func(), error) {
	if s.closed.Load() || s.fenced.Load() {
		return nil, unavailable()
	}
	select {
	case <-ctx.Done():
		return nil, unavailable()
	case <-s.gate:
	}
	release := func() { s.gate <- struct{}{} }
	if s.closed.Load() || s.fenced.Load() {
		release()
		return nil, unavailable()
	}
	return release, nil
}
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		<-s.gate
		s.closeErr = errors.Join(s.configDB.Close(), s.secretDB.Close(), s.lock.Close())
		s.gate <- struct{}{}
	})
	return s.closeErr
}
func Open(ctx context.Context, stateDir string) (*Store, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("configuration engine requires root")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, e := lockStateDir(stateDir)
	if e != nil {
		return nil, e
	}
	success := false
	defer func() {
		if !success {
			lock.Close()
		}
	}()
	final := filepath.Join(stateDir, "configuration")
	if _, e = os.Lstat(final); os.IsNotExist(e) {
		stage, e := newStaging(stateDir)
		if e != nil {
			return nil, e
		}
		if e = initializePair(ctx, stage); e != nil {
			return nil, diagnostic("initialize-pair", e)
		}
		if e = publishInitial(stage, final); e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	}
	if e = checkMembers(final, true); e != nil {
		return nil, e
	}
	for _, role := range []struct {
		name string
		app  int
	}{{"config.db", configApp}, {"secrets.db", secretApp}} {
		if e = probeDatabase(ctx, filepath.Join(final, role.name), role.app); e != nil {
			return nil, diagnostic("probe-"+role.name, e)
		}
	}
	cfg, e := openDatabase(ctx, filepath.Join(final, "config.db"), "rw", false)
	if e != nil {
		return nil, diagnostic("open-config", e)
	}
	sec, e := openDatabase(ctx, filepath.Join(final, "secrets.db"), "rw", true)
	if e != nil {
		cfg.Close()
		return nil, diagnostic("open-secret", e)
	}
	// A store made before the packet filter lacks policy_changes; adding it is the whole migration.
	if _, e = cfg.ExecContext(ctx, policyDDL+serviceDDL); e != nil {
		cfg.Close()
		sec.Close()
		return nil, diagnostic("open-config-policy-table", e)
	}
	s := &Store{root: stateDir, configDB: cfg, secretDB: sec, lock: lock, gate: make(chan struct{}, 1), failure: make(chan error, 1)}
	s.gate <- struct{}{}
	conn, e := s.configDB.Conn(ctx)
	if e != nil {
		s.Close()
		return nil, diagnostic("open-config-connect", e)
	}
	_, _, e = s.loadState(ctx, conn, "", nil)
	conn.Close()
	if e != nil {
		s.Close()
		return nil, e
	}
	success = true
	return s, nil
}
func lockStateDir(path string) (*os.File, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("state path requires root and a canonical absolute directory")
	}
	if e := securefs.CheckAncestors(filepath.Dir(path)); e != nil {
		return nil, e
	}
	if e := os.Mkdir(path, 0700); e != nil && !os.IsExist(e) {
		return nil, e
	}
	if e := privateDirectory(path); e != nil {
		return nil, e
	}
	fd, e := unix.Open(filepath.Join(path, "engine.lock"), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), "engine.lock")
	info, e := f.Stat()
	if e != nil || !privateFile(info) {
		f.Close()
		return nil, errors.New("unsafe state lock")
	}
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("state is already owned by an engine")
	}
	return f, nil
}
func privateDirectory(path string) error {
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !st.IsDir() || !securefs.RootOwned(st) || st.Mode().Perm() != 0700 {
		return errors.New("state directory must be root-owned 0700 without symlinks")
	}
	return nil
}
func privateFile(st os.FileInfo) bool {
	if st == nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !securefs.RootOwned(st) {
		return false
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
func allowedMember(name string) bool {
	if name == "init.marker" {
		return true
	}
	for _, base := range []string{"config.db", "secrets.db"} {
		if name == base || name == base+"-wal" || name == base+"-shm" || name == base+"-journal" {
			return true
		}
	}
	return false
}
func checkMembers(path string, complete bool) error {
	if e := privateDirectory(path); e != nil {
		return e
	}
	entries, e := os.ReadDir(path)
	if e != nil {
		return e
	}
	present := map[string]bool{}
	for _, entry := range entries {
		if !allowedMember(entry.Name()) {
			return errors.New("unexpected state member; refusing to modify it")
		}
		st, e := os.Lstat(filepath.Join(path, entry.Name()))
		if e != nil || !privateFile(st) {
			return errors.New("unsafe state member")
		}
		present[entry.Name()] = true
	}
	if !present["init.marker"] {
		return errors.New("state ownership marker is missing")
	}
	raw, e := os.ReadFile(filepath.Join(path, "init.marker"))
	if e != nil || string(raw) != markerText {
		return errors.New("invalid state ownership marker")
	}
	if complete && (!present["config.db"] || !present["secrets.db"]) {
		return errors.New("configuration store pair is incomplete")
	}
	return nil
}
func newStaging(root string) (string, error) {
	stage := filepath.Join(root, ".configuration-init")
	if _, e := os.Lstat(filepath.Join(root, "configuration")); e == nil {
		return "", errors.New("published state already exists")
	} else if !os.IsNotExist(e) {
		return "", e
	}
	if _, e := os.Lstat(stage); e == nil {
		if e = privateDirectory(stage); e != nil {
			return "", e
		}
		entries, e := os.ReadDir(stage)
		if e != nil {
			return "", e
		}
		emptyMarker := false
		if len(entries) == 1 && entries[0].Name() == "init.marker" {
			st, err := os.Lstat(filepath.Join(stage, "init.marker"))
			emptyMarker = err == nil && privateFile(st) && st.Size() == 0
		}
		if len(entries) > 0 && !emptyMarker {
			if e = checkMembers(stage, false); e != nil {
				return "", e
			}
		}
		if e = os.RemoveAll(stage); e != nil {
			return "", e
		}
	} else if !os.IsNotExist(e) {
		return "", e
	}
	if e := os.Mkdir(stage, 0700); e != nil {
		return "", e
	}
	marker, e := os.OpenFile(filepath.Join(stage, "init.marker"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return "", e
	}
	_, e = marker.WriteString(markerText)
	e = errors.Join(e, marker.Sync(), marker.Close())
	if e != nil {
		return "", e
	}
	return stage, syncDirectory(stage)
}
func publishInitial(stage, final string) error {
	if e := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, final, unix.RENAME_NOREPLACE); e != nil {
		return e
	}
	return syncDirectory(filepath.Dir(final))
}
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

type pendingSave struct {
	s                           *Store
	conn                        *sql.Conn
	release                     func()
	prepared                    preparedChange
	actor                       string
	active, secretReady, closed bool
	deadline                    time.Time
	// head is the saved version the batch was prepared against.
	head Version
}

func (s *Store) prepareSave(ctx context.Context, actor string, input Changes) (*pendingSave, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	release, e := s.acquire(ctx)
	if e != nil {
		return nil, e
	}
	p := &pendingSave{s: s, release: release, actor: actor, deadline: time.Now().Add(2 * time.Second)}
	success := false
	defer func() {
		if !success {
			p.Close()
		}
	}()
	if !strings.HasPrefix(actor, "unix:uid:") || len(actor) > 64 {
		return nil, fault("INVALID_REQUEST", "", "Invalid verified actor.")
	}
	p.conn, e = s.configDB.Conn(ctx)
	if e != nil {
		return nil, unavailable()
	}
	if e = verifyPragmas(ctx, p.conn, false); e != nil {
		return nil, unavailable()
	}
	if _, e = p.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
		p.conn.Raw(func(any) error { return driver.ErrBadConn })
		return nil, unavailable()
	}
	p.active = true
	requested := make([]objectKey, 0, len(input.Operations))
	for _, op := range input.Operations {
		if op.Op != "create" {
			requested = append(requested, objectKey{op.Collection, op.ID})
		}
	}
	current, _, e := s.loadState(ctx, p.conn, "", requested)
	if e != nil {
		return nil, e
	}
	p.head = current.Version
	n, e := strconvVersion(current.Version)
	if e != nil || n == int64(^uint64(0)>>1) {
		return nil, unavailable()
	}
	p.prepared, e = prepareChanges(&current, input, Version(fmt.Sprint(n+1)), time.Now().UTC())
	if e != nil {
		return nil, e
	}
	if ctx.Err() != nil {
		return nil, unavailable()
	}
	p.deadline = time.Now().Add(2 * time.Second)
	success = true
	return p, nil
}
func (p *pendingSave) commitSecrets(ctx context.Context) error {
	if p.closed || !p.active {
		return unavailable()
	}
	ctx, cancel := context.WithDeadline(ctx, p.deadline)
	defer cancel()
	conn, e := p.s.secretDB.Conn(ctx)
	if e != nil {
		return p.s.fail("publish-secret-connect", e)
	}
	defer conn.Close()
	if e = verifyPragmas(ctx, conn, true); e != nil {
		return p.s.fail("publish-secret-pragmas", e)
	}
	if _, e = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
		return p.s.fail("publish-secret-begin", e)
	}
	ok := false
	defer func() {
		if !ok {
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if e = putDocument(ctx, conn, p.prepared.SecretBody); e != nil {
		return p.s.fail("publish-secret-document", e)
	}
	if _, e = conn.ExecContext(ctx, "COMMIT"); e != nil {
		return p.s.fail("publish-secret-commit", e)
	}
	ok = true
	p.secretReady = true
	return nil
}
func (p *pendingSave) commitConfiguration(ctx context.Context) (SaveResult, error) {
	if p.closed || !p.active || !p.secretReady {
		return SaveResult{}, unavailable()
	}
	ctx, cancel := context.WithDeadline(ctx, p.deadline)
	defer cancel()
	if e := publishVersion(ctx, p.conn, p.prepared, p.actor); e != nil {
		return SaveResult{}, p.s.fail("publish-config-version", e)
	}
	if _, e := p.conn.ExecContext(ctx, "COMMIT"); e != nil {
		return SaveResult{}, p.s.fail("publish-config-commit", e)
	}
	p.active = false
	return resultView(p.prepared)
}
func (p *pendingSave) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var result error
	if p.conn != nil {
		if p.active {
			if p.s.fenced.Load() {
				p.conn.Raw(func(any) error { return driver.ErrBadConn })
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, e := p.conn.ExecContext(ctx, "ROLLBACK")
				cancel()
				if e != nil {
					p.conn.Raw(func(any) error { return driver.ErrBadConn })
					result = p.s.fail("prepare-rollback", e)
				}
			}
		}
		p.conn.Close()
	}
	if p.release != nil {
		p.release()
	}
	return result
}
func (s *Store) Save(ctx context.Context, actor string, input Changes) (result SaveResult, err error) {
	p, e := s.prepareSave(ctx, actor, input)
	if e != nil {
		return result, e
	}
	defer func() {
		if e := p.Close(); e != nil {
			result = SaveResult{}
			err = e
		}
	}()
	if p.prepared.Noop {
		return resultView(p.prepared)
	}
	return p.publish(ctx)
}

func (p *pendingSave) publish(ctx context.Context) (SaveResult, error) {
	pub, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := p.commitSecrets(pub); err != nil {
		return SaveResult{}, err
	}
	return p.commitConfiguration(pub)
}

// Draft prepares a change batch against the saved head exactly as Save does and returns
// the declaration it would produce, without publishing anything. A draft is only shown,
// so it may not carry a secret value.
func (s *Store) Draft(ctx context.Context, input Changes) (RealizationInput, error) {
	for i, op := range input.Operations {
		var value map[string]json.RawMessage
		if json.Unmarshal(op.Value, &value) == nil && value["password"] != nil {
			return RealizationInput{}, fault("VALIDATION_FAILED", input.path(i, "/password", true), "A draft cannot carry a password.")
		}
	}
	p, e := s.prepareSave(ctx, "unix:uid:0", input)
	if e != nil {
		return RealizationInput{}, e
	}
	defer p.Close()
	view, e := publicSnapshot(p.prepared.State)
	if e != nil {
		return RealizationInput{}, e
	}
	result := RealizationInput{SavedVersion: p.head, Value: view.Value, Secrets: map[string]string{}}
	for _, l := range p.prepared.State.Config.Links {
		if l.Password != nil {
			result.Secrets[l.Password.Secret] = p.prepared.State.Secrets.Scopes["core"][l.Password.Secret].Value
		}
	}
	return result, nil
}

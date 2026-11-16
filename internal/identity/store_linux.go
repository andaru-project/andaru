package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/securefs"
	"golang.org/x/sys/unix"
)

type store struct { db *sql.DB }

func Open(ctx context.Context, stateDir string) (*store, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, errors.New("identity store requires root and canonical absolute state directory")
	}
	if err := securefs.CheckAncestors(filepath.Dir(stateDir)); err != nil { return nil, err }
	if err := os.Mkdir(stateDir, 0700); err != nil && !os.IsExist(err) { return nil, err }
	if err := privateDirectory(stateDir); err != nil { return nil, err }
	path := filepath.Join(stateDir, "identity.db")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil { return nil, err }
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	f.Close()
	if err != nil || !privateFile(info) { return nil, errors.New("unsafe identity database") }
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(2000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=secure_delete(1)")
	if err != nil { return nil, err }
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(e error) (*store,error) { db.Close(); return nil,e }
	if err = db.PingContext(ctx); err != nil { return fail(err) }
	var app, version int
	if err = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil { return fail(err) }
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil { return fail(err) }
	if app != 0 && app != applicationID { return fail(errors.New("wrong identity application id")) }
	if version > LayoutVersion { return fail(fmt.Errorf("identity layout %d is newer than supported %d", version, LayoutVersion)) }
	if version == 0 {
		tx, e := db.BeginTx(ctx,nil); if e != nil { return fail(e) }
		if _,e=tx.ExecContext(ctx,schema); e == nil { _,e=tx.ExecContext(ctx,fmt.Sprintf("PRAGMA application_id=%d",applicationID)) }
		if e == nil { _,e=tx.ExecContext(ctx,fmt.Sprintf("PRAGMA user_version=%d",LayoutVersion)) }
		if e != nil { tx.Rollback(); return fail(e) }
		if e=tx.Commit(); e != nil { return fail(e) }
	}
	var journal string; var syncMode, foreign, secure int
	if err=db.QueryRowContext(ctx,"PRAGMA journal_mode").Scan(&journal); err!=nil { return fail(err) }
	if err=db.QueryRowContext(ctx,"PRAGMA synchronous").Scan(&syncMode); err!=nil { return fail(err) }
	if err=db.QueryRowContext(ctx,"PRAGMA foreign_keys").Scan(&foreign); err!=nil { return fail(err) }
	if err=db.QueryRowContext(ctx,"PRAGMA secure_delete").Scan(&secure); err!=nil { return fail(err) }
	if journal!="wal" || syncMode!=2 || foreign!=1 || secure!=1 { return fail(errors.New("identity SQLite pragmas mismatch")) }
	return &store{db:db},nil
}

func (s *store) Close() error { return s.db.Close() }
func (s *store) Revision(ctx context.Context) (int64,error) {
	var revision int64
	err:=s.db.QueryRowContext(ctx,"SELECT value FROM metadata WHERE key='revision'").Scan(&revision)
	return revision,err
}
func privateDirectory(path string) error {
	st,err:=os.Lstat(path); if err!=nil{return err}
	if !st.IsDir() || !securefs.RootOwned(st) || st.Mode().Perm()!=0700 { return errors.New("identity state directory must be root-owned 0700") }; return nil
}
func privateFile(st os.FileInfo) bool {
	if st==nil || !st.Mode().IsRegular() || st.Mode().Perm()!=0600 || !securefs.RootOwned(st) { return false }
	stat,ok:=st.Sys().(*syscall.Stat_t); return ok && stat.Nlink==1
}

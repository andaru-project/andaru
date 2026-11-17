package kept

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/andaru-project/andaru/internal/securefs"
)

const Root = "/var/lib/andaru-kept"

type Name string

type Store struct{}

func Open(stateDir string) (*Store, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, errors.New("kept store requires root and a canonical absolute state directory")
	}
	if nested(stateDir, Root) || nested(Root, stateDir) {
		return nil, errors.New("kept root must not contain or sit inside the state directory")
	}
	if err := securefs.CheckAncestors(filepath.Dir(Root)); err != nil {
		return nil, err
	}
	return &Store{}, nil
}

func (s *Store) Put(ctx context.Context, name Name, body []byte, replace bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validName(name) {
		return errors.New("unknown kept copy")
	}
	dir := filepath.Join(Root, string(name))
	final := filepath.Join(dir, string(name)+".json")
	if _, err := os.Lstat(final); err == nil && !replace {
		return errors.New("kept copy already exists")
	}
	if err := os.MkdirAll(Root, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func (s *Store) Read(name Name) ([]byte, error) {
	if !validName(name) {
		return nil, errors.New("unknown kept copy")
	}
	path := filepath.Join(Root, string(name), string(name)+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != 0 || stat.Nlink != 1 || info.Mode().Perm() != 0o600 {
		return nil, errors.New("unsafe kept copy")
	}
	return os.ReadFile(path)
}

func LiveTreeUsable(stateDir string) bool {
	info, err := os.Lstat(filepath.Join(stateDir, "configuration"))
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func validName(name Name) bool {
	switch name {
	case "recovery", "first", "last-lan":
		return true
	default:
		return false
	}
}

func nested(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return true
	}
	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

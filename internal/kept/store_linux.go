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

type Store struct{ root string }

func Open(stateDir string) (*Store, error) { return openAt(Root, stateDir, true) }

// OpenIn is the test seam. Production code uses Open.
func OpenIn(root, stateDir string) (*Store, error) { return openAt(root, stateDir, false) }

func openAt(root, stateDir string, production bool) (*Store, error) {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("kept store requires canonical absolute paths")
	}
	if production && os.Geteuid() != 0 {
		return nil, errors.New("kept store requires root and a canonical absolute state directory")
	}
	if nested(stateDir, root) || nested(root, stateDir) {
		return nil, errors.New("kept root must not contain or sit inside the state directory")
	}
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("kept root must be a real directory")
		}
		if production {
			if err = securefs.CheckAncestors(root); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else if production {
		if err = securefs.CheckAncestors(filepath.Dir(root)); err != nil {
			return nil, err
		}
	}
	return &Store{root: root}, nil
}

func (s *Store) Put(ctx context.Context, name Name, body []byte, replace bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validName(name) {
		return errors.New("unknown kept copy")
	}
	dir := filepath.Join(s.root, string(name))
	final := filepath.Join(dir, string(name)+".json")
	if info, err := os.Lstat(final); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("kept copy must be a real file")
	} else if err == nil && !replace {
		return errors.New("kept copy already exists")
	}
	if err := s.privateDir(s.root, true); err != nil {
		return err
	}
	if err := s.privateDir(dir, true); err != nil {
		return err
	}
	tmp := final + ".tmp"
	if info, err := os.Lstat(tmp); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("kept copy temp must be a real file")
		}
		if err = os.Remove(tmp); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
	dir := filepath.Join(s.root, string(name))
	if err := s.privateDir(dir, false); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, string(name)+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	owner := stat.Uid == 0 || s.root != Root && stat.Uid == uint32(os.Geteuid())
	if !info.Mode().IsRegular() || !ok || !owner || stat.Nlink != 1 || info.Mode().Perm() != 0o600 {
		return nil, errors.New("unsafe kept copy")
	}
	return os.ReadFile(path)
}

func (s *Store) privateDir(path string, create bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if !create {
			return err
		}
		return os.Mkdir(path, 0o700)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("kept copy directory must be a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	owner := ok && (stat.Uid == 0 || s.root != Root && stat.Uid == uint32(os.Geteuid()))
	if !owner || info.Mode().Perm()&0022 != 0 {
		return errors.New("kept copy directory is not private")
	}
	return nil
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

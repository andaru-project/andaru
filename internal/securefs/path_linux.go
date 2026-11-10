package securefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func RootOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

// A private leaf is insufficient if another user can rename an ancestor.
func CheckAncestors(path string) error {
	for {
		info, e := os.Lstat(path)
		if e != nil {
			return fmt.Errorf("inspect owned path ancestor: %w", e)
		}
		if !info.IsDir() || !RootOwned(info) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return errors.New("ancestors must be canonical root-controlled directories")
		}
		if path == "/" {
			return nil
		}
		path = filepath.Dir(path)
	}
}

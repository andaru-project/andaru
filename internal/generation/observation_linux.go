package generation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/securefs"
	"golang.org/x/sys/unix"
)

const observationLockWait = 3 * time.Second

// Observation is the generation provenance captured while the generation lock is held.
type Observation struct {
	Live             *int
	Pending          *int
	LiveSavedVersion string
}

// Observe captures generation provenance and invokes capture while the generation lock is held.
// It initializes only the private generation directory and lock file when they do not yet exist.
func Observe(ctx context.Context, dir string, capture func(Observation) error) error {
	if capture == nil {
		return errors.New("generation capture is required")
	}
	if os.Geteuid() != 0 || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("generation path requires root and a canonical absolute directory")
	}
	if err := securefs.CheckAncestors(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !securefs.RootOwned(info) || info.Mode().Perm() != 0o700 {
		return errors.New("generation directory must be root-owned 0700 without symlinks")
	}
	unlock, err := observeLock(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()

	pointers, err := readPointers(dir)
	if err != nil {
		return err
	}
	result := Observation{Live: cloneInt(pointers.Live), Pending: cloneInt(pointers.Pending)}
	if pointers.Live != nil {
		live, err := read(dir, *pointers.Live, pointers)
		if err != nil {
			return err
		}
		result.LiveSavedVersion = live.SavedVersion
	}
	if pointers.Pending != nil && !is(pointers.Live, *pointers.Pending) {
		if _, err := read(dir, *pointers.Pending, pointers); err != nil {
			return err
		}
	}
	return capture(result)
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func observeLock(ctx context.Context, dir string) (func(), error) {
	fd, err := unix.Open(filepath.Join(dir, "lock"), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "generation lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !securefs.RootOwned(info) {
		file.Close()
		return nil, errors.New("unsafe generation lock")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("unsafe generation lock")
	}
	wait, cancel := context.WithTimeout(ctx, observationLockWait)
	defer cancel()
	for {
		if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return func() { _ = file.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			return nil, fmt.Errorf("lock generations: %w", err)
		}
		select {
		case <-wait.Done():
			file.Close()
			return nil, fmt.Errorf("lock generations: %w", wait.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

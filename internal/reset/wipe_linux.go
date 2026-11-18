package reset

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// wipe deletes the contents of stateDir, never the directory itself, which may be a mount point, and never keep.
// It first proves every entry is on stateDir's own mount, bind mounts of the same filesystem included, so nothing
// mounted inside is emptied. It follows no symlink. step is called after each removal, for crash tests.
func wipe(stateDir, keep string, step func() error) error {
	if step == nil {
		step = func() error { return nil }
	}
	root, err := unix.Open(stateDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open the state directory: %w", err)
	}
	defer unix.Close(root)
	var st unix.Statx_t
	if err = unix.Statx(root, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &st); err != nil {
		return err
	}
	if st.Mask&unix.STATX_MNT_ID == 0 {
		return errors.New("the kernel does not report mount IDs; the wipe cannot prove it stays on one mount")
	}
	if err = walk(root, st.Mnt_id, keep, nil); err != nil {
		return err
	}
	if err = walk(root, st.Mnt_id, keep, step); err != nil {
		return err
	}
	return unix.Fsync(root)
}

// walk checks the tree under dir, or, with remove set, removes it depth first.
func walk(dir int, mount uint64, keep string, remove func() error) error {
	names, err := entries(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == keep {
			continue
		}
		var st unix.Statx_t
		if err := unix.Statx(dir, name, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE|unix.STATX_MNT_ID, &st); err != nil {
			return err
		}
		if st.Mnt_id != mount {
			return fmt.Errorf("%s is another mount inside the state directory; unmount it before a factory reset", name)
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			child, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			err = walk(child, mount, "", remove)
			unix.Close(child)
			if err != nil {
				return err
			}
			if remove == nil {
				continue
			}
			if err := unix.Unlinkat(dir, name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
		} else if remove == nil {
			continue
		} else if err := unix.Unlinkat(dir, name, 0); err != nil {
			return err
		}
		if err := remove(); err != nil {
			return err
		}
	}
	return nil
}

func entries(dir int) ([]string, error) {
	fd, err := unix.Dup(dir)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "dir")
	defer f.Close()
	// The duplicate shares the offset a previous pass left at the end.
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	return f.Readdirnames(-1)
}

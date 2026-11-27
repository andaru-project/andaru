package addon

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ReadBoundedLogFile opens a manifest-declared log file inside an add-on's storage directory using
// openat2(RESOLVE_IN_ROOT|RESOLVE_NO_SYMLINKS) with O_NOFOLLOW|O_NONBLOCK so symlinks and container-created
// FIFOs can neither escape the storage directory nor hang the caller.
func ReadBoundedLogFile(stateDir, slug, storage, relPath string, maxLines, maxBytes int) ([]string, bool, error) {
	if stateDir == "" || !ValidSlug(slug) || !filepath.IsLocal(relPath) || maxLines < 1 || maxBytes < 1 {
		return nil, false, errors.New("invalid log file parameters")
	}
	storageDir := filepath.Join(stateDir, "addons", slug, storage)
	dirFD, err := unix.Open(storageDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return []string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer unix.Close(dirFD)

	fd, err := unix.Openat2(dirFD, relPath, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_SYMLINKS,
	})
	if errors.Is(err, unix.ENOENT) {
		return []string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	f := os.NewFile(uintptr(fd), relPath)
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("log path is not a regular file")
	}
	truncated := false
	size := info.Size()
	readLimit := int64(maxBytes)
	if size > int64(maxBytes) {
		// Seek one byte earlier so we can see whether the retained window begins right after '\n'.
		if _, err := f.Seek(size-int64(maxBytes)-1, io.SeekStart); err != nil {
			return nil, false, err
		}
		readLimit = int64(maxBytes) + 1
		truncated = true
	}
	raw, err := io.ReadAll(io.LimitReader(f, readLimit))
	if err != nil {
		return nil, false, err
	}
	if truncated && len(raw) > 0 {
		startsOnLineBoundary := raw[0] == '\n'
		raw = raw[1:]
		if !startsOnLineBoundary {
			if nl := bytes.IndexByte(raw, '\n'); nl >= 0 {
				raw = raw[nl+1:]
			}
		}
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return []string{}, truncated, nil
	}
	all := strings.Split(text, "\n")
	if len(all) > maxLines {
		all = all[len(all)-maxLines:]
		truncated = true
	}
	return all, truncated, nil
}

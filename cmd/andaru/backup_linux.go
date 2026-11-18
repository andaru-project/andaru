package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"

	"github.com/andaru-project/andaru/internal/backup"
)

var (
	openBackupTTY         = func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR|unix.O_CLOEXEC, 0) }
	backupFsync           = unix.Fsync
	backupRenameNoReplace = func(oldDir int, oldName string, newDir int, newName string) error {
		return unix.Renameat2(oldDir, oldName, newDir, newName, unix.RENAME_NOREPLACE)
	}
)

func backupTerminalAvailable() bool {
	tty, err := openBackupTTY()
	if err != nil {
		return false
	}
	defer tty.Close()
	return term.IsTerminal(tty.Fd())
}

func readBackupPassphrase(ctx context.Context, prompt string) (string, error) {
	value, err := readTerminalLine(ctx, prompt, true)
	if err != nil {
		return "", err
	}
	if err := backup.ValidatePassphrase(value); err != nil {
		return "", err
	}
	return value, nil
}

// readTerminalLine reads one bounded line from the controlling terminal, hidden when asked; it may be empty.
func readTerminalLine(ctx context.Context, prompt string, hidden bool) (value string, returnErr error) {
	tty, err := openBackupTTY()
	if err != nil {
		return "", err
	}
	defer func() {
		if err := tty.Close(); err != nil {
			value, returnErr = "", errors.Join(returnErr, err)
		}
	}()
	fd := tty.Fd()
	if !term.IsTerminal(fd) {
		return "", errors.New("not a terminal")
	}
	initial, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	settings, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	if err != nil {
		return "", err
	}
	if hidden {
		settings.Lflag &^= unix.ECHO | unix.ECHONL
	}
	if err = unix.IoctlSetTermios(int(fd), unix.TCSETS, settings); err != nil {
		return "", err
	}
	flushInput := true
	defer func() {
		var flushErr error
		if flushInput {
			flushErr = unix.IoctlSetInt(int(fd), unix.TCFLSH, unix.TCIFLUSH)
		}
		restoreErr := term.Restore(fd, initial)
		if flushErr != nil || restoreErr != nil {
			value, returnErr = "", errors.Join(returnErr, flushErr, restoreErr)
		}
	}()
	if _, err = io.WriteString(tty, prompt); err != nil {
		return "", err
	}
	raw, readErr := readBoundedTerminalLine(ctx, int(fd))
	flushInput = readErr != nil
	var newlineErr error
	if hidden {
		// Echo is off, so the operator's Enter did not move the cursor.
		_, newlineErr = io.WriteString(tty, "\n")
	}
	if readErr != nil {
		clear(raw)
		return "", readErr
	}
	value = string(raw)
	clear(raw)
	if newlineErr != nil {
		return "", newlineErr
	}
	return value, nil
}

func readBoundedTerminalLine(ctx context.Context, fd int) ([]byte, error) {
	raw := make([]byte, 0, backup.MaxPassphraseBytes)
	var chunk [256]byte
	tooLong := false
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
	for {
		if err := ctx.Err(); err != nil {
			clear(raw)
			return nil, err
		}
		ready, err := unix.Poll(poll, 50)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			clear(raw)
			return nil, err
		}
		if ready == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			clear(raw)
			return nil, err
		}
		n, err := unix.Read(fd, chunk[:])
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			clear(raw)
			return nil, err
		}
		if n == 0 {
			clear(raw)
			return nil, io.EOF
		}
		for _, b := range chunk[:n] {
			if b == '\n' {
				if len(raw) > 0 && raw[len(raw)-1] == '\r' {
					raw = raw[:len(raw)-1]
				}
				if tooLong {
					clear(raw)
					return nil, errors.New("passphrase exceeds its size limit")
				}
				return raw, nil
			}
			if tooLong {
				continue
			}
			if len(raw) == backup.MaxPassphraseBytes {
				tooLong = true
				continue
			}
			raw = append(raw, b)
		}
	}
}

func readNewBackupPassphrase(ctx context.Context) (string, error) {
	return readRepeatedBackupSecret(ctx, "New backup passphrase: ", "Repeat backup passphrase: ")
}

func readNewRecoveryPassword(ctx context.Context) (string, error) {
	return readRepeatedBackupSecret(ctx, "New recovery administrator password: ", "Repeat recovery administrator password: ")
}

func readRepeatedBackupSecret(ctx context.Context, firstPrompt, secondPrompt string) (string, error) {
	first, err := readBackupPassphrase(ctx, firstPrompt)
	if err != nil {
		return "", err
	}
	second, err := readBackupPassphrase(ctx, secondPrompt)
	if err != nil || first != second {
		return "", errors.New("secrets do not match")
	}
	return first, nil
}

func newBackupOperationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func readBackupFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > backup.MaxArchiveSize {
		return nil, errors.New("unsafe backup input")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size < 0 || stat.Size > backup.MaxArchiveSize {
		return nil, errors.New("unsafe backup input")
	}
	raw, err := io.ReadAll(io.LimitReader(file, backup.MaxArchiveSize+1))
	if err != nil || len(raw) > backup.MaxArchiveSize || int64(len(raw)) != stat.Size {
		return nil, errors.New("backup input changed or exceeded its size limit")
	}
	return raw, nil
}

func openTrustedBackupParent(path string) (dirfd int, name, absolute string, err error) {
	absolute, err = filepath.Abs(path)
	if err != nil {
		return -1, "", "", err
	}
	absolute = filepath.Clean(absolute)
	name = filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return -1, "", "", errors.New("invalid destination")
	}
	parent := filepath.Dir(absolute)
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", "", err
	}
	for _, component := range splitPath(parent) {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, "", "", openErr
		}
		fd = next
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Uid != 0 || stat.Mode&0022 != 0 {
		unix.Close(fd)
		return -1, "", "", errors.New("backup destination parent is not trusted")
	}
	return fd, name, absolute, nil
}

func splitPath(path string) []string {
	path = strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
	if path == "" || path == "." {
		return nil
	}
	return strings.Split(path, string(filepath.Separator))
}

func publishBackupFile(path string, raw []byte) (string, error) {
	dirfd, name, absolute, err := openTrustedBackupParent(path)
	if err != nil {
		return "", err
	}
	defer unix.Close(dirfd)
	var random [12]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", err
	}
	stage := ".andaru-backup-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(dirfd, stage, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return "", err
	}
	published := false
	defer func() {
		unix.Close(fd)
		if !published {
			_ = unix.Unlinkat(dirfd, stage, 0)
		}
	}()
	if err = unix.Fchmod(fd, 0600); err != nil {
		return "", err
	}
	if err = unix.Fchown(fd, 0, 0); err != nil {
		return "", err
	}
	for len(raw) > 0 {
		n, writeErr := unix.Write(fd, raw)
		if writeErr != nil {
			return "", writeErr
		}
		if n == 0 {
			return "", io.ErrShortWrite
		}
		raw = raw[n:]
	}
	if err = backupFsync(fd); err != nil {
		return "", err
	}
	if err = backupRenameNoReplace(dirfd, stage, dirfd, name); err != nil {
		return "", err
	}
	published = true
	if err = backupFsync(dirfd); err != nil {
		return "", fmt.Errorf("sync backup destination: %w", err)
	}
	return absolute, nil
}

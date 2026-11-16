package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/securefs"
	"golang.org/x/sys/unix"
)

const (
	authorizationLockName       = "authorization.lock"
	authorizationStateName      = "authorization.json"
	authorizationTransitionName = "authorization-transition.json"
	TransitionActivate          = "activate"
	TransitionConfirm           = "confirm"
)

var (
	ErrStateUnavailable     = errors.New("authorization state is unavailable")
	ErrTransitionInProgress = errors.New("authorization transition is in progress")
	digestPattern           = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type State struct {
	LiveDigest         string `json:"liveDigest"`
	AuthorizationEpoch uint64 `json:"authorizationEpoch"`
}

type PendingTransition struct {
	GenerationID        int    `json:"generationId"`
	Phase               string `json:"phase"`
	IdentityRevision    int64  `json:"identityRevision"`
	AuthorizationDigest string `json:"authorizationDigest"`
}

type StateLock struct {
	stateDir string
	file     *os.File
	fresh    bool
}

// Acquire takes the root-owned cross-process authorization lock.
func Acquire(ctx context.Context, stateDir string) (*StateLock, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, errors.New("authorization state requires root and a canonical absolute directory")
	}
	if err := securefs.CheckAncestors(filepath.Dir(stateDir)); err != nil {
		return nil, err
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !securefs.RootOwned(info) {
		return nil, errors.New("authorization state directory must be root-owned 0700 without symlinks")
	}
	lockPath := filepath.Join(stateDir, authorizationLockName)
	fresh := true
	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if errors.Is(err, unix.EEXIST) {
		fresh = false
		fd, err = unix.Open(lockPath, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "authorization lock")
	if err = safePrivateFile(file); err != nil {
		file.Close()
		return nil, err
	}
	for {
		if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return &StateLock{stateDir: stateDir, file: file, fresh: fresh}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			return nil, fmt.Errorf("lock authorization state: %w", err)
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, fmt.Errorf("lock authorization state: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (l *StateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func safePrivateFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !securefs.RootOwned(info) {
		return errors.New("unsafe authorization state file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("unsafe authorization state file")
	}
	return nil
}

func validDigest(digest string) bool { return digest == "" || digestPattern.MatchString(digest) }

func validState(state State) bool {
	return validDigest(state.LiveDigest)
}

func validPending(p PendingTransition) bool {
	return p.GenerationID > 0 && p.IdentityRevision >= 0 && digestPattern.MatchString(p.AuthorizationDigest) &&
		(p.Phase == TransitionActivate || p.Phase == TransitionConfirm)
}

func (l *StateLock) read(name string, value any) (bool, error) {
	if l == nil || l.file == nil {
		return false, ErrStateUnavailable
	}
	fd, err := unix.Open(filepath.Join(l.stateDir, name), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if err = safePrivateFile(file); err != nil {
		return false, err
	}
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return false, ErrStateUnavailable
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false, ErrStateUnavailable
	}
	return true, nil
}

func (l *StateLock) write(name string, value any) (err error) {
	if l == nil || l.file == nil {
		return ErrStateUnavailable
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(l.stateDir, "."+name+"-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporaryName, filepath.Join(l.stateDir, name))
	}
	if err == nil {
		err = syncAuthorizationDir(l.stateDir)
	}
	return err
}

func syncAuthorizationDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (l *StateLock) readState() (State, bool, error) {
	var state State
	found, err := l.read(authorizationStateName, &state)
	if err != nil || !found {
		return State{}, found, err
	}
	if !validState(state) {
		return State{}, false, ErrStateUnavailable
	}
	return state, true, nil
}

func (l *StateLock) Read() (State, error) {
	state, found, err := l.readState()
	if err != nil || !found {
		return State{}, errors.Join(ErrStateUnavailable, err)
	}
	return state, nil
}

// Advance publishes digest and increments the epoch exactly when the digest differs.
func (l *StateLock) Advance(digest string) (State, error) {
	if !validDigest(digest) {
		return State{}, ErrStateUnavailable
	}
	state, found, err := l.readState()
	if err != nil {
		return State{}, err
	}
	if !found {
		if !l.fresh {
			return State{}, ErrStateUnavailable
		}
		state = State{LiveDigest: digest}
		if digest != "" {
			state.AuthorizationEpoch = 1
		}
		if err := l.write(authorizationStateName, state); err != nil {
			return State{}, err
		}
		return state, nil
	}
	if state.LiveDigest == digest {
		return state, nil
	}
	if state.AuthorizationEpoch == uint64(^uint64(0)>>1) {
		return State{}, ErrStateUnavailable
	}
	state.LiveDigest = digest
	state.AuthorizationEpoch++
	if err := l.write(authorizationStateName, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (l *StateLock) Pending() (PendingTransition, bool, error) {
	var pending PendingTransition
	found, err := l.read(authorizationTransitionName, &pending)
	if err != nil || !found {
		return PendingTransition{}, found, err
	}
	if !validPending(pending) {
		return PendingTransition{}, false, ErrStateUnavailable
	}
	return pending, true, nil
}

func (l *StateLock) SetPending(pending PendingTransition) error {
	if !validPending(pending) {
		return ErrStateUnavailable
	}
	if _, found, err := l.Pending(); err != nil {
		return err
	} else if found {
		return ErrTransitionInProgress
	}
	return l.write(authorizationTransitionName, pending)
}

func (l *StateLock) ClearPending(generationID int, phase string) error {
	pending, found, err := l.Pending()
	if err != nil || !found {
		return err
	}
	if pending.GenerationID != generationID || phase != "" && pending.Phase != phase {
		return ErrTransitionInProgress
	}
	if err = os.Remove(filepath.Join(l.stateDir, authorizationTransitionName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncAuthorizationDir(l.stateDir)
}

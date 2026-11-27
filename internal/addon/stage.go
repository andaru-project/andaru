package addon

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// StagedStateFile records staged add-on image digests inside StateDir so the unprivileged engine
	// and host fact probes can observe staged digests without opening Podman storage locks.
	StagedStateFile = "addons-staged.json"
	stagedLockFile  = "addons-staged.lock"
	podmanBinary    = "/usr/bin/podman"
	stageTimeout    = 10 * time.Minute
)

var (
	// ErrAddonNotFound reports a slug absent from the shipped catalog.
	ErrAddonNotFound = errors.New("add-on not found in catalog")
	// ErrImageReferenced reports an attempt to remove an image digest still needed by a retained generation or saved config.
	ErrImageReferenced = errors.New("staged image is referenced by a retained generation or saved configuration")
	// errImageAbsent is a non-OS sentinel for unit-test runners simulating `podman image exists` exit status 1.
	errImageAbsent = errors.New("image absent")
)

// StagedImage records one verified local Podman image digest.
type StagedImage struct {
	Digest   string    `json:"digest"`
	ImageID  string    `json:"imageId"`
	StagedAt time.Time `json:"stagedAt"`
}

// StagedState is the persisted index of staged add-on image digests.
type StagedState struct {
	Images map[string]StagedImage `json:"images"`
}

// Digests returns the sorted list of staged immutable digest references.
func (s StagedState) Digests() []string {
	if len(s.Images) == 0 {
		return []string{}
	}
	return slices.Sorted(maps.Keys(s.Images))
}

// ReadStagedState reads StateDir/addons-staged.json. A missing file returns an empty state.
func ReadStagedState(stateDir string) (StagedState, error) {
	empty := StagedState{Images: map[string]StagedImage{}}
	if stateDir == "" {
		return empty, nil
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, StagedStateFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return empty, nil
		}
		return empty, fmt.Errorf("read staged add-on images: %w", err)
	}
	var st StagedState
	if err := json.Unmarshal(raw, &st); err != nil {
		return empty, fmt.Errorf("decode staged add-on images: %w", err)
	}
	if st.Images == nil {
		st.Images = map[string]StagedImage{}
	}
	return st, nil
}

// CommandRunner executes a host command and returns stdout.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultCommandRunner executes host commands directly.
func DefaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, fmt.Errorf("%s %v: %w: %s", name, args, err, strings.TrimSpace(string(exit.Stderr)))
	}
	return out, err
}

// Stager manages explicit out-of-band Podman image staging and removal for catalog add-ons.
// It runs outside the generations lock during slow pulls so WAN downloads never block apply or rollback.
// WithUnstageLock serializes unstaging with generation publication (generations/lock -> addons-staged.lock);
// the engine HTTP API additionally checks the saved head in config.db.
type Stager struct {
	StateDir         string
	CatalogDir       string
	Run              CommandRunner
	Now              func() time.Time
	ReferencedImages func() (map[string]bool, error)
	WithUnstageLock  func(ctx context.Context, fn func(refs map[string]bool) error) error
}

func (s Stager) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, name, args...)
	}
	return DefaultCommandRunner(ctx, name, args...)
}

func (s Stager) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Stager) imageForSlug(slug string) (string, error) {
	if !ValidSlug(slug) {
		return "", fmt.Errorf("invalid add-on slug %q", slug)
	}
	cat, err := LoadCatalog(cmp.Or(s.CatalogDir, DefaultCatalogDir))
	if err != nil {
		return "", err
	}
	m, ok := cat.BySlug(slug)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrAddonNotFound, slug)
	}
	return m.Runtime.Image, nil
}

func withStagedLock(ctx context.Context, stateDir string, fn func() error) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, stagedLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(ctx, f); err != nil {
		return fmt.Errorf("lock staged add-on state: %w", err)
	}
	return fn()
}

// imageConfirmedAbsent reports whether `podman image exists <imageRef>` confirmed the image is absent
// (exit status 1 with a non-canceled context), distinguishing confirmed absence from missing podman or canceled probes.
func imageConfirmedAbsent(ctx context.Context, run CommandRunner, imageRef string) bool {
	if ctx.Err() != nil {
		return false
	}
	_, err := run(ctx, podmanBinary, "image", "exists", imageRef)
	if err == nil || ctx.Err() != nil {
		return false
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode() == 1
	}
	return errors.Is(err, errImageAbsent)
}

// Stage pulls the catalog add-on's pinned digest into local Podman storage and records its verified image ID.
func (s Stager) Stage(ctx context.Context, slug string) (StagedImage, error) {
	imageRef, err := s.imageForSlug(slug)
	if err != nil {
		return StagedImage{}, err
	}
	pullCtx, cancel := context.WithTimeout(ctx, stageTimeout)
	defer cancel()
	if _, err := s.run(pullCtx, podmanBinary, "pull", imageRef); err != nil {
		return StagedImage{}, fmt.Errorf("pull add-on image %s: %w", imageRef, err)
	}
	rawID, err := s.run(pullCtx, podmanBinary, "image", "inspect", "--format", "{{.Id}}", imageRef)
	if err != nil {
		return StagedImage{}, fmt.Errorf("verify staged add-on image %s: %w", imageRef, err)
	}
	imageID := strings.TrimSpace(string(rawID))
	if imageID == "" {
		return StagedImage{}, fmt.Errorf("verify staged add-on image %s: empty image ID", imageRef)
	}
	entry := StagedImage{Digest: imageRef, ImageID: imageID, StagedAt: s.now()}
	err = withStagedLock(pullCtx, s.StateDir, func() error {
		// Re-verify the image is still present under lock so a concurrent Unstage cannot leave a phantom record.
		if _, err := s.run(pullCtx, podmanBinary, "image", "exists", imageRef); err != nil {
			return fmt.Errorf("staged add-on image %s vanished before recording: %w", imageRef, err)
		}
		st, err := ReadStagedState(s.StateDir)
		if err != nil {
			return err
		}
		st.Images[imageRef] = entry
		return writeStagedState(s.StateDir, st)
	})
	if err != nil {
		return StagedImage{}, err
	}
	return entry, nil
}

// Unstage removes an add-on's staged image from local Podman storage when no retained generation references it.
func (s Stager) Unstage(ctx context.Context, slug string) error {
	imageRef, err := s.imageForSlug(slug)
	if err != nil {
		return err
	}
	return s.UnstageDigest(ctx, imageRef)
}

// UnstageDigest removes one immutable image digest if no retained or newly published generation references it.
func (s Stager) UnstageDigest(ctx context.Context, imageRef string) error {
	if !validDigestImage(imageRef) {
		return fmt.Errorf("invalid image digest reference %q", imageRef)
	}
	removeWithRefs := func(refs map[string]bool) error {
		return withStagedLock(ctx, s.StateDir, func() error {
			if refs[imageRef] {
				return fmt.Errorf("%w: %s", ErrImageReferenced, imageRef)
			}
			if _, err := s.run(ctx, podmanBinary, "rmi", imageRef); err != nil {
				if !imageConfirmedAbsent(ctx, s.run, imageRef) {
					return fmt.Errorf("remove staged add-on image %s: %w", imageRef, err)
				}
			}
			st, err := ReadStagedState(s.StateDir)
			if err != nil {
				return err
			}
			delete(st.Images, imageRef)
			return writeStagedState(s.StateDir, st)
		})
	}
	if s.WithUnstageLock != nil {
		return s.WithUnstageLock(ctx, removeWithRefs)
	}
	var refs map[string]bool
	if s.ReferencedImages != nil {
		var err error
		refs, err = s.ReferencedImages()
		if err != nil {
			return fmt.Errorf("check referenced add-on images: %w", err)
		}
	}
	return removeWithRefs(refs)
}

// RemoveAllManagedImages removes every staged add-on image tracked under StateDir during a factory reset.
// Any digest whose removal fails stays recorded in addons-staged.json so a retry cleans it up.
func RemoveAllManagedImages(ctx context.Context, stateDir string, run CommandRunner) error {
	if stateDir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(stateDir, StagedStateFile)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if run == nil {
		run = DefaultCommandRunner
	}
	return withStagedLock(ctx, stateDir, func() error {
		st, err := ReadStagedState(stateDir)
		if err != nil {
			return err
		}
		if len(st.Images) == 0 {
			if err := os.Remove(filepath.Join(stateDir, StagedStateFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		}
		var errs []error
		remaining := StagedState{Images: map[string]StagedImage{}}
		for _, digest := range st.Digests() {
			if _, err := run(ctx, podmanBinary, "rmi", "-f", digest); err != nil {
				if !imageConfirmedAbsent(ctx, run, digest) {
					errs = append(errs, fmt.Errorf("remove managed image %s: %w", digest, err))
					remaining.Images[digest] = st.Images[digest]
				}
			}
		}
		if len(remaining.Images) > 0 {
			if werr := writeStagedState(stateDir, remaining); werr != nil {
				errs = append(errs, werr)
			}
			return errors.Join(errs...)
		}
		if err := os.Remove(filepath.Join(stateDir, StagedStateFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	})
}

func writeStagedState(stateDir string, st StagedState) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(stateDir, ".addons-staged-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(stateDir, StagedStateFile)); err != nil {
		return err
	}
	if d, err := os.Open(stateDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	ok = true
	return nil
}

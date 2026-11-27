package addon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	// ErrImageReferenced reports an attempt to remove an image digest still needed by a saved or rollback generation.
	ErrImageReferenced = errors.New("staged image is referenced by a retained generation or saved configuration")
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
	out := make([]string, 0, len(s.Images))
	for k := range s.Images {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
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
// It runs outside the generations lock so slow WAN pulls never block apply or rollback.
type Stager struct {
	StateDir         string
	CatalogDir       string
	Run              CommandRunner
	Now              func() time.Time
	ReferencedImages func() (map[string]bool, error)
}

func (s Stager) catalogDir() string {
	if s.CatalogDir != "" {
		return s.CatalogDir
	}
	return DefaultCatalogDir
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

func (s Stager) withLock(fn func() error) error {
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.StateDir, stagedLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("lock staged add-on state: %w", err)
	}
	return fn()
}

// Stage pulls the catalog add-on's pinned digest into local Podman storage and records its verified image ID.
func (s Stager) Stage(ctx context.Context, slug string) (StagedImage, error) {
	if !ValidSlug(slug) {
		return StagedImage{}, fmt.Errorf("invalid add-on slug %q", slug)
	}
	cat, err := LoadCatalog(s.catalogDir())
	if err != nil {
		return StagedImage{}, err
	}
	m, ok := cat.BySlug(slug)
	if !ok {
		return StagedImage{}, fmt.Errorf("%w: %s", ErrAddonNotFound, slug)
	}
	imageRef := m.Runtime.Image
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
	err = s.withLock(func() error {
		st, err := ReadStagedState(s.StateDir)
		if err != nil {
			// Replace unreadable index with fresh state carrying the verified image.
			st = StagedState{Images: map[string]StagedImage{}}
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
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid add-on slug %q", slug)
	}
	cat, err := LoadCatalog(s.catalogDir())
	if err != nil {
		return err
	}
	m, ok := cat.BySlug(slug)
	if !ok {
		return fmt.Errorf("%w: %s", ErrAddonNotFound, slug)
	}
	return s.UnstageDigest(ctx, m.Runtime.Image)
}

// UnstageDigest removes one immutable image digest if no retained generation or saved config references it.
func (s Stager) UnstageDigest(ctx context.Context, imageRef string) error {
	if !validDigestImage(imageRef) {
		return fmt.Errorf("invalid image digest reference %q", imageRef)
	}
	return s.withLock(func() error {
		if s.ReferencedImages != nil {
			refs, err := s.ReferencedImages()
			if err != nil {
				return fmt.Errorf("check referenced add-on images: %w", err)
			}
			if refs[imageRef] {
				return fmt.Errorf("%w: %s", ErrImageReferenced, imageRef)
			}
		}
		if _, err := s.run(ctx, podmanBinary, "rmi", imageRef); err != nil {
			// Already absent in local Podman storage is not an error.
			if _, existsErr := s.run(ctx, podmanBinary, "image", "exists", imageRef); existsErr == nil {
				return fmt.Errorf("remove staged add-on image %s: %w", imageRef, err)
			}
		}
		st, err := ReadStagedState(s.StateDir)
		if err != nil {
			st = StagedState{Images: map[string]StagedImage{}}
		}
		delete(st.Images, imageRef)
		return writeStagedState(s.StateDir, st)
	})
}

// RemoveAllManagedImages removes every staged add-on image tracked under StateDir during a factory reset.
func RemoveAllManagedImages(ctx context.Context, stateDir string, run CommandRunner) error {
	st, err := ReadStagedState(stateDir)
	if err != nil || len(st.Images) == 0 {
		return nil
	}
	if run == nil {
		run = DefaultCommandRunner
	}
	var errs []error
	for _, digest := range st.Digests() {
		if _, err := run(ctx, podmanBinary, "rmi", "-f", digest); err != nil {
			if _, existsErr := run(ctx, podmanBinary, "image", "exists", digest); existsErr == nil {
				errs = append(errs, fmt.Errorf("remove managed image %s: %w", digest, err))
			}
		}
	}
	if err := os.Remove(filepath.Join(stateDir, StagedStateFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
	ok = true
	return nil
}

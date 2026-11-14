package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/securefs"
)

const recoveryMarker = "ANDARU_UPGRADE_RECOVERY_V1\n"

var setPattern = regexp.MustCompile(`^set-[0-9]+$`)

type Preparation struct {
	Directory       string    `json:"directory"`
	Version         string    `json:"version"`
	PreparedAt      time.Time `json:"preparedAt"`
	PackageRetained bool      `json:"packageRetained"`
	Warnings        []string  `json:"warnings,omitempty"`
}
type fileDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type unitState struct {
	Name         string `json:"name"`
	EnabledState string `json:"enabledState"`
	WasActive    bool   `json:"wasActive"`
}
type recoveryManifest struct {
	FormatVersion   int          `json:"formatVersion"`
	ID              string       `json:"id"`
	PreparedAt      time.Time    `json:"preparedAt"`
	Package         packageInfo  `json:"package"`
	PackageRetained bool         `json:"packageRetained"`
	PackageSHA256   string       `json:"packageSHA256,omitempty"`
	Dependencies    []string     `json:"dependencies,omitempty"`
	Files           []fileDigest `json:"files"`
	Units           []unitState  `json:"units,omitempty"`
}
type setPointer struct {
	ID string `json:"id"`
}

func privateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("recovery paths must be canonical and absolute")
	}
	if err := securefs.CheckAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || !securefs.RootOwned(st) || st.Mode().Perm() != 0700 {
		return errors.New("recovery directories must be root-owned 0700 directories")
	}
	return nil
}

func newRecoveryStage(root string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("unsafe recovery root")
	}
	if err := securefs.CheckAncestors(filepath.Dir(root)); err != nil {
		return "", err
	}
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := privateDir(root); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".prepare-")
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(stage, "owner"), []byte(recoveryMarker), 0600); err != nil {
		return "", err
	}
	if err = syncRegularFile(filepath.Join(stage, "owner")); err != nil {
		return "", err
	}
	return stage, syncDirectory(stage)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func regularFile(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("recovery state is not a regular file")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || !ok || stat.Uid != 0 || stat.Nlink != 1 || st.Mode().Perm()&0022 != 0 {
		f.Close()
		return nil, nil, errors.New("unsafe recovery state file")
	}
	return f, st, nil
}

func copyState(ctx context.Context, source, destination string) ([]fileDigest, error) {
	if err := privateDir(source); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return nil, errors.New("unsafe snapshot destination")
	}
	rel, err := filepath.Rel(source, destination)
	if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, errors.New("recovery cannot be nested inside source state")
	}
	if err = securefs.CheckAncestors(filepath.Dir(destination)); err != nil {
		return nil, err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return nil, err
	}
	var result []fileDigest
	var directories []string
	err = filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if d.IsDir() {
			st, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !securefs.RootOwned(st) || st.Mode().Perm()&0022 != 0 {
				return errors.New("unsafe state directory")
			}
			if path != source {
				if err = os.Mkdir(target, 0700); err != nil {
					return err
				}
			}
			directories = append(directories, target)
			return nil
		}
		src, st, err := regularFile(path)
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			src.Close()
			return err
		}
		hash := sha256.New()
		count, copyErr := io.Copy(io.MultiWriter(dst, hash), contextReader{ctx, src})
		err = errors.Join(copyErr, src.Close(), dst.Sync(), dst.Close())
		if err != nil {
			return err
		}
		if count != st.Size() {
			return errors.New("source state changed during snapshot")
		}
		result = append(result, fileDigest{Path: relative, Size: count, SHA256: hex.EncodeToString(hash.Sum(nil))})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err = syncDirectory(directories[i]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func hashFile(ctx context.Context, path string) (fileDigest, error) {
	f, st, err := regularFile(path)
	if err != nil {
		return fileDigest{}, err
	}
	h := sha256.New()
	n, readErr := io.Copy(h, contextReader{ctx, f})
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return fileDigest{}, err
	}
	if n != st.Size() {
		return fileDigest{}, errors.New("recovery file changed during verification")
	}
	return fileDigest{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func verifyCopiedFiles(ctx context.Context, state string, files []fileDigest) error {
	wanted := make(map[string]fileDigest, len(files))
	for _, file := range files {
		if file.Path == "." || filepath.IsAbs(file.Path) || filepath.Clean(file.Path) != file.Path || strings.HasPrefix(file.Path, ".."+string(os.PathSeparator)) {
			return errors.New("invalid recovery file path")
		}
		if _, exists := wanted[file.Path]; exists {
			return errors.New("duplicate recovery file path")
		}
		wanted[file.Path] = file
	}
	count := 0
	err := filepath.WalkDir(state, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return privateDir(path)
		}
		relative, e := filepath.Rel(state, path)
		if e != nil {
			return e
		}
		expected, ok := wanted[relative]
		if !ok {
			return errors.New("unlisted recovery state file")
		}
		got, e := hashFile(ctx, path)
		if e != nil {
			return e
		}
		if got.Size != expected.Size || got.SHA256 != expected.SHA256 {
			return errors.New("recovery state checksum mismatch")
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count != len(wanted) {
		return errors.New("recovery state file is missing")
	}
	return nil
}

func syncRegularFile(path string) error {
	f, _, err := regularFile(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func atomicJSON(path string, value any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".record-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	err = json.NewEncoder(f).Encode(value)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func privateJSON(path string, value any) error {
	f, st, err := regularFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if st.Size() > outputLimit {
		return errors.New("recovery metadata is oversized")
	}
	dec := json.NewDecoder(io.LimitReader(f, outputLimit+1))
	dec.DisallowUnknownFields()
	if err = dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing recovery metadata")
	}
	return nil
}

func verifyOwner(path string) error {
	f, _, err := regularFile(filepath.Join(path, "owner"))
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, int64(len(recoveryMarker)+1)))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	if !bytes.Equal(raw, []byte(recoveryMarker)) {
		return errors.New("recovery ownership marker does not match")
	}
	return nil
}

func ownedSet(root, id string) error {
	if !setPattern.MatchString(id) {
		return errors.New("invalid recovery set identifier")
	}
	path := filepath.Join(root, id)
	if err := privateDir(path); err != nil {
		return err
	}
	err := verifyOwner(path)
	if err != nil {
		return err
	}
	var manifest recoveryManifest
	if err = privateJSON(filepath.Join(path, "manifest.json"), &manifest); err != nil {
		return err
	}
	if manifest.FormatVersion != 1 || manifest.ID != id {
		return errors.New("recovery set identity does not match")
	}
	return nil
}

func oldPointer(root, name string) (string, error) {
	var pointer setPointer
	err := privateJSON(filepath.Join(root, name), &pointer)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return pointer.ID, ownedSet(root, pointer.ID)
}

func publishRecovery(ctx context.Context, root, stage string, manifest recoveryManifest) (Preparation, error) {
	var result Preparation
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := privateDir(root); err != nil {
		return result, err
	}
	if filepath.Dir(stage) != root || !strings.HasPrefix(filepath.Base(stage), ".prepare-") {
		return result, errors.New("staging is not owned by this preparation")
	}
	if err := privateDir(stage); err != nil {
		return result, err
	}
	err := verifyOwner(stage)
	if err != nil {
		return result, err
	}
	if manifest.FormatVersion != 1 || manifest.PreparedAt.IsZero() || !versionPattern.MatchString(manifest.Package.Version) || !architecturePattern.MatchString(manifest.Package.Architecture) {
		return result, errors.New("invalid recovery manifest")
	}
	if err = verifyCopiedFiles(ctx, filepath.Join(stage, "state"), manifest.Files); err != nil {
		return result, err
	}
	if err = config.VerifyRecoveryState(ctx, filepath.Join(stage, "state")); err != nil {
		return result, err
	}
	// SQLite's read-only connections may rebuild the copy's shared WAL indexes.
	// Their final bytes, not the pre-verification indexes, belong in the manifest.
	for _, name := range []string{"configuration/config.db-shm", "configuration/secrets.db-shm"} {
		manifest.Files = slices.DeleteFunc(manifest.Files, func(file fileDigest) bool { return file.Path == name })
		digest, readErr := hashFile(ctx, filepath.Join(stage, "state", name))
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return result, readErr
		}
		if err = syncRegularFile(filepath.Join(stage, "state", name)); err != nil {
			return result, err
		}
		digest.Path = name
		manifest.Files = append(manifest.Files, digest)
	}
	slices.SortFunc(manifest.Files, func(a, b fileDigest) int { return strings.Compare(a.Path, b.Path) })
	if manifest.PackageRetained {
		digest, err := hashFile(ctx, filepath.Join(stage, "package.deb"))
		if err != nil {
			return result, err
		}
		if digest.SHA256 != manifest.PackageSHA256 {
			return result, errors.New("recovery package checksum mismatch")
		}
		if err = syncRegularFile(filepath.Join(stage, "package.deb")); err != nil {
			return result, err
		}
	}
	manifest.ID = "set-" + strings.TrimPrefix(filepath.Base(stage), ".prepare-")
	if !setPattern.MatchString(manifest.ID) {
		return result, errors.New("invalid staging identifier")
	}
	pointer := "current.json"
	if !manifest.PackageRetained {
		pointer = "state-only.json"
	}
	old, err := oldPointer(root, pointer)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = atomicJSON(filepath.Join(stage, "manifest.json"), manifest); err != nil {
		return result, err
	}
	target := filepath.Join(root, manifest.ID)
	if _, err = os.Lstat(target); err == nil || !os.IsNotExist(err) {
		return result, errors.New("recovery destination already exists or cannot be inspected")
	}
	if err = os.Rename(stage, target); err != nil {
		return result, err
	}
	if err = syncDirectory(root); err != nil {
		return result, err
	}
	if err = atomicJSON(filepath.Join(root, pointer), setPointer{manifest.ID}); err != nil {
		return result, err
	}
	result = Preparation{Directory: target, Version: manifest.Package.Version, PreparedAt: manifest.PreparedAt, PackageRetained: manifest.PackageRetained}
	if !manifest.PackageRetained {
		result.Warnings = []string{"State saved; previous package not retained; package recovery requires a separately verified package."}
	}
	if old != "" {
		if err = ownedSet(root, old); err != nil {
			return result, err
		}
		if err = os.RemoveAll(filepath.Join(root, old)); err != nil {
			return result, fmt.Errorf("prune previous recovery set: %w", err)
		}
		if err = syncDirectory(root); err != nil {
			return result, err
		}
	}
	return result, nil
}

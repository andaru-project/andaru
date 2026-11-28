package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/securefs"
)

const (
	maxAddonBusTokensBytes = 1 << 20
	maxCertSyncBytes       = generation.MaxCertSyncBytes
	maxAddonLogBytes       = 256 << 10
)

var addonLogCatalogDir = addon.DefaultCatalogDir

// readStagedJSON strictly reads a root-owned 0600 JSON document the engine staged, without following symlinks, into v.
// When removeAfterValidate is true, the file is unlinked only AFTER all path, ancestor, symlink, ownership, and 0600
// regular-file checks succeed, so an unvalidated target is never removed.
func readStagedJSON(path, what string, maxBytes int, v any, removeAfterValidate bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New(what + " path must be canonical and absolute")
	}
	if err := securefs.CheckAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", what, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", what, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !securefs.RootOwned(info) {
		return errors.New(what + " must be a root-owned 0600 regular file")
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if removeAfterValidate {
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) && readErr == nil {
			readErr = fmt.Errorf("remove %s: %w", what, rmErr)
		}
	}
	if readErr != nil {
		return fmt.Errorf("read %s: %w", what, readErr)
	}
	if len(raw) > maxBytes {
		return fmt.Errorf("%s exceeds maximum size (%d bytes)", what, maxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", what, err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("unexpected trailing data in " + what)
	}
	return nil
}

// readCertSyncFile reads the engine's staged cert-sync payload and removes it once validated: it holds private keys.
func readCertSyncFile(path string) (generation.CertSync, error) {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "cert-sync-") || !strings.HasSuffix(base, ".json") {
		return generation.CertSync{}, errors.New("cert-sync file name must match cert-sync-*.json")
	}
	var payload generation.CertSync
	if err := readStagedJSON(path, "cert-sync file", maxCertSyncBytes, &payload, true); err != nil {
		return generation.CertSync{}, err
	}
	return payload, payload.Validate()
}

// readAddonBusTokensFile strictly reads a root-owned 0600 JSON map of slug -> bus token without following symlinks.
func readAddonBusTokensFile(path string) (map[string]string, error) {
	var tokens map[string]string
	if err := readStagedJSON(path, "bus tokens file", maxAddonBusTokensBytes, &tokens, false); err != nil {
		return nil, err
	}
	if tokens == nil {
		return nil, errors.New("bus tokens file must be a JSON object")
	}
	for slug, tok := range tokens {
		if !addon.ValidSlug(slug) {
			return nil, fmt.Errorf("invalid add-on slug %q in bus tokens file", slug)
		}
		if tok == "" || len(tok) > 256 || strings.ContainsAny(tok, "\x00\r\n") {
			return nil, fmt.Errorf("invalid bus token for add-on %q", slug)
		}
	}
	return tokens, nil
}

func addonStageVerb(ctx context.Context, verb, stateDir, target string, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "realize requires root")
		return 1
	}
	stager := addon.Stager{
		StateDir: stateDir,
		WithUnstageLock: func(ctx context.Context, fn func(map[string]bool) error) error {
			return generation.WithAddonImageLock(ctx, stateDir, fn)
		},
	}
	var err error
	switch verb {
	case "addon-stage":
		_, err = stager.Stage(ctx, target)
	case "addon-unstage":
		if addon.ValidDigestImage(target) {
			err = stager.UnstageDigest(ctx, target)
		} else {
			err = stager.Unstage(ctx, target)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// addonLogVerb reads a manifest-declared file log with privileged DAC capabilities (needed when
// container storage directories are owned by a non-root container UID while andaru-engine runs without
// CAP_DAC_READ_SEARCH) and writes the bounded JSON result into a pre-created root-owned 0600 file inside stateDir.
func addonLogVerb(stateDir, slug, logName string, lines int, outPath string, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "realize requires root")
		return 1
	}
	if err := writeAddonLogOutput(addonLogCatalogDir, stateDir, slug, logName, lines, outPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func writeAddonLogOutput(catalogDir, stateDir, slug, logName string, lines int, outPath string) error {
	if !filepath.IsAbs(outPath) || filepath.Clean(outPath) != outPath || filepath.Dir(outPath) != stateDir {
		return errors.New("log output path must be directly inside state-dir")
	}
	if err := securefs.CheckAncestors(stateDir); err != nil {
		return err
	}
	cat, err := addon.LoadCatalog(catalogDir)
	if err != nil {
		return fmt.Errorf("load add-on catalog: %w", err)
	}
	m, ok := cat.BySlug(slug)
	if !ok {
		return fmt.Errorf("unknown add-on slug %q", slug)
	}
	idx := slices.IndexFunc(m.Logs, func(l addon.LogSpec) bool { return l.Name == logName && l.Source.Type == "file" })
	if idx < 0 {
		return fmt.Errorf("unknown file log %q for add-on %q", logName, slug)
	}
	spec := m.Logs[idx]

	// Open and validate the output file before writing so we never follow a symlink or clobber another file.
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open log output file: %w", err)
	}
	defer out.Close()
	info, err := out.Stat()
	if err != nil {
		return fmt.Errorf("stat log output file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !securefs.RootOwned(info) {
		return errors.New("log output file must be a root-owned 0600 regular file")
	}

	entries, truncated, err := addon.ReadBoundedLogFile(stateDir, slug, spec.Source.Storage, spec.Source.Path, lines, maxAddonLogBytes)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Lines     []string `json:"lines"`
		Truncated bool     `json:"truncated"`
	}{Lines: entries, Truncated: truncated})
	if err != nil {
		return err
	}
	if _, err := out.Write(payload); err != nil {
		return err
	}
	return out.Sync()
}

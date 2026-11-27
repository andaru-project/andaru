package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/securefs"
)

const maxAddonBusTokensBytes = 1 << 20

// readAddonBusTokensFile strictly reads a root-owned 0600 JSON map of slug -> bus token without following symlinks.
func readAddonBusTokensFile(path string) (map[string]string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("bus tokens file path must be canonical and absolute")
	}
	if err := securefs.CheckAncestors(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open bus tokens file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat bus tokens file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !securefs.RootOwned(info) {
		return nil, errors.New("bus tokens file must be a root-owned 0600 regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxAddonBusTokensBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read bus tokens file: %w", err)
	}
	if len(raw) > maxAddonBusTokensBytes {
		return nil, errors.New("bus tokens file exceeds maximum size")
	}
	var tokens map[string]string
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&tokens); err != nil {
		return nil, fmt.Errorf("decode bus tokens file: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("unexpected trailing data in bus tokens file")
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

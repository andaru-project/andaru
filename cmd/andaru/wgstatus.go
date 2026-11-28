package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/securefs"
)

var wgStatusFilename = regexp.MustCompile(`^wg-status-[A-Za-z0-9._-]+\.json$`)

func wgStatusVerb(ctx context.Context, stateDir, outPath string, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "realize requires root")
		return 1
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out, fmt.Errorf("%s %v: %w: %s", name, args, err, exit.Stderr)
		}
		return out, err
	}
	if err := writeWireGuardStatus(ctx, stateDir, outPath, run, time.Now()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// writeWireGuardStatus replaces outPath, a wg-status-*.json file directly inside stateDir, with the WireGuard state as JSON.
func writeWireGuardStatus(ctx context.Context, stateDir, outPath string, run func(context.Context, string, ...string) ([]byte, error), now time.Time) error {
	if !filepath.IsAbs(outPath) || filepath.Clean(outPath) != outPath || filepath.Dir(outPath) != stateDir {
		return errors.New("status output path must be directly inside state-dir")
	}
	if !wgStatusFilename.MatchString(filepath.Base(outPath)) {
		return errors.New("status output filename must match wg-status-*.json")
	}
	if info, err := os.Lstat(outPath); err == nil && !info.Mode().IsRegular() {
		return errors.New("status output path must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.Geteuid() == 0 {
		if err := securefs.CheckAncestors(stateDir); err != nil {
			return err
		}
	}
	status, err := generation.ReadWireGuardStatus(ctx, run, now)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(status)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".wg-status-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(payload)
	if err = errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), outPath)
}

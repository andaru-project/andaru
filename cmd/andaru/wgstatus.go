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
	if err := writeWireGuardStatus(ctx, stateDir, outPath, runBounded, time.Now()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// runBounded runs a status or control command for at most 30 seconds, its stderr in the error.
func runBounded(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, fmt.Errorf("%s %v: %w: %s", name, args, err, exit.Stderr)
	}
	return out, err
}

// writeWireGuardStatus replaces outPath, a wg-status-*.json file directly inside stateDir, with the WireGuard state as JSON.
func writeWireGuardStatus(ctx context.Context, stateDir, outPath string, run func(context.Context, string, ...string) ([]byte, error), now time.Time) error {
	return writeStatusFile(stateDir, outPath, wgStatusFilename, func() (any, error) { return generation.ReadWireGuardStatus(ctx, run, now) })
}

// writeStatusFile replaces outPath, a file named as pattern says directly inside stateDir, with what read returns as
// JSON. The engine created the file; realize never follows a link there or writes outside stateDir.
func writeStatusFile(stateDir, outPath string, pattern *regexp.Regexp, read func() (any, error)) error {
	if !filepath.IsAbs(outPath) || filepath.Clean(outPath) != outPath || filepath.Dir(outPath) != stateDir {
		return errors.New("status output path must be directly inside state-dir")
	}
	if !pattern.MatchString(filepath.Base(outPath)) {
		return errors.New("status output filename must match " + pattern.String())
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
	status, err := read()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(status)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".status-*")
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

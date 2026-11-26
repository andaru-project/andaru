package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// A PPPoE session is pppd's options under pppDir and a unit that runs pppd on them. The directory carries pppd's
// SELinux type, so pppd may read what Andaru writes there.
const (
	pppDir        = "/run/andaru-ppp/"
	pppUnitPrefix = "/run/systemd/system/andaru-pppoe-"
)

// pppSessions lists the PPPoE sessions a set carries, by interface name.
func pppSessions(set []placed) []string {
	var names []string
	for _, f := range set {
		if pppUnit.MatchString(f.path) {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(f.path, pppUnitPrefix), ".service"))
		}
	}
	return names
}

func pppUnitName(name string) string { return "andaru-pppoe-" + name + ".service" }

// pppStamp is where Andaru records what a session's pppd was last started on.
func pppStamp(name string) string { return pppDir + name + ".started" }

// pppSum digests a session's unit and options as they are on disk now.
func (r Realizer) pppSum(name string) (string, error) {
	h := sha256.New()
	for _, path := range []string{pppUnitPrefix + name + ".service", pppDir + name + ".options"} {
		data, err := os.ReadFile(r.Root + path)
		if err != nil {
			return "", err
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stopSession stops a session's pppd and waits for it to exit, so its interface is gone when this returns.
func (r Realizer) stopSession(ctx context.Context, name string) error {
	unit := pppUnitName(name)
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "stop", unit); err != nil {
		// A unit systemd never loaded, as after a crash before the reload, is not running either.
		if _, active := r.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", unit); active == nil {
			return fmt.Errorf("stop the PPPoE session %s: %w", unit, err)
		}
	}
	return nil
}

// sessions stops the PPPoE sessions the new set dropped and starts the ones it carries. pppd reads its options only
// when it starts, so a running session is restarted only when its unit or options changed since Andaru started it:
// a redial drops every connection through it and may change the public address. A restart stops the old pppd
// first and waits for it, so verification cannot mistake the old session for the new one; starts do not wait for
// the session to come up, and verification decides whether it had to.
func (r Realizer) sessions(ctx context.Context, from, to []placed) error {
	had, has := pppSessions(from), pppSessions(to)
	if len(had) == 0 && len(has) == 0 {
		return nil
	}
	for _, name := range had {
		if slices.Contains(has, name) {
			continue
		}
		// systemd keeps a unit loaded after its file is gone, until the reload below.
		if err := r.stopSession(ctx, name); err != nil {
			return err
		}
		if err := os.Remove(r.Root + pppStamp(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("load the PPPoE session units: %w", err)
	}
	for _, name := range has {
		sum, err := r.pppSum(name)
		if err != nil {
			return err
		}
		// Unchanged, a running session stays up and a stopped or failed one is started again.
		if started, err := os.ReadFile(r.Root + pppStamp(name)); err != nil || string(started) != sum {
			if err := r.stopSession(ctx, name); err != nil {
				return err
			}
		}
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "start", "--no-block", pppUnitName(name)); err != nil {
			return fmt.Errorf("start the PPPoE session %s: %w", pppUnitName(name), err)
		}
		if err := writeAtomic(r.Root+pppStamp(name), []byte(sum), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// pppNetwork reports a .network file for a PPPoE interface, which exists only while its session is up.
func pppNetwork(data []byte) bool {
	return strings.Contains(string(data), "\nType=ppp\n")
}

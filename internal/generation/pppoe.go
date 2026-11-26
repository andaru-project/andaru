package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A PPPoE session is pppd's options under pppDir and a unit that runs pppd on them. The directory carries pppd's
// SELinux type, so pppd may read what Andaru writes there.
const (
	pppDir        = "/run/andaru-ppp/"
	pppUnitPrefix = "/run/systemd/system/andaru-pppoe-"
)

// pppoeUnits lists the session units a set carries, by unit name.
func pppoeUnits(set []placed) []string {
	var units []string
	for _, f := range set {
		if pppUnit.MatchString(f.path) {
			units = append(units, filepath.Base(f.path))
		}
	}
	return units
}

// pppStamp is where Andaru records what a session's pppd was last started on.
func pppStamp(unit string) string {
	return pppDir + strings.TrimSuffix(strings.TrimPrefix(unit, "andaru-pppoe-"), ".service") + ".started"
}

// pppSum digests a session's unit and options as they are on disk now.
func (r Realizer) pppSum(unit string) (string, error) {
	h := sha256.New()
	name := strings.TrimSuffix(strings.TrimPrefix(unit, "andaru-pppoe-"), ".service")
	for _, path := range []string{"/run/systemd/system/" + unit, pppDir + name + ".options"} {
		data, err := os.ReadFile(r.Root + path)
		if err != nil {
			return "", err
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sessions stops the PPPoE sessions the new set dropped and starts the ones it carries. pppd reads its options only
// when it starts, so a running session is restarted only when its unit or options changed since Andaru started it:
// a redial drops every connection through it and may change the public address. Starts do not wait for the
// session to come up; verification decides whether it had to.
func (r Realizer) sessions(ctx context.Context, from, to []placed) error {
	had, has := pppoeUnits(from), pppoeUnits(to)
	if len(had) == 0 && len(has) == 0 {
		return nil
	}
	for _, unit := range had {
		if slices.Contains(has, unit) {
			continue
		}
		// systemd keeps a unit loaded after its file is gone, until the reload below.
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "stop", unit); err != nil {
			return fmt.Errorf("stop the PPPoE session %s: %w", unit, err)
		}
		if err := os.Remove(r.Root + pppStamp(unit)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("load the PPPoE session units: %w", err)
	}
	for _, unit := range has {
		sum, err := r.pppSum(unit)
		if err != nil {
			return err
		}
		verb := "restart"
		if started, err := os.ReadFile(r.Root + pppStamp(unit)); err == nil && string(started) == sum {
			verb = "start" // unchanged: a running session stays up, a stopped or failed one is started again
		}
		if _, err := r.Run(ctx, "/usr/bin/systemctl", verb, "--no-block", unit); err != nil {
			return fmt.Errorf("start the PPPoE session %s: %w", unit, err)
		}
		if err := writeAtomic(r.Root+pppStamp(unit), []byte(sum), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// pppNetwork reports a .network file for a PPPoE interface, which exists only while its session is up.
func pppNetwork(data []byte) bool {
	return strings.Contains(string(data), "\nType=ppp\n")
}

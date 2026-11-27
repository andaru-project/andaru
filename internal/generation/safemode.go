package generation

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	// SafeModeBootFile is written under /run at boot when one-boot safe mode or the kernel argument is active.
	SafeModeBootFile = "/run/andaru/safe-mode"
	// SafeModeNextFile lives inside the state directory until the next boot consumes it into SafeModeBootFile.
	SafeModeNextFile = "safe-mode-next"
	// AddonsHeldFile is written fail-closed on every boot and removed once the engine verifies no upgrade hold is pending.
	AddonsHeldFile = "/run/andaru/addons-held"
	// SafeModeKernelArg is the exact kernel command-line token that activates safe mode for one boot.
	SafeModeKernelArg = "andaru.safe=1"
)

// SafeModeStatus reports whether add-ons are currently held off and every active reason.
type SafeModeStatus struct {
	Active  bool     `json:"active"`
	Reasons []string `json:"reasons"`
}

// ReadSafeMode inspects the runtime flags, pending next-boot flag, and kernel command line under root and stateDir.
// Reasons are returned in deterministic order: "boot", "kernelArg", "settingsHold".
func ReadSafeMode(root, stateDir string) SafeModeStatus {
	reasons := []string{}
	karg := kernelArgPresent(root+"/proc/cmdline", SafeModeKernelArg)
	bootFlag := false
	if raw, err := os.ReadFile(root + SafeModeBootFile); err == nil {
		// An empty or unrecognized file defaults to "boot" (fail closed), while a kernelArg-only marker does not
		// duplicate "boot" when only the kernel command line armed safe mode.
		content := strings.TrimSpace(string(raw))
		if content != "kernelArg" {
			bootFlag = true
		}
	}
	if bootFlag || (stateDir != "" && pathExists(filepath.Join(stateDir, SafeModeNextFile))) {
		reasons = append(reasons, "boot")
	}
	if karg {
		reasons = append(reasons, "kernelArg")
	}
	if pathExists(root + AddonsHeldFile) {
		reasons = append(reasons, "settingsHold")
	}
	return SafeModeStatus{Active: len(reasons) > 0, Reasons: reasons}
}

// ScheduleSafeMode arms the one-boot safe-mode flag in stateDir so the next boot starts without add-ons.
func ScheduleSafeMode(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(stateDir, SafeModeNextFile), []byte("boot\n"), 0o644)
}

func (r Realizer) applySafeModeOnBoot() {
	runDir := filepath.Dir(r.Root + AddonsHeldFile)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		slog.Error("create safe-mode runtime directory", "dir", runDir, "error", err)
		return
	}
	// Fail closed until andaru-engine opens config.db and verifies no settings-upgrade hold is active.
	if err := writeAtomic(r.Root+AddonsHeldFile, []byte("1\n"), 0o644); err != nil {
		slog.Error("write addons hold flag", "error", err)
	}
	nextPath := ""
	nextPresent := false
	if r.StateDir != "" {
		nextPath = filepath.Join(r.StateDir, SafeModeNextFile)
		nextPresent = pathExists(nextPath)
	}
	kargPresent := kernelArgPresent(r.Root+"/proc/cmdline", SafeModeKernelArg)
	if !nextPresent && !kargPresent {
		return
	}
	marker := "boot\n"
	if !nextPresent && kargPresent {
		marker = "kernelArg\n"
	}
	if err := writeAtomic(r.Root+SafeModeBootFile, []byte(marker), 0o644); err != nil {
		// Leave safe-mode-next in place on failure so ConditionPathExists=!/var/lib/andaru/safe-mode-next still holds.
		slog.Error("write boot safe-mode flag", "error", err)
		return
	}
	if nextPresent {
		if err := os.Remove(nextPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Error("remove consumed safe-mode-next flag", "error", err)
		}
	}
}

// ReleaseAddonsHold removes the fail-closed boot hold once the engine confirms no upgrade decisions are pending.
// The addon-runtime module will also reload and start enabled add-on units here when boot safe mode is inactive.
func (r Realizer) ReleaseAddonsHold(_ context.Context) error {
	if err := os.Remove(r.Root + AddonsHeldFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func kernelArgPresent(cmdlinePath, token string) bool {
	raw, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return false
	}
	for _, field := range strings.Fields(string(raw)) {
		if field == token {
			return true
		}
	}
	return false
}

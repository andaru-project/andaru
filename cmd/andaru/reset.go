package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/identity"
	"github.com/andaru-project/andaru/internal/reset"
)

const factoryResetUsage = "usage: andaru ctl factory-reset run|abort|status"

// Test seams. Production leaves them as they are.
var (
	resetDir      = reset.Dir
	resetStateDir = "/var/lib/andaru"
	resetEnvSeam  func(socket string) reset.Env
	resetEUID     = os.Geteuid
)

// runFactoryReset asks the questions, plans the reset and hands it to andaru-factory-reset.service,
// which survives the SSH session the new network may cut.
func runFactoryReset(ctx context.Context, socket string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || !map[string]bool{"run": true, "abort": true, "status": true}[args[0]] {
		fmt.Fprintln(stderr, factoryResetUsage)
		return 2
	}
	if resetEUID() != 0 {
		fmt.Fprintln(stderr, "factory reset requires root")
		return 1
	}
	env := resetEnv(socket)
	switch args[0] {
	case "status":
		active, phase, err := reset.Active(env.Dir)
		switch {
		case err != nil:
			fmt.Fprintln(stderr, err)
			return 1
		case !active:
			fmt.Fprintln(stdout, "no factory reset in progress")
		default:
			fmt.Fprintln(stdout, "factory reset: "+phase)
			fmt.Fprintln(stdout, "if it stopped, see journalctl -u andaru-factory-reset.service; resume with: systemctl start andaru-factory-reset.service")
		}
		return 0
	case "abort":
		if err := reset.Abort(env); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "factory reset aborted")
		return 0
	}
	password, err := readTerminalLine(ctx, "New admin password (empty for the default, admin): ", true)
	if err == nil && password != "" {
		var again string
		again, err = readTerminalLine(ctx, "Repeat the new admin password: ", true)
		if err == nil && again != password {
			err = errors.New("the passwords do not match")
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "factory reset cancelled:", err)
		return 1
	}
	verifier, err := identity.HashResetPassword(ctx, []byte(password))
	if err != nil {
		fmt.Fprintln(stderr, "factory reset cancelled: the password does not meet the password policy")
		return 1
	}
	path, err := readTerminalLine(ctx, "Write a portable backup first? Path for it, empty to skip: ", false)
	if err != nil {
		fmt.Fprintln(stderr, "factory reset cancelled:", err)
		return 1
	}
	if path != "" {
		if err = backupPathAllowed(path); err != nil {
			fmt.Fprintln(stderr, "factory reset cancelled:", err)
			return 1
		}
		if code := runBackup(ctx, socket, []string{"create", "--include-secrets", "--output", path}, nil, stdout, stderr); code != 0 {
			fmt.Fprintln(stderr, "factory reset cancelled: the backup was not written")
			return code
		}
	}
	answer, err := readTerminalLine(ctx, "Reboot when the reset is done? [y/N]: ", false)
	if err != nil || !map[string]bool{"": true, "n": true, "N": true, "y": true, "Y": true}[answer] {
		fmt.Fprintln(stderr, "factory reset cancelled: answer y or n")
		return 1
	}
	fmt.Fprintln(stderr, "Factory reset deletes every Andaru account, setting, generation and audit record in "+resetStateDir+".")
	fmt.Fprintln(stderr, "The box comes back on the stored recovery network, else on the last LAN an administrator used.")
	phrase, err := readTerminalLine(ctx, "Type FACTORY RESET to continue: ", false)
	if err != nil || phrase != "FACTORY RESET" {
		fmt.Fprintln(stderr, "factory reset cancelled; type FACTORY RESET exactly")
		return 1
	}
	if _, err = reset.Plan(ctx, env, verifier, path != "", answer == "y" || answer == "Y"); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err = env.Run(ctx, "systemctl", "start", "--no-block", "andaru-factory-reset.service"); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "factory reset started; this session may drop when the network changes. Follow it with: andaru ctl factory-reset status")
	return 0
}

// backupPathAllowed refuses a backup inside a tree the reset wipes or keeps.
func backupPathAllowed(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, tree := range []string{resetStateDir, "/var/lib/andaru-kept", resetDir} {
		if rel, err := filepath.Rel(tree, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return fmt.Errorf("the backup cannot go under %s", tree)
		}
	}
	return nil
}

// factoryResetVerb is andaru realize factory-reset: the unit's work, at the start and after a boot.
func factoryResetVerb(ctx context.Context, stderr io.Writer) int {
	op, err := reset.Resume(ctx, resetEnv(defaultSocket))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "factory reset finished:", op.Phase)
	return 0
}

// resetBlocks reports a reset that is stopping, tearing down or wiping the tree: nothing else may run then.
// denyAll says whether boot must shut the host then: always, unless the journal says Andaru never ran its network.
func resetBlocks() (blocked, denyAll bool) {
	active, phase, err := reset.Active(resetDir)
	if !active {
		return false, false
	}
	if err != nil {
		return true, true
	}
	if !reset.Fenced(phase) {
		return false, false
	}
	op, err := reset.Journal(resetDir)
	return true, err != nil || op.Ownership.Takeover || op.Ownership.Live
}

func resetEnv(socket string) reset.Env {
	if resetEnvSeam != nil {
		return resetEnvSeam(socket)
	}
	return productionResetEnv(socket)
}

func productionResetEnv(socket string) reset.Env {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out, fmt.Errorf("%s %v: %w: %s", name, args, err, exit.Stderr)
		}
		return out, err
	}
	realizer, _ := newRealizer(resetStateDir)
	r, _ := realizer.(generation.Realizer)
	return reset.Env{
		StateDir: resetStateDir, Dir: resetDir, Realizer: r, Run: run,
		PackageLocks: []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock"}, LockWait: 2 * time.Minute,
		Apply: func(ctx context.Context, saved string) (bool, error) { return engineApply(ctx, socket, saved) },
	}
}

// engineApply has the engine apply a saved version and confirms it once it came up, as an operator would.
func engineApply(ctx context.Context, socket, saved string) (bool, error) {
	ready := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && !ready && ctx.Err() == nil; {
		ready = runCtl(ctx, socket, []string{"versions", "list"}, nil, io.Discard, io.Discard) == 0
		if !ready {
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	if !ready {
		return false, errors.New("the engine did not start on the fresh state")
	}
	var out, errs bytes.Buffer
	if runCtl(ctx, socket, []string{"apply", "network", "--saved-version", saved, "--confirm-timeout", "120"}, nil, &out, &errs) != 0 {
		return false, fmt.Errorf("apply: %s", strings.TrimSpace(errs.String()))
	}
	var reply applyReply
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		return false, err
	}
	errs.Reset()
	if runCtl(ctx, socket, []string{"applies", "confirm", reply.ID}, nil, io.Discard, &errs) != 0 {
		return false, fmt.Errorf("confirm: %s", strings.TrimSpace(errs.String()))
	}
	return true, nil
}

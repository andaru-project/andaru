package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
)

const (
	andaruBinary = "/usr/bin/andaru"
	// Longer than any verb's lock wait plus work, so PID 1 only ever kills a wedged one.
	realizeRuntime = "RuntimeMaxSec=180"
)

// realizeRunner asks PID 1 to run the privileged realize command. The engine
// itself never writes outside its state directory.
type realizeRunner interface {
	// Arm schedules the rollback of a generation; it must succeed before anything changes.
	Arm(ctx context.Context, id, seconds int) error
	// Disarm stops the timer. A timer that fires anyway finds nothing to do.
	Disarm(ctx context.Context, id int)
	// Realize runs verb on generation id, or on none when id is 0.
	Realize(ctx context.Context, verb string, id int, extra ...string) error
}

type systemdRunner struct {
	stateDir string
	// busSocket is where realize tells the engine's subscribers of each apply's state.
	busSocket string
	exec      func(ctx context.Context, name string, args ...string) error
}

func newSystemdRunner(stateDir, busSocket string) realizeRunner {
	return systemdRunner{stateDir: stateDir, busSocket: busSocket, exec: func(ctx context.Context, name string, args ...string) error {
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, out)
		}
		return nil
	}}
}

// command runs verb on generation id; id 0 is a verb on no generation.
func (s systemdRunner) command(verb string, id int, extra ...string) []string {
	argv := []string{andaruBinary, "realize", "--state-dir", s.stateDir, "--bus-socket", s.busSocket, verb}
	if id != 0 {
		argv = append(argv, strconv.Itoa(id))
	}
	return append(argv, extra...)
}

func (s systemdRunner) Arm(ctx context.Context, id, seconds int) error {
	// AccuracySec defaults to a minute, which would let the rollback fire that much late.
	// A rollback that fails once (lock held, /run full) is tried again: nothing else undoes an unconfirmed change.
	args := []string{"--quiet", "--collect", "--unit=andaru-rollback-" + strconv.Itoa(id), "--on-active=" + strconv.Itoa(seconds), "--timer-property=AccuracySec=1s", "-p", realizeRuntime, "-p", "Restart=on-failure", "-p", "RestartSec=10"}
	return s.exec(ctx, "/usr/bin/systemd-run", append(args, s.command("rollback", id, "deadline")...)...)
}

func (s systemdRunner) Disarm(ctx context.Context, id int) {
	if err := s.exec(ctx, "/usr/bin/systemctl", "stop", "andaru-rollback-"+strconv.Itoa(id)+".timer"); err != nil {
		slog.WarnContext(ctx, "stop rollback timer", "generation", id, "error", err)
	}
}

func (s systemdRunner) Realize(ctx context.Context, verb string, id int, extra ...string) error {
	if verb == "" {
		return errors.New("realize needs a verb")
	}
	// The name differs from the timer's service: systemd refuses a transient unit whose name is already loaded.
	unit := "andaru-realize-" + verb
	if id != 0 {
		unit += "-" + strconv.Itoa(id)
	}
	args := []string{"--quiet", "--wait", "--collect", "--unit=" + unit, "-p", realizeRuntime}
	return s.exec(ctx, "/usr/bin/systemd-run", append(args, s.command(verb, id, extra...)...)...)
}

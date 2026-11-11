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
	Realize(ctx context.Context, verb string, id int, extra ...string) error
}

type systemdRunner struct {
	stateDir string
	exec     func(ctx context.Context, name string, args ...string) error
}

func newSystemdRunner(stateDir string) realizeRunner {
	return systemdRunner{stateDir: stateDir, exec: func(ctx context.Context, name string, args ...string) error {
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, out)
		}
		return nil
	}}
}

func (s systemdRunner) command(verb string, id int, extra ...string) []string {
	return append([]string{andaruBinary, "realize", "--state-dir", s.stateDir, verb, strconv.Itoa(id)}, extra...)
}

func (s systemdRunner) Arm(ctx context.Context, id, seconds int) error {
	// AccuracySec defaults to a minute, which would let the rollback fire that much late.
	args := []string{"--quiet", "--collect", "--unit=andaru-rollback-" + strconv.Itoa(id), "--on-active=" + strconv.Itoa(seconds), "--timer-property=AccuracySec=1s", "-p", realizeRuntime}
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
	args := []string{"--quiet", "--wait", "--collect", "--unit=andaru-realize-" + verb + "-" + strconv.Itoa(id), "-p", realizeRuntime}
	return s.exec(ctx, "/usr/bin/systemd-run", append(args, s.command(verb, id, extra...)...)...)
}

package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CheckIdle is the part of recovery preparation's checks a factory reset shares: dpkg is consistent, no apply is
// pending, and no realize job is queued or running.
func CheckIdle(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), stateDir string) error {
	audit, err := run(ctx, "dpkg", "--audit")
	if err != nil {
		return fmt.Errorf("inspect dpkg state: %w", err)
	}
	if err = textOutput(audit); err != nil {
		return err
	}
	if len(strings.TrimSpace(string(audit))) > 0 {
		return errors.New("dpkg has incomplete packages; repair them first")
	}
	if err = idleApplications(stateDir); err != nil {
		return err
	}
	return idleRealizationJobs(ctx, run)
}

// StopWriters stops the web service and the units recovery preparation pauses, then waits for running realizers.
// Unlike preparation it does not start them again: a factory reset starts them on the fresh state.
func StopWriters(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error)) error {
	for _, name := range append([]string{"andaru-web.service"}, preparationUnits...) {
		if _, err := run(ctx, "systemctl", "stop", name); err != nil {
			return fmt.Errorf("stop %s: %w", name, err)
		}
	}
	if err := drainRealizers(ctx, run); err != nil {
		return err
	}
	return idleRealizationJobs(ctx, run)
}

// WriterUnits are the units StopWriters stops, in the order a reset starts them again.
func WriterUnits() []string {
	return append(append([]string{}, preparationUnits...), "andaru-web.service")
}

//go:build linux

package reset

import (
	"context"
	"slices"
	"testing"
)

func TestCheckIdleFencesUpdateUnitsAndApplyIdleFencesRealizers(t *testing.T) {
	var seen [][]string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		seen = append(seen, slices.Clone(args))
		return nil, nil
	}
	if err := checkIdle(context.Background(), run, t.TempDir()); err != nil {
		t.Fatalf("checkIdle clean: %v", err)
	}
	if len(seen) != 2 || !slices.Contains(seen[0], "andaru-update-*.service") || !slices.Contains(seen[1], "andaru-update-*.service") {
		t.Fatalf("checkIdle must query andaru-update-*.service, got %v", seen)
	}

	// Active update unit refuses reset.
	busyRun := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if slices.Contains(args, "andaru-update-*.service") && args[0] == "list-units" {
			return []byte("andaru-update-run-stage.service loaded active running Andaru update\n"), nil
		}
		return nil, nil
	}
	if err := checkIdle(context.Background(), busyRun, t.TempDir()); err == nil {
		t.Fatal("expected checkIdle to refuse when andaru-update-*.service is active")
	}

	// CheckApplyIdle checks realize, leases, and prefixes units.
	seen = nil
	if err := CheckApplyIdle(context.Background(), t.TempDir(), run); err != nil {
		t.Fatalf("CheckApplyIdle clean: %v", err)
	}
	if len(seen) != 2 || !slices.Contains(seen[0], "andaru-leases.service") || !slices.Contains(seen[0], "andaru-prefixes.service") {
		t.Fatalf("CheckApplyIdle must query realize/leases/prefixes, got %v", seen)
	}
}

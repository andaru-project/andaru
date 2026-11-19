package reset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/andaru-project/andaru/internal/generation"
)

const outputLimit = 1024 * 1024

type commandRunner func(context.Context, string, ...string) ([]byte, error)

// preparationUnits are the units that write Andaru's state, in the order a reset starts them again.
var preparationUnits = []string{"andaru-leases.path", "andaru-prefixes.path", "andaru-prefixes.timer", "andaru-engine.service"}

// realizationUnits are the realizers the path and timer units start; a reset waits for them, it never kills them.
var realizationUnits = []string{"andaru-leases.service", "andaru-prefixes.service"}

// checkIdle refuses a reset while an apply is pending or active, or a realize job is queued or running.
func checkIdle(ctx context.Context, run commandRunner, stateDir string) error {
	if err := idleApplications(stateDir); err != nil {
		return err
	}
	return idleRealizationJobs(ctx, run)
}

// stopWriters stops the web service and the units that write Andaru's state, then waits for running realizers.
// It does not start them again: a factory reset starts them on the fresh state.
func stopWriters(ctx context.Context, run commandRunner) error {
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

// writerUnits are the units stopWriters stops, in the order a reset starts them again.
func writerUnits() []string {
	return append(append([]string{}, preparationUnits...), "andaru-web.service")
}

func drainRealizers(ctx context.Context, run commandRunner) error {
	for {
		allIdle := true
		for _, name := range realizationUnits {
			raw, err := run(ctx, "systemctl", "show", "--property=ActiveState", name)
			if err != nil {
				return err
			}
			if err = textOutput(raw); err != nil {
				return err
			}
			active := ""
			for _, line := range strings.Split(string(raw), "\n") {
				if value, ok := strings.CutPrefix(line, "ActiveState="); ok {
					active = value
				}
			}
			switch active {
			case "inactive", "failed":
			case "active", "activating", "deactivating", "reloading":
				allIdle = false
			default:
				return errors.New("unknown realizer activation state")
			}
		}
		if allIdle {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for realization jobs: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func idleRealizationJobs(ctx context.Context, run commandRunner) error {
	for _, args := range [][]string{
		{"list-jobs", "--no-legend", "--no-pager", "--plain", "andaru-realize-*.service"},
		{"list-units", "--state=active,activating,deactivating,reloading", "--no-legend", "--no-pager", "--plain", "andaru-realize-*.service"},
	} {
		raw, err := run(ctx, "systemctl", args...)
		if err != nil {
			return fmt.Errorf("inspect independent realization work: %w", err)
		}
		if err = textOutput(raw); err != nil {
			return err
		}
		if len(strings.TrimSpace(string(raw))) != 0 {
			return errors.New("independent realization work is queued or running; retry after it completes")
		}
	}
	return nil
}

func idleApplications(stateDir string) error {
	dir := filepath.Join(stateDir, "generations")
	pointer := filepath.Join(dir, "pointers.json")
	var raw map[string]json.RawMessage
	err := readStrict(pointer, &raw)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read generation pointers: %w", err)
	}
	if err == nil {
		for _, key := range []string{"confirmed", "lastKnownGood", "pending", "live"} {
			value, ok := raw[key]
			if !ok {
				return errors.New("generation pointers are incomplete")
			}
			var id *int
			if err = json.Unmarshal(value, &id); err != nil || id != nil && *id <= 0 {
				return errors.New("invalid generation pointer")
			}
			if key == "pending" && id != nil {
				return errors.New("a connectivity apply is pending; confirm or roll it back first")
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, e := strconv.Atoi(entry.Name())
		if e != nil || id <= 0 {
			continue
		}
		var state struct {
			State    string     `json:"state"`
			Previous *int       `json:"previous"`
			Deadline *time.Time `json:"deadline,omitempty"`
			Reason   string     `json:"reason,omitempty"`
		}
		err = readStrict(filepath.Join(dir, entry.Name(), "state.json"), &state)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if state.State == generation.Activating || state.State == generation.Awaiting || state.State == "rolling_back" {
			return errors.New("a connectivity apply is active; retry after it completes")
		}
	}
	return nil
}

// readStrict decodes one root-owned JSON value and refuses unknown fields and trailing data. Like the check it
// replaces, it refuses a file group or others can write, not one they can read: a restore may leave 0640 or 0400.
func readStrict(path string, value any) error {
	data, err := readRoot(path, func(perm os.FileMode) bool { return perm&0o022 == 0 })
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%s: trailing data", path)
	}
	return nil
}

func textOutput(raw []byte) error {
	if len(raw) > outputLimit || !utf8.Valid(raw) {
		return errors.New("invalid or oversized systemd output")
	}
	for _, c := range string(raw) {
		if unicode.IsControl(c) && c != '\n' && c != '\t' {
			return errors.New("control character in systemd output")
		}
	}
	return nil
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/render"
)

const realizeUsage = "usage: andaru realize [--state-dir PATH] activate N | rollback N REASON | confirm N | boot | leases | prefixes"

var generationID = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

type realizeVerbs interface {
	Activate(ctx context.Context, id int) error
	Rollback(ctx context.Context, id int, reason string) error
	Confirm(ctx context.Context, id int) error
	Boot(ctx context.Context) error
	SyncLeases(ctx context.Context) error
	SyncPrefixes(ctx context.Context) error
}

// newRealizer builds the privileged actor for the real host.
func newRealizer(stateDir string) (realizeVerbs, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("realize requires root")
	}
	return generation.Realizer{
		StateDir: stateDir, RenderDisaster: renderDisaster(""),
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// One stuck tool must not hold the generations lock.
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, name, args...).Output()
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return out, fmt.Errorf("%s %v: %w: %s", name, args, err, exit.Stderr)
			}
			return out, err
		},
		Chown: os.Chown, Now: time.Now,
		VerifyWait: 30 * time.Second, Poll: time.Second, LockWait: 70 * time.Second, LeaseSettle: time.Second, DeprecateAfter: 5 * time.Second,
	}, nil
}

// realizeCommand runs one privileged verb. Usage is checked before privilege so a typo never looks like a permission problem.
func realizeCommand(ctx context.Context, args []string, stderr io.Writer, build func(stateDir string) (realizeVerbs, error)) int {
	flags := flag.NewFlagSet("realize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", "/var/lib/andaru", "absolute private state directory")
	usage := func() int { fmt.Fprintln(stderr, realizeUsage); return 2 }
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 || !filepath.IsAbs(*stateDir) || filepath.Clean(*stateDir) != *stateDir {
		return usage()
	}
	verb, rest := flags.Arg(0), flags.Args()[1:]
	want := map[string]int{"activate": 1, "rollback": 2, "confirm": 1, "boot": 0, "leases": 0, "prefixes": 0}
	if n, known := want[verb]; !known || len(rest) != n || (n > 0 && !generationID.MatchString(rest[0])) {
		return usage()
	}
	id := 0
	if len(rest) > 0 {
		id, _ = strconv.Atoi(rest[0])
	}
	verbs, err := build(*stateDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch verb {
	case "activate":
		err = verbs.Activate(ctx, id)
	case "rollback":
		err = verbs.Rollback(ctx, id, rest[1])
	case "confirm":
		err = verbs.Confirm(ctx, id)
	case "boot":
		err = verbs.Boot(ctx)
	case "leases":
		err = verbs.SyncLeases(ctx)
	case "prefixes":
		err = verbs.SyncPrefixes(ctx)
	}
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, err)
	// A verb that found nothing to do (a late timer) succeeded; boot must never hold up the boot.
	if errors.Is(err, generation.ErrWrongState) || verb == "boot" {
		return 0
	}
	return 1
}

func renderDisaster(root string) func([]byte) error {
	return func(body []byte) error {
		var doc struct {
			Links    json.RawMessage `json:"links"`
			Segments json.RawMessage `json:"segments"`
			Zones    json.RawMessage `json:"zones"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return err
		}
		empty := func(raw json.RawMessage) json.RawMessage {
			if len(raw) == 0 {
				return json.RawMessage("[]")
			}
			return raw
		}
		decl, err := json.Marshal(map[string]any{"schemaVersion": 1, "links": empty(doc.Links), "segments": empty(doc.Segments), "zones": empty(doc.Zones)})
		if err != nil {
			return err
		}
		result, err := render.Network(render.Input{Declaration: decl})
		if err != nil {
			return err
		}
		for _, file := range result.Files {
			path := root + file.Path
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if file.Mode == "0600" {
				mode = 0o600
			}
			if err = os.WriteFile(path, []byte(file.Content), mode); err != nil {
				return err
			}
		}
		return nil
	}
}

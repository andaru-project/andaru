package main

import (
	"bytes"
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

var generationID = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

type realizeVerbs interface {
	Activate(ctx context.Context, id int) error
	Rollback(ctx context.Context, id int, reason string) error
	Confirm(ctx context.Context, id int) error
	Boot(ctx context.Context) error
	SyncLeases(ctx context.Context) error
	SyncPrefixes(ctx context.Context) error
	FillAliases(ctx context.Context) error
	ReleaseAddonsHold(ctx context.Context) error
	AddonControl(ctx context.Context, action, slug string) error
	SyncAddonBusTokens(ctx context.Context, tokens map[string]string) error
	Fence(ctx context.Context, denyAll bool) error
}

// newRealizer builds the privileged actor for the real host.
func newRealizer(stateDir, busSocket string) (realizeVerbs, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("realize requires root")
	}
	var activating func(id int)
	if busSocket != "" {
		activating = func(id int) {
			if err := tellApply(context.Background(), busSocket, id); err != nil {
				fmt.Fprintln(os.Stderr, "event bus not told of the activation:", err)
			}
		}
	}
	return generation.Realizer{
		StateDir: stateDir, RenderDisaster: renderDisaster(""), Activating: activating,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// One stuck tool must not hold the generations lock; add-on systemctl stop/start/restart
			// gets 25s to cover manifests with TimeoutStopSec up to 10s plus container cleanup.
			limit := 10 * time.Second
			if name == "/usr/bin/systemctl" && len(args) >= 2 &&
				(args[0] == "stop" || args[0] == "start" || args[0] == "restart") &&
				strings.HasPrefix(args[len(args)-1], "andaru-addon-") {
				limit = 25 * time.Second
			}
			ctx, cancel := context.WithTimeout(ctx, limit)
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
func realizeCommand(ctx context.Context, args []string, stdout, stderr io.Writer, build func(stateDir, busSocket string) (realizeVerbs, error)) int {
	flags := flag.NewFlagSet("realize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir, "")
	busSocket := flags.String("bus-socket", "", "")
	// Help flags are taken out first, so one standing where a flag's value belongs does not hide the verb.
	help := helpRequested(args)
	parseErr := flags.Parse(slices.DeleteFunc(slices.Clone(args), isHelp))
	verb := flags.Arg(0)
	want := map[string]int{
		"activate": 1, "rollback": 2, "confirm": 1, "boot": 0, "leases": 0, "prefixes": 0, "aliases": 0, "addons-release-hold": 0, "factory-reset": 0,
		"addon-stage": 1, "addon-unstage": 1, "addon-ctl": 2, "addon-bus-tokens": 1,
		"update-check": 0, "update-stage": 0, "update-rollback": 0, "update-reboot": 0, "set-hostname": 1,
	}
	n, known := want[verb]
	// A help flag anywhere is a request for help, never a rollback REASON; an unknown verb stays a usage error.
	if verb == "help" || help && (known || verb == "") {
		fmt.Fprint(stdout, realizeHelp)
		return 0
	}
	if parseErr != nil || !known || !filepath.IsAbs(*stateDir) || filepath.Clean(*stateDir) != *stateDir ||
		*busSocket != "" && (!filepath.IsAbs(*busSocket) || filepath.Clean(*busSocket) != *busSocket) {
		return usageError(stderr, realizeUsage, "andaru realize")
	}
	rest := flags.Args()[1:]
	if len(rest) != n {
		return usageError(stderr, realizeUsage, "andaru realize")
	}
	switch verb {
	case "activate", "rollback", "confirm":
		if !generationID.MatchString(rest[0]) {
			return usageError(stderr, realizeUsage, "andaru realize")
		}
	case "addon-stage":
		if !addon.ValidSlug(rest[0]) {
			return usageError(stderr, realizeUsage, "andaru realize")
		}
	case "addon-unstage":
		if !addon.ValidSlug(rest[0]) && !addon.ValidDigestImage(rest[0]) {
			return usageError(stderr, realizeUsage, "andaru realize")
		}
	case "addon-ctl":
		if (rest[0] != "start" && rest[0] != "stop" && rest[0] != "restart") || !addon.ValidSlug(rest[1]) {
			return usageError(stderr, realizeUsage, "andaru realize")
		}
	case "addon-bus-tokens":
		if !filepath.IsAbs(rest[0]) || filepath.Clean(rest[0]) != rest[0] {
			return usageError(stderr, realizeUsage, "andaru realize")
		}
	}
	if verb == "factory-reset" {
		return factoryResetVerb(ctx, stderr)
	}
	// While a reset stops, tears down or wipes the tree, nothing else touches it; boot only shuts the host.
	blocked, denyAll := resetBlocks()
	if blocked && verb != "boot" {
		fmt.Fprintln(stderr, "a factory reset is in progress; "+verb+" waits for it")
		// The engine keeps owing these: it tries them again after the reset.
		switch verb {
		case "set-hostname", "aliases", "addons-release-hold", "addon-stage", "addon-unstage", "addon-ctl", "addon-bus-tokens":
			return 1
		}
		return 0
	}
	switch verb {
	case "update-check", "update-stage", "update-rollback", "update-reboot":
		return updateVerb(ctx, verb, *stateDir, stderr)
	case "addon-stage", "addon-unstage":
		return addonStageVerb(ctx, verb, *stateDir, rest[0], stderr)
	case "set-hostname":
		return setHostnameVerb(ctx, rest[0], stderr)
	}
	id := 0
	if verb == "activate" || verb == "rollback" || verb == "confirm" {
		id, _ = strconv.Atoi(rest[0])
	}
	verbs, err := build(*stateDir, *busSocket)
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
		// Past the privilege check, and on the fence path too; a failed move never fails boot.
		if err := moveSeed(*stateDir); err != nil {
			fmt.Fprintln(stderr, "install seed not moved:", err)
		}
		if blocked {
			// Shut until the reset resumes: deny-all, or the factory filter where Andaru never ran the network.
			err = verbs.Fence(ctx, denyAll)
		} else {
			err = verbs.Boot(ctx)
		}
	case "leases":
		err = verbs.SyncLeases(ctx)
	case "prefixes":
		err = verbs.SyncPrefixes(ctx)
	case "aliases":
		err = verbs.FillAliases(ctx)
	case "addons-release-hold":
		err = verbs.ReleaseAddonsHold(ctx)
	case "addon-ctl":
		err = verbs.AddonControl(ctx, rest[0], rest[1])
	case "addon-bus-tokens":
		var tokens map[string]string
		if tokens, err = readAddonBusTokensFile(rest[0]); err == nil {
			err = verbs.SyncAddonBusTokens(ctx, tokens)
		}
	}
	// A verb on a generation may change its state even when it fails, so the bus hears of each one.
	if id != 0 && *busSocket != "" {
		if tellErr := tellApply(ctx, *busSocket, id); tellErr != nil {
			fmt.Fprintln(stderr, "event bus not told of the apply:", tellErr)
		}
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
		// Only the network: a write-once first/ from before the closure carried group members must still render,
		// so the disaster floor does not validate the allowed sources a reset apply needs.
		var doc struct {
			Links    json.RawMessage `json:"links"`
			Segments json.RawMessage `json:"segments"`
			Zones    json.RawMessage `json:"zones"`
			// Management, and the aliases its sources name, give the kept LAN its filter: without them none renders.
			Management json.RawMessage `json:"management"`
			Aliases    json.RawMessage `json:"aliases"`
			// A copy kept before the aliases had that name.
			Objects struct {
				Addresses json.RawMessage `json:"addresses"`
			} `json:"objects"`
			// A lan-closure carries the address objects its allowed sources name at the top level, not under objects.
			Addresses json.RawMessage `json:"addresses"`
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
		floor := map[string]any{"schemaVersion": 1, "links": empty(doc.Links), "segments": empty(doc.Segments), "zones": empty(doc.Zones)}
		if len(doc.Management) > 0 {
			floor["management"] = doc.Management
		}
		kept := doc.Objects.Addresses
		if len(kept) == 0 {
			kept = doc.Addresses
		}
		if len(doc.Aliases) > 0 {
			floor["aliases"] = doc.Aliases
		} else if len(kept) > 0 {
			floor["aliases"] = map[string]json.RawMessage{"addresses": kept}
		}
		decl, err := json.Marshal(floor)
		if err != nil {
			return err
		}
		links, facts, err := disasterHost(context.Background())
		if err != nil {
			return err
		}
		result, err := render.Network(render.Input{Declaration: decl, Links: links, Facts: facts})
		if err != nil {
			return err
		}
		if !result.Realizable {
			return errors.New("kept LAN is not realizable")
		}
		var written []restored
		rollback := func() error {
			var errs []error
			for i := len(written) - 1; i >= 0; i-- {
				item := written[i]
				if !item.existed {
					if rmErr := os.Remove(item.path); rmErr != nil && !os.IsNotExist(rmErr) {
						errs = append(errs, rmErr)
					}
					continue
				}
				if rbErr := commitRendered(item.path, item.body, item.mode); rbErr != nil {
					errs = append(errs, rbErr)
				}
			}
			return errors.Join(errs...)
		}
		// The kept LAN comes up behind its own filter, loaded before its files: never its addresses without it.
		var script []byte
		for _, file := range result.Files {
			if file.Path == generation.FilterEntry {
				script = []byte(file.Content)
			}
		}
		if script == nil {
			return errors.New("kept LAN has no packet filter")
		}
		if err = loadDisasterFilter(script); err != nil {
			return fmt.Errorf("load the kept LAN's packet filter: %w", err)
		}
		for _, file := range result.Files {
			if file.Path == generation.FilterEntry {
				continue
			}
			path := root + file.Path
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				if rbErr := rollback(); rbErr != nil {
					return errors.Join(err, generation.ErrRenderCleanup, rbErr)
				}
				return err
			}
			prior, existed, mode, err := priorFile(path)
			if err != nil {
				if rbErr := rollback(); rbErr != nil {
					return errors.Join(err, generation.ErrRenderCleanup, rbErr)
				}
				return err
			}
			fileMode := os.FileMode(0o644)
			if file.Mode == "0600" {
				fileMode = 0o600
			}
			if err = commitRendered(path, []byte(file.Content), fileMode); err != nil {
				if rbErr := rollback(); rbErr != nil {
					return errors.Join(err, generation.ErrRenderCleanup, rbErr)
				}
				return err
			}
			written = append(written, restored{path, existed, prior, mode})
		}
		return nil
	}
}

// disasterHost reads what the kept LAN renders against: the host's links and its tools, nft among them; tests replace it.
var disasterHost = func(ctx context.Context) ([]host.LinkStatus, host.RealizerFacts, error) {
	links, err := host.ReadLinks(ctx)
	if err != nil {
		return nil, host.RealizerFacts{}, err
	}
	facts, err := host.ReadRealizerFacts()
	return links, facts, err
}

// loadDisasterFilter replaces table inet andaru with the kept LAN's filter; tests replace it.
var loadDisasterFilter = func(script []byte) error {
	cmd := exec.Command("/usr/sbin/nft", "-f", "-")
	cmd.Stdin = bytes.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

var writeRenderedBody = func(f *os.File, body []byte) error {
	_, err := f.Write(body)
	return err
}

func commitRendered(path string, body []byte, mode os.FileMode) error {
	tmp := path + ".andaru-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return cleanupTemp(tmp, err)
	}
	if err = writeRenderedBody(f, body); err != nil {
		_ = f.Close()
		return cleanupTemp(tmp, err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return cleanupTemp(tmp, err)
	}
	if err = f.Close(); err != nil {
		return cleanupTemp(tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return cleanupTemp(tmp, err)
	}
	return nil
}

func cleanupTemp(tmp string, err error) error {
	if rmErr := os.Remove(tmp); rmErr != nil && !os.IsNotExist(rmErr) {
		return errors.Join(err, generation.ErrRenderCleanup)
	}
	return err
}

type restored struct {
	path    string
	existed bool
	body    []byte
	mode    os.FileMode
}

func priorFile(path string) ([]byte, bool, os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, 0, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, true, info.Mode().Perm(), err
	}
	return body, true, info.Mode().Perm(), nil
}

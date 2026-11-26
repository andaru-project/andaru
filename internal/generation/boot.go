package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/kept"
)

// Confirm makes a pending generation the one boot uses.
func (r Realizer) Confirm(ctx context.Context, id int) (result error) {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	authorization, err := r.authorization(ctx)
	if err != nil {
		return err
	}
	if authorization != nil {
		defer authorization.Close()
	}
	ctx, cancel := context.WithTimeout(ctx, verbLimit)
	defer cancel()
	p, err := readPointers(r.dir())
	if err != nil {
		return err
	}
	if !is(p.Pending, id) {
		return fmt.Errorf("confirm %d: %w", id, ErrWrongState)
	}
	g, err := read(r.dir(), id, p)
	if err != nil {
		return err
	}
	if g.State.State != Awaiting || g.Deadline == nil || r.Now().After(*g.Deadline) {
		return fmt.Errorf("confirm %d: %w", id, ErrWrongState)
	}
	previousDigest, err := generationAuthorizationDigest(r.dir(), p, g.Previous)
	if err != nil {
		return err
	}
	if authorization != nil && g.AuthorizationDigest != previousDigest {
		if err = requireAuthorizationTransition(authorization, g, id, authz.TransitionConfirm); err != nil {
			return err
		}
		defer func() { result = errors.Join(result, authorization.ClearPending(id, authz.TransitionConfirm)) }()
	}
	if authorization != nil {
		authState, err := authorization.Read()
		if err != nil || authState.LiveDigest != g.AuthorizationDigest {
			return errors.Join(authz.ErrStateUnavailable, err)
		}
	}
	// Pointers first: once they name this generation a late timer and the next boot both keep it.
	// Live is set too: a kill during activation may have left it behind the files.
	// Last-known-good is what ran before this generation, which after a boot fallback is not the confirmed pointer.
	p.LastKnownGood, p.Confirmed, p.Pending, p.Live = g.Previous, &id, nil, &id
	if err := r.writePointers("confirm:pointers", p); err != nil {
		return err
	}
	if err := r.writeState("confirm:state", id, State{State: Confirmed, Previous: g.Previous}); err != nil {
		return err
	}
	r.copyKeptFirst(ctx, id)
	return nil
}

// copyKeptFirst copies the closure written beside this generation. A missing
// file is an uplink or a generation from before this copy existed.
func (r Realizer) copyKeptFirst(ctx context.Context, id int) {
	raw, err := os.ReadFile(filepath.Join(r.dir(), strconv.Itoa(id), "lan-closure.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("read the LAN closure", "error", err)
		}
		return
	}
	root := kept.Root
	if r.KeptRoot != "" {
		root = r.KeptRoot
	}
	if err = kept.CopyFirst(ctx, root, r.StateDir, raw); err != nil {
		slog.Error("copy the first LAN", "error", err)
	}
}

// Boot puts the confirmed generation into /run before networkd starts. It renders nothing.
func (r Realizer) Boot(ctx context.Context) (result error) {
	if _, statErr := os.Lstat(r.StateDir); os.IsNotExist(statErr) {
		return r.disasterBoot(ctx)
	}
	if _, statErr := os.Lstat(filepath.Join(r.dir(), "pointers.json")); os.IsNotExist(statErr) {
		return r.disasterBoot(ctx)
	} else if statErr != nil {
		return statErr
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	authorization, err := r.authorization(ctx)
	if err != nil {
		return err
	}
	if authorization != nil {
		defer authorization.Close()
		if pending, found, pendingErr := authorization.Pending(); pendingErr != nil {
			return pendingErr
		} else if found {
			defer func() { result = errors.Join(result, authorization.ClearPending(pending.GenerationID, pending.Phase)) }()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, restoreLimit)
	defer cancel()
	dir := r.dir()
	p, err := readPointers(dir)
	if err != nil {
		return err
	}
	if err := r.serviceDirs(); err != nil {
		slog.Error("create the service directories", "error", err)
	}
	if p.Pending != nil {
		// /run was emptied by the reboot, so an unconfirmed generation is already gone; only the record remains.
		id := *p.Pending
		g, err := read(dir, id, p)
		p.Pending = nil
		if e := r.writePointers("boot:pointers-pending", p); e != nil {
			return e
		}
		if err == nil {
			err = r.writeState("boot:state", id, State{State: RolledBack, Previous: g.Previous, Reason: "reboot"})
		}
		if err != nil {
			slog.Error("record the unconfirmed generation as rolled back", "generation", id, "error", err)
		}
	}
	if p.Confirmed == nil {
		p.Live = nil
		if r.resetTree() {
			// Fenced first, so no failure below can leave the host without a filter.
			return r.consoleRequired(ctx, p, authorization)
		}
		// Fenced first, as on a reset tree; a failure still records the pointers.
		fenced := r.fence(ctx)
		if err = advanceAuthorization(authorization, ""); err != nil {
			return errors.Join(fenced, err)
		}
		return errors.Join(fenced, r.writePointers("boot:pointers", p))
	}
	var failures []error
	for _, id := range []*int{p.Confirmed, p.LastKnownGood} {
		if id == nil {
			continue
		}
		g, err := read(dir, *id, p)
		var set []placed
		if err == nil {
			set, err = r.files(g)
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		// The adapters get their names first, so everything after sees the final ones: udev may have named them
		// before the .link files existed, and they are down until networkd starts.
		links := slices.DeleteFunc(slices.Clone(set), func(f placed) bool { return !strings.HasSuffix(f.path, ".link") })
		if len(links) > 0 {
			if err := r.place(nil, links); err != nil {
				return err
			}
			for _, args := range [][]string{{"control", "--reload"}, {"trigger", "--settle", "--action=add", "--subsystem-match=net"}} {
				if _, err := r.Run(ctx, "/usr/bin/udevadm", args...); err != nil {
					slog.Error("apply the adapter names", "error", err)
				}
			}
		}
		// A candidate whose filter does not load is unusable: never its files without its filter.
		path, comment, ok, err := r.filter(g)
		if err == nil && ok {
			err = r.loadFilter(ctx, path, comment)
		} else if err == nil {
			// An earlier candidate's filter may have half-loaded; this generation runs without one.
			err = r.dropFilter(ctx)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("generation %d: its packet filter does not load: %w", *id, err))
			// None of an unusable candidate's files may stay, its adapter names included.
			if err := r.place(links, nil); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if err := r.place(nil, set); err != nil {
			return err
		}
		if err := r.sessions(ctx, nil, set); err != nil {
			slog.Error("start the PPPoE sessions", "error", err)
		}
		if err := r.bootPrefixes(); err != nil {
			slog.Error("fill the DHCPv6 subnets of delegated segments", "error", err)
		}
		if err = advanceAuthorization(authorization, g.AuthorizationDigest); err != nil {
			return err
		}
		p.Live = id
		if len(failures) > 0 {
			slog.Error("boot fell back to an older generation", "generation", *id, "error", errors.Join(failures...))
		}
		return r.writePointers("boot:pointers", p)
	}
	if r.resetTree() {
		// No generation runs: deny-all replaces whatever a candidate half-loaded, in one load.
		p.Live = nil
		return r.consoleRequired(ctx, p, authorization, failures...)
	}
	// No generation is usable: the host still gets its bare filter, and the catch-all once set up. No generation
	// runs, so the pointers and the authorization say so.
	p.Live = nil
	failures = append(failures, r.fence(ctx), errors.New("no generation is usable; the host has no managed interface"))
	if err = advanceAuthorization(authorization, ""); err != nil {
		return errors.Join(append(failures, err)...)
	}
	return errors.Join(append(failures, r.writePointers("boot:pointers", p))...)
}

// consoleRequired is boot on a reset tree with no usable generation: its bare filter and, once set up, the catch-all,
// so only the console gets in. The fence comes first; the authorization update and the pointers follow even when it fails.
func (r Realizer) consoleRequired(ctx context.Context, p Pointers, authorization *authz.StateLock, failures ...error) error {
	failures = append(failures, r.fence(ctx), advanceAuthorization(authorization, ""), r.writePointers("boot:pointers", p))
	return errors.Join(append([]error{errors.New("CONSOLE_REQUIRED: no generation is usable on this reset host")}, failures...)...)
}

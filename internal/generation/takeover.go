package generation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/andaru-project/andaru/internal/host"
)

const cloudInitDisable = "/etc/cloud/cloud.cfg.d/99-andaru-network.cfg"

var netplanPath = regexp.MustCompile(`^/run/systemd/network/10-netplan-[^/]+$`)

type baselineFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	SHA256 string `json:"sha256"`
}

// baselineManifest records the files a takeover replaced, so a rollback can put them back exactly.
type baselineManifest struct {
	Kind     string            `json:"kind"`
	Files    []baselineFile    `json:"files"`
	Ifupdown *IfupdownBaseline `json:"ifupdown,omitempty"`
}

// IfupdownBaseline is what an ifupdown takeover captured: enough to hand the host back to ifupdown,
// and dhcpcd's identity, which every later generation keeps presenting.
type IfupdownBaseline struct {
	Interfaces        []host.IfupdownInterface `json:"interfaces"`
	NetworkdWasActive bool                     `json:"networkdWasActive"`
	// Connected are the interfaces with a global IPv4 address before the takeover; a hand-back waits for them.
	Connected []string `json:"connected"`
}

// ReadBaselineKind returns the kind of the captured baseline ("" when none was captured) and, for ifupdown, what it holds.
func ReadBaselineKind(dir string) (kind string, ifupdown *IfupdownBaseline, err error) {
	var m baselineManifest
	if _, err := readJSON(filepath.Join(dir, "baseline", "manifest.json"), &m); err != nil {
		return "", nil, err
	}
	return m.Kind, m.Ifupdown, nil
}

func (r Realizer) baselineDir() string { return filepath.Join(r.dir(), "baseline") }

// baseline returns the captured pre-Andaru files; captured is false when no takeover ever recorded one.
func (r Realizer) baseline() (set []placed, captured bool, err error) {
	var m baselineManifest
	captured, err = readJSON(filepath.Join(r.baselineDir(), "manifest.json"), &m)
	if err != nil || !captured {
		return nil, false, err
	}
	for _, f := range m.Files {
		mode, err := strconv.ParseUint(f.Mode, 8, 9)
		if err != nil || !netplanPath.MatchString(f.Path) {
			return nil, true, fmt.Errorf("baseline names %q (mode %q), which Andaru may not write", f.Path, f.Mode)
		}
		data, err := os.ReadFile(filepath.Join(r.baselineDir(), "files", f.SHA256))
		if err != nil {
			return nil, true, err
		}
		if sum(data) != f.SHA256 {
			return nil, true, fmt.Errorf("baseline copy of %s does not match its manifest", f.Path)
		}
		set = append(set, placed{path: f.Path, mode: fs.FileMode(mode), uid: f.UID, gid: f.GID, data: data})
	}
	return set, true, nil
}

// captureBaseline copies netplan's files out of /run. Until a generation has been
// confirmed each attempt captures afresh, so a netplan change between attempts is kept.
func (r Realizer) captureBaseline(p Pointers) error {
	// Once a takeover is confirmed netplan's files are gone for good; what is in /run then is not a baseline.
	if p.Takeover {
		return nil
	}
	paths, err := filepath.Glob(r.Root + "/run/systemd/network/10-netplan-*")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(r.baselineDir(), "files"), 0o700); err != nil {
		return err
	}
	m := baselineManifest{Kind: "netplan", Files: []baselineFile{}}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || !ok {
			return fmt.Errorf("baseline file %s is not a regular file", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f := baselineFile{
			Path: "/run/systemd/network/" + filepath.Base(path), Mode: fmt.Sprintf("%04o", info.Mode().Perm()),
			UID: int(owner.Uid), GID: int(owner.Gid), SHA256: sum(data),
		}
		// Content is stored by hash, so the manifest rename below is the only commit.
		if err := writeAtomic(filepath.Join(r.baselineDir(), "files", f.SHA256), data, 0o600); err != nil {
			return err
		}
		m.Files = append(m.Files, f)
	}
	return writeJSON(filepath.Join(r.baselineDir(), "manifest.json"), m)
}

// Confirm makes a pending generation the one boot uses. On a takeover it then
// removes netplan and cloud-init as competing writers.
func (r Realizer) Confirm(ctx context.Context, id int) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
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
	// Pointers first: once they name this generation a late timer and the next boot both keep it.
	// Live is set too: a kill during activation may have left it behind the files.
	// Last-known-good is what ran before this generation, which after a boot fallback is not the confirmed pointer.
	p.LastKnownGood, p.Confirmed, p.Pending, p.Live = g.Previous, &id, nil, &id
	p.Takeover = p.Takeover || g.Takeover
	if err := r.writePointers("confirm:pointers", p); err != nil {
		return err
	}
	state := State{State: Confirmed, Previous: g.Previous}
	if err := r.writeState("confirm:state", id, state); err != nil {
		return err
	}
	if !g.Takeover {
		return nil
	}
	kind, _, err := ReadBaselineKind(r.dir())
	if err == nil {
		err = r.makePermanent(ctx, true, kind)
	}
	if err != nil {
		state.Reason = "takeover not yet permanent: " + err.Error() + "; completes at the next boot"
		return errors.Join(err, r.writeState("confirm:state-reason", id, state))
	}
	return nil
}

// makePermanent stops what configured the network before Andaru from doing it again: netplan and cloud-init,
// or ifupdown. Every step runs even after a failure and may be repeated; boot repeats all of them.
func (r Realizer) makePermanent(ctx context.Context, reload bool, kind string) error {
	var failures []error
	step := func(name string, do func() error) {
		err := r.at(name)
		if err == nil {
			err = do()
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
		}
	}
	// netplan's generator is what enables networkd on cloud images; without its YAML nothing else would.
	enable := func() error {
		// Andaru's links are never required for "online", so wait-online would hold every boot for its two-minute timeout.
		var failures []error
		for _, args := range [][]string{{"enable", "systemd-networkd.service"}, {"disable", "systemd-networkd-wait-online.service"}} {
			if !reload {
				args = []string{args[0], "--no-reload", args[1]}
			}
			_, err := r.Run(ctx, "/usr/bin/systemctl", args...)
			failures = append(failures, err)
		}
		return errors.Join(failures...)
	}
	if kind == "ifupdown" {
		step("permanent:enable", enable)
		// Neither the boot service nor udev's hotplug may raise a taken interface again; /etc/network/interfaces stays as it was.
		step("permanent:mask", func() error {
			args := []string{"mask", "networking.service", "ifup@.service"}
			if !reload {
				args = []string{"mask", "--no-reload", "networking.service", "ifup@.service"}
			}
			_, err := r.Run(ctx, "/usr/bin/systemctl", args...)
			return err
		})
		return errors.Join(failures...)
	}
	// cloud-init must be told before the YAML goes, or it writes the YAML back at the next boot.
	step("permanent:cloud-init", func() error {
		target := r.Root + cloudInitDisable
		if _, err := os.Stat(filepath.Dir(target)); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return writeAtomic(target, []byte("network: {config: disabled}\n"), 0o644)
	})
	step("permanent:netplan", func() error {
		paths, err := filepath.Glob(r.Root + "/etc/netplan/*.yaml")
		if err != nil {
			return err
		}
		kept := filepath.Join(r.baselineDir(), "netplan")
		if err := os.MkdirAll(kept, 0o700); err != nil {
			return err
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			target := filepath.Join(kept, filepath.Base(path))
			if old, err := os.ReadFile(target); err == nil && !bytes.Equal(old, data) {
				target += "." + strconv.FormatInt(r.Now().Unix(), 10)
			}
			if err := writeAtomic(target, data, 0o600); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		return nil
	})
	// Last: its daemon reload reruns netplan's generator, which writes netplan's files back while the YAML exists.
	step("permanent:enable", enable)
	return errors.Join(failures...)
}

// Boot puts the confirmed generation into /run before networkd starts. It renders nothing.
func (r Realizer) Boot(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
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
		return r.writePointers("boot:pointers", p)
	}
	base, captured, baseErr := r.baseline()
	kind, ifupdown, kindErr := ReadBaselineKind(dir)
	if p.Takeover {
		if err := errors.Join(kindErr, r.makePermanent(ctx, false, kind)); err != nil {
			slog.Error("finish the takeover", "error", err)
		}
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
		// netplan's generator runs before this unit while its YAML exists, and its files sort before Andaru's.
		stale, _ := filepath.Glob(r.Root + "/run/systemd/network/10-netplan-*")
		for _, path := range stale {
			if err := os.Remove(path); err != nil {
				failures = append(failures, err)
			}
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
		p.Live = id
		if len(failures) > 0 {
			slog.Error("boot fell back to an older generation", "generation", *id, "error", errors.Join(failures...))
		}
		return r.writePointers("boot:pointers", p)
	}
	// A candidate's filter can fail after nft accepted it (readback, sysctl); no generation runs, so neither does a filter.
	if err := r.dropFilter(ctx); err != nil {
		failures = append(failures, err)
	}
	if kind == "ifupdown" && ifupdown != nil {
		return r.bootIfupdown(ctx, p, *ifupdown, failures)
	}
	if !captured || baseErr != nil {
		return errors.Join(append(failures, baseErr, errors.New("no generation is usable; the host has no managed interface"))...)
	}
	if err := r.place(nil, base); err != nil {
		return err
	}
	p.Live = nil
	slog.Error("booting the pre-Andaru baseline; no generation is usable", "error", errors.Join(failures...))
	return r.writePointers("boot:pointers", p)
}

// bootIfupdown starts the host on its unchanged ifupdown configuration when no generation is usable.
// Boot runs before networking.service, so it only queues the starts: waiting for them would deadlock.
func (r Realizer) bootIfupdown(ctx context.Context, p Pointers, b IfupdownBaseline, failures []error) error {
	run := func(args ...string) {
		if _, err := r.Run(ctx, "/usr/bin/systemctl", args...); err != nil {
			failures = append(failures, err)
		}
	}
	run("unmask", "networking.service", "ifup@.service")
	run("start", "--no-block", "networking.service")
	for _, i := range b.Interfaces {
		if i.Hotplug {
			run("start", "--no-block", "ifup@"+i.Name+".service")
		}
	}
	p.Live = nil
	slog.Error("booting on ifupdown; no generation is usable", "error", errors.Join(failures...))
	return r.writePointers("boot:pointers", p)
}

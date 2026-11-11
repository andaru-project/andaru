package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/host"
)

// ErrWrongState means the pointers do not name this generation for this verb; nothing was done.
var ErrWrongState = errors.New("generation is not in the expected state")

const (
	activateLimit = 45 * time.Second
	verbLimit     = 60 * time.Second
	restoreLimit  = 20 * time.Second
)

// Realizer runs the privileged verbs. Everything it touches outside StateDir
// lives under Root, so tests run it against a temporary tree.
type Realizer struct {
	StateDir string
	Root     string
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	Chown    func(path string, uid, gid int) error
	Now      func() time.Time
	// VerifyWait is how long networkd gets to apply a generation; Poll is the interval between looks.
	VerifyWait, Poll time.Duration
	LockWait         time.Duration
	// failAt lets tests stop a verb before a named write, as a kill would.
	failAt func(step string) error
}

// placed is one file as it must exist under /run.
type placed struct {
	path     string
	mode     fs.FileMode
	uid, gid int // -1: leave as created
	data     []byte
}

func (r Realizer) dir() string { return Dir(r.StateDir) }

func (r Realizer) at(step string) error {
	if r.failAt != nil {
		return r.failAt(step)
	}
	return nil
}

// lock serialises every verb. The engine never takes it, so a dead engine cannot hold it.
func (r Realizer) lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(r.dir(), "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for deadline := time.Now().Add(r.LockWait); ; time.Sleep(100 * time.Millisecond) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock generations: %w", err)
		}
	}
}

func (r Realizer) writePointers(step string, p Pointers) error {
	if err := r.at(step); err != nil {
		return err
	}
	return writeJSON(filepath.Join(r.dir(), "pointers.json"), p)
}

func (r Realizer) writeState(step string, id int, s State) error {
	if err := r.at(step); err != nil {
		return err
	}
	return writeJSON(filepath.Join(r.dir(), strconv.Itoa(id), "state.json"), s)
}

// files loads a generation's content, checked against its manifest.
func (r Realizer) files(g Generation) ([]placed, error) {
	out := make([]placed, 0, len(g.Files))
	for _, f := range g.Files {
		data, err := g.content(r.dir(), f)
		if err != nil {
			return nil, err
		}
		out = append(out, placed{path: f.Path, mode: 0o644, uid: -1, gid: -1, data: data})
	}
	return out, nil
}

// live is what the pointers say is in /run now: a generation's files or the baseline's.
func (r Realizer) live(p Pointers) ([]placed, error) {
	if p.Live == nil {
		set, _, err := r.baseline()
		return set, err
	}
	g, err := read(r.dir(), *p.Live, p)
	if err != nil {
		return nil, err
	}
	// Only the paths matter for what is being replaced, so a corrupt live generation is no obstacle.
	out := make([]placed, 0, len(g.Files))
	for _, f := range g.Files {
		if allowed(f.Path) {
			out = append(out, placed{path: f.Path})
		}
	}
	return out, nil
}

func has(set []placed, path string) bool {
	return slices.ContainsFunc(set, func(f placed) bool { return f.path == path })
}

// place swaps files only. It writes before it removes, so /run never holds less than one complete configuration.
func (r Realizer) place(from, to []placed) error {
	for _, f := range to {
		target := r.Root + f.path
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeAtomic(target, f.data, f.mode); err != nil {
			return err
		}
		if f.uid >= 0 {
			if err := r.Chown(target, f.uid, f.gid); err != nil {
				return err
			}
		}
	}
	if err := r.at("place:remove"); err != nil {
		return err
	}
	for _, f := range from {
		if has(to, f.path) {
			continue
		}
		if err := os.Remove(r.Root + f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// settle makes networkd read the files and removes the devices only from had.
func (r Realizer) settle(ctx context.Context, from, to []placed) error {
	if _, err := r.Run(ctx, "/usr/bin/networkctl", "reload"); err != nil {
		return err
	}
	for _, f := range from {
		name, isDevice := strings.CutSuffix(filepath.Base(f.path), ".netdev")
		name, own := strings.CutPrefix(name, "50-andaru-")
		if !isDevice || !own || has(to, f.path) {
			continue
		}
		// networkd keeps a device whose .netdev is gone; one never created is not an error worth stopping for.
		if _, err := r.Run(ctx, "/usr/bin/networkctl", "delete", name); err != nil {
			slog.Warn("delete network device", "device", name, "error", err)
		}
	}
	return nil
}

type observation struct {
	links   []host.LinkStatus
	managed []host.Managed
	route   string // interface of the IPv4 default route, if any
}

func (r Realizer) observe(ctx context.Context) (observation, error) {
	var o observation
	data, err := r.Run(ctx, "/usr/bin/networkctl", "list", "--json=short")
	if err == nil {
		o.managed, err = host.ParseManaged(data)
	}
	if err != nil {
		return o, err
	}
	if data, err = r.Run(ctx, "/usr/bin/ip", "-d", "-j", "address", "show"); err == nil {
		o.links, err = host.ParseLinks(data)
	}
	if err != nil {
		return o, err
	}
	if data, err = r.Run(ctx, "/usr/bin/ip", "-j", "route", "show", "default"); err != nil {
		return o, err
	}
	var routes []struct {
		Dev string `json:"dev"`
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return o, fmt.Errorf("decode default routes: %w", err)
	}
	if len(routes) > 0 {
		o.route = routes[0].Dev
	}
	return o, nil
}

func (o observation) link(name string) (host.LinkStatus, bool) {
	i := slices.IndexFunc(o.links, func(l host.LinkStatus) bool { return l.Name == name })
	if i < 0 {
		return host.LinkStatus{}, false
	}
	return o.links[i], true
}

func globalIPv4(l host.LinkStatus) bool {
	return slices.ContainsFunc(l.Addresses, func(a host.AddressStatus) bool { return a.Family == "ipv4" && a.Scope == "global" })
}

// connected lists the physical interfaces networkd gives a global IPv4 address, and whether one carries the default route.
func (o observation) connected() (names []string, route bool) {
	for _, m := range o.managed {
		if l, ok := o.link(m.Name); ok && l.Kind == "" && globalIPv4(l) {
			names = append(names, m.Name)
		}
		route = route || (o.route != "" && o.route == m.Name)
	}
	return names, route
}

// unmet says why the host does not yet run the generation; "" means it does.
func (o observation) unmet(set []placed, names []string, route bool) string {
	for _, f := range set {
		if !strings.HasSuffix(f.path, ".network") {
			continue
		}
		i := slices.IndexFunc(o.managed, func(m host.Managed) bool { return m.NetworkFile == f.path })
		if i < 0 {
			return f.path + " is not bound to any interface"
		}
		m := o.managed[i]
		if m.State != "configured" {
			return m.Name + " is " + m.State
		}
		l, _ := o.link(m.Name)
		// The path alone cannot tell a rewritten file from the old one; its static addresses can.
		for line := range strings.SplitSeq(string(f.data), "\n") {
			value, ok := strings.CutPrefix(line, "Address=")
			want, err := netip.ParsePrefix(value)
			if !ok || err != nil {
				continue
			}
			if !slices.ContainsFunc(l.Addresses, func(a host.AddressStatus) bool {
				return a.Address == want.Addr().String() && a.PrefixLength == want.Bits()
			}) {
				return m.Name + " does not have " + value
			}
		}
	}
	for _, name := range names {
		if l, ok := o.link(name); !ok || !globalIPv4(l) {
			return name + " lost its IPv4 address"
		}
	}
	if route && o.route == "" {
		return "the IPv4 default route is gone"
	}
	return ""
}

// Activate makes a stored generation live and leaves it awaiting confirmation,
// or restores what was live and records why.
func (r Realizer) Activate(ctx context.Context, id int) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, activateLimit)
	defer cancel()
	dir := r.dir()
	p, err := readPointers(dir)
	if err != nil {
		return err
	}
	g, err := read(dir, id, p)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(id), "state.json")); err == nil || p.Pending != nil {
		return fmt.Errorf("activate %d: %w", id, ErrWrongState)
	}
	next, err := r.files(g)
	if err != nil {
		return err
	}
	if g.Takeover {
		if err := r.captureBaseline(p); err != nil {
			return err
		}
	}
	previous, err := r.live(p)
	if err != nil {
		return err
	}
	before, err := r.observe(ctx)
	if err != nil {
		return err
	}
	names, route := before.connected()

	// State first: a kill before the pointer moves leaves a generation that was never pending.
	if err := r.writeState("activate:state-activating", id, State{State: Activating, Previous: p.Live}); err != nil {
		return err
	}
	p.Pending = &id
	if err := r.writePointers("activate:pointers-pending", p); err != nil {
		return err
	}

	reason := r.swapAndVerify(ctx, previous, next, names, route)
	if reason == "" {
		deadline := r.Now().Add(time.Duration(g.ConfirmTimeoutSeconds) * time.Second)
		if err := r.writeState("activate:state-awaiting", id, State{State: Awaiting, Previous: p.Live, Deadline: &deadline}); err != nil {
			return err
		}
		p.Live = &id
		if err := r.writePointers("activate:pointers-live", p); err != nil {
			return err
		}
		return nil
	}

	restore, stop := context.WithTimeout(context.WithoutCancel(ctx), restoreLimit)
	defer stop()
	restored, err := r.restorable(previous, p)
	if err == nil {
		err = r.place(next, restored)
	}
	if err != nil {
		// The files could not be put back; the armed timer still owns this generation.
		return fmt.Errorf("activate %d: %s; restoring the previous files failed: %w", id, reason, err)
	}
	if err := r.settle(restore, next, restored); err != nil {
		reason += "; reload after restoring: " + err.Error()
	}
	live := p.Live
	p.Pending = nil
	if err := r.writePointers("activate:pointers-failed", p); err != nil {
		return err
	}
	if err := r.writeState("activate:state-failed", id, State{State: Failed, Previous: live, Reason: reason}); err != nil {
		return err
	}
	return fmt.Errorf("activate %d: %s", id, reason)
}

// restorable returns the content of what was live before an activation.
func (r Realizer) restorable(previous []placed, p Pointers) ([]placed, error) {
	if p.Live == nil {
		return previous, nil // the baseline set already carries its content
	}
	g, err := read(r.dir(), *p.Live, p)
	if err != nil {
		return nil, err
	}
	return r.files(g)
}

func (r Realizer) swapAndVerify(ctx context.Context, previous, next []placed, names []string, route bool) string {
	if err := r.place(previous, next); err != nil {
		return err.Error()
	}
	if err := r.settle(ctx, previous, next); err != nil {
		return err.Error()
	}
	verify, cancel := context.WithTimeout(ctx, r.VerifyWait)
	defer cancel()
	reason := "networkd was not observed"
	for {
		if o, err := r.observe(verify); err != nil {
			reason = err.Error()
		} else if reason = o.unmet(next, names, route); reason == "" {
			return ""
		}
		select {
		case <-verify.Done():
			return "not verified in time: " + reason
		case <-time.After(r.Poll):
		}
	}
}

// Rollback restores what ran before a pending generation. It acts only while
// the pointers name that generation, so a timer firing late does nothing.
func (r Realizer) Rollback(ctx context.Context, id int, reason string) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, verbLimit)
	defer cancel()
	dir := r.dir()
	p, err := readPointers(dir)
	if err != nil {
		return err
	}
	if !is(p.Pending, id) {
		return fmt.Errorf("roll back %d: %w", id, ErrWrongState)
	}
	g, err := read(dir, id, p)
	if err != nil {
		return err
	}
	// The timer is armed before activation, so it can fire inside the window the operator was shown; it retries until the deadline.
	if reason == "deadline" && g.State.State == Awaiting && g.Deadline != nil && r.Now().Before(*g.Deadline) {
		return fmt.Errorf("roll back %d: the confirmation deadline has not passed", id)
	}
	// from is this generation whatever the live pointer says: a kill may have left the pointer behind the files.
	from := make([]placed, 0, len(g.Files))
	for _, f := range g.Files {
		if allowed(f.Path) {
			from = append(from, placed{path: f.Path})
		}
	}
	target, live, what, err := r.fallback(p, g.Previous)
	if err != nil {
		p.Pending = nil
		if e := r.writePointers("rollback:pointers", p); e != nil {
			return e
		}
		if e := r.writeState("rollback:state", id, State{State: Failed, Previous: g.Previous, Reason: reason + ": nothing to restore: " + err.Error()}); e != nil {
			return e
		}
		return fmt.Errorf("roll back %d: nothing to restore: %w", id, err)
	}
	// Files first: once they are back, a failing reload still leaves the next reload or boot correct.
	if err := r.place(from, target); err != nil {
		return err
	}
	if err := r.settle(ctx, from, target); err != nil {
		reason += "; " + err.Error()
	}
	if what != "" {
		reason += " (restored " + what + ")"
	}
	p.Pending, p.Live = nil, live
	if err := r.writePointers("rollback:pointers", p); err != nil {
		return err
	}
	return r.writeState("rollback:state", id, State{State: RolledBack, Previous: g.Previous, Reason: reason})
}

// fallback picks what to restore: the previous generation, else last-known-good, else the captured baseline.
// what is empty when the choice is the one the generation recorded.
func (r Realizer) fallback(p Pointers, previous *int) (set []placed, live *int, what string, err error) {
	var failures []error
	for _, candidate := range []struct {
		id   *int
		what string
	}{{previous, ""}, {p.LastKnownGood, "last-known-good"}} {
		if candidate.id == nil {
			continue
		}
		g, err := read(r.dir(), *candidate.id, p)
		if err == nil {
			set, err = r.files(g)
		}
		if err == nil {
			return set, candidate.id, candidate.what, nil
		}
		failures = append(failures, err)
	}
	set, captured, err := r.baseline()
	if err != nil {
		return nil, nil, "", errors.Join(append(failures, err)...)
	}
	if previous == nil {
		return set, nil, "", nil // also "no files" when Andaru's first apply had no baseline to replace
	}
	if !captured {
		// Removing every file would take the host off the network; what runs now at least works.
		return nil, nil, "", errors.Join(failures...)
	}
	return set, nil, "baseline", nil
}

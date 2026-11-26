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

	"github.com/andaru-project/andaru/internal/authz"
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
	// LeaseSettle is how long the lease sync waits for more lease events before it reads.
	LeaseSettle time.Duration
	// DeprecateAfter is how long the prefix sync waits before advertising a prefix it just saw replaced, so
	// networkd's own advertisement listing it, which would undo the deprecation, goes first.
	DeprecateAfter time.Duration
	// SendRA sends a router advertisement on an interface; nil sends it for real.
	SendRA func(ifname string, body []byte) error
	// RenderDisaster renders a kept LAN closure. Nil records the selected copy without rendering.
	RenderDisaster func(body []byte) error
	// KeptRoot is the test seam. Production leaves it empty and uses kept.Root.
	KeptRoot string
	// Activating, when set, hears that a generation is activating, before the wait for networkd: until then the
	// generation reads as never started.
	Activating func(id int)
	// failAt lets tests stop a verb before a named write, as a kill would.
	failAt func(step string) error
	// afterLeaseRead lets tests act while a lease sync is running.
	afterLeaseRead func()
	// keaUID lets unprivileged tests name a Kea user other than their own; nil: the owner of /run/kea.
	keaUID func() uint32
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

// lock serialises every verb. The engine takes it only for bounded backup observation,
// so it cannot retain the lock for its process lifetime.
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

func generationAuthorizationDigest(dir string, p Pointers, id *int) (string, error) {
	if id == nil {
		return "", nil
	}
	g, err := read(dir, *id, p)
	if err != nil {
		return "", err
	}
	return g.AuthorizationDigest, nil
}

func requireAuthorizationTransition(lock *authz.StateLock, g Generation, id int, phase string) error {
	pending, found, err := lock.Pending()
	if err != nil {
		return err
	}
	// Revision 0 with no pending file is a pre-identity generation. A recorded pending file, including the initial store revision, must match.
	if g.IdentityRevision == 0 && !found {
		return nil
	}
	if !found || pending != (authz.PendingTransition{
		GenerationID: id, Phase: phase, IdentityRevision: g.IdentityRevision, AuthorizationDigest: g.AuthorizationDigest,
	}) {
		return authz.ErrTransitionInProgress
	}
	return nil
}

func (r Realizer) writePointers(step string, p Pointers) error {
	if err := r.at(step); err != nil {
		return err
	}
	p.Takeover = nil // read from older trees, never written
	return writeJSON(filepath.Join(r.dir(), "pointers.json"), p)
}

func (r Realizer) writeState(step string, id int, s State) error {
	if err := r.at(step); err != nil {
		return err
	}
	return writeJSON(filepath.Join(r.dir(), strconv.Itoa(id), "state.json"), s)
}

// Non-root unit proofs predate the authorization store. The engine process is root, so production always takes the lock.
func (r Realizer) authorization(ctx context.Context) (*authz.StateLock, error) {
	if os.Geteuid() != 0 {
		return nil, nil
	}
	return authz.Acquire(ctx, r.StateDir)
}

func advanceAuthorization(lock *authz.StateLock, digest string) error {
	if lock == nil {
		return nil
	}
	_, err := lock.Advance(digest)
	return err
}

// files loads a generation's content, checked against its manifest.
func (r Realizer) files(g Generation) ([]placed, error) {
	out := make([]placed, 0, len(g.Files))
	for _, f := range g.Files {
		if f.Path == filterEntry {
			continue // loaded with nft, never placed
		}
		data, err := g.content(r.dir(), f)
		if err != nil {
			return nil, err
		}
		mode := fs.FileMode(0o644)
		if f.Mode == "0600" {
			mode = 0o600
		}
		out = append(out, placed{path: f.Path, mode: mode, uid: -1, gid: -1, data: data})
	}
	return out, nil
}

// live is what the pointers say is in /run now: a generation's files, or the catch-all once set up.
func (r Realizer) live(p Pointers) ([]placed, error) {
	if p.Live == nil {
		return r.bare(), nil
	}
	g, err := read(r.dir(), *p.Live, p)
	if err != nil {
		return nil, err
	}
	// Only the paths matter for what is being replaced, so a corrupt live generation is no obstacle.
	out := make([]placed, 0, len(g.Files))
	for _, f := range g.Files {
		if allowed(f.Path) && f.Path != filterEntry {
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
	if err := r.sessions(ctx, from, to); err != nil {
		return err
	}
	return r.services(ctx, from, to)
}

type observation struct {
	links   []host.LinkStatus
	managed []host.Managed
	route   string // interface of the IPv4 default route, if any
	// dns is what systemd-resolved reports per interface; nil when it was not asked.
	dns map[string][]netip.Addr
}

func (r Realizer) observe(ctx context.Context, withDNS bool) (observation, error) {
	o := observation{managed: []host.Managed{}}
	// networkctl fails outright while networkd is stopped.
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "systemd-networkd.service"); err == nil {
		data, err := r.Run(ctx, "/usr/bin/networkctl", "list", "--json=short")
		if err == nil {
			o.managed, err = host.ParseManaged(data)
		}
		if err != nil {
			return o, err
		}
	}
	data, err := r.Run(ctx, "/usr/bin/ip", "-d", "-j", "address", "show")
	if err == nil {
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
	if withDNS {
		if data, err = r.Run(ctx, "/usr/bin/resolvectl", "dns"); err == nil {
			o.dns, err = host.ParseResolvedDNS(data)
		}
		if err != nil {
			return o, fmt.Errorf("ask systemd-resolved for its servers: %w", err)
		}
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

// connected lists the physical interfaces and PPPoE sessions networkd manages with a global IPv4 address, and whether
// one carries the default route: each must still have one after a change.
func (o observation) connected() (names []string, route bool) {
	for _, m := range o.managed {
		if l, ok := o.link(m.Name); ok && (l.Kind == "" || l.Kind == "ppp") && globalIPv4(l) {
			names = append(names, m.Name)
		}
		route = route || (o.route != "" && o.route == m.Name)
	}
	return names, route
}

// asksIPv4 says whether a rendered .network file gives its interface an IPv4 address of its own.
func asksIPv4(data []byte) bool {
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "DHCP="); ok && (value == "yes" || value == "ipv4") {
			return true
		}
		if value, ok := strings.CutPrefix(line, "Address="); ok {
			if p, err := netip.ParsePrefix(value); err != nil || p.Addr().Is4() {
				return true
			}
		}
	}
	return false
}

// unmet says why the host does not yet run the generation; "" means it does.
func (o observation) unmet(set []placed, names []string, route bool) string {
	for _, f := range set {
		// The catch-all binds only links no generation names, and there may be none. A PPPoE interface exists only while its session is up; connected decides whether it had to be.
		if !strings.HasSuffix(f.path, ".network") || f.path == catchAllPath || pppNetwork(f.data) {
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
		// Exactly the declared servers, in order: leftover offered ones mean the change did not take effect.
		if want := DeclaredDNS(f.data); o.dns != nil && len(want) > 0 && !slices.Equal(o.dns[m.Name], want) {
			return fmt.Sprintf("%s resolves through %v, not the declared %v", m.Name, o.dns[m.Name], want)
		}
	}
	for _, name := range names {
		if l, ok := o.link(name); !ok || !globalIPv4(l) {
			return name + " lost its IPv4 address"
		}
	}
	if route && o.route == "" {
		why := "the IPv4 default route is gone"
		for _, name := range pppSessions(set) {
			if l, ok := o.link(name); !ok {
				why += "; PPPoE session " + name + " is not up"
			} else if !globalIPv4(l) {
				why += "; PPPoE session " + name + " has no IPv4 address"
			}
		}
		return why
	}
	return ""
}

// Activate makes a stored generation live and leaves it awaiting confirmation,
// or restores what was live and records why.
func (r Realizer) Activate(ctx context.Context, id int) (result error) {
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
	if authorization != nil {
		if err = requireAuthorizationTransition(authorization, g, id, authz.TransitionActivate); err != nil {
			return err
		}
		defer func() { result = errors.Join(result, authorization.ClearPending(id, authz.TransitionActivate)) }()
	}
	previousDigest, err := generationAuthorizationDigest(dir, p, p.Live)
	if err != nil {
		return err
	}
	if authorization != nil {
		if err = advanceAuthorization(authorization, previousDigest); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(id), "state.json")); err == nil || p.Pending != nil {
		return fmt.Errorf("activate %d: %w", id, ErrWrongState)
	}
	next, err := r.files(g)
	if err != nil {
		return err
	}
	previous, err := r.live(p)
	if err != nil {
		return err
	}
	before, err := r.observe(ctx, false)
	if err != nil {
		return err
	}
	names, route := before.connected()
	names = slices.DeleteFunc(names, func(name string) bool {
		// A session the new set drops is stopped: its interface is not expected back.
		if l, _ := before.link(name); l.Kind == "ppp" {
			return !slices.Contains(pppSessions(next), name)
		}
		// Nor is the address of an interface a session now runs over, unless its own file still asks for one.
		if !slices.Contains(pppParents(next), name) {
			return false
		}
		i := slices.IndexFunc(next, func(f placed) bool { return filepath.Base(f.path) == "50-andaru-"+name+".network" })
		return i >= 0 && !asksIPv4(next[i].data)
	})

	// State first: a kill before the pointer moves leaves a generation that was never pending.
	if err := r.writeState("activate:state-activating", id, State{State: Activating, Previous: p.Live}); err != nil {
		return err
	}
	p.Pending = &id
	if err := r.writePointers("activate:pointers-pending", p); err != nil {
		return err
	}
	if r.Activating != nil {
		r.Activating(id)
	}

	// The filter goes in before anything forwards or listens on the new files; the reason is why it did not.
	var reason string
	if err := r.toward(ctx, &g, func() error {
		if why := r.swapAndVerify(ctx, previous, next, names, route); why != "" {
			return errors.New(why)
		}
		return nil
	}); err != nil {
		reason = err.Error()
	}
	if reason == "" {
		deadline := r.Now().Add(time.Duration(g.ConfirmTimeoutSeconds) * time.Second)
		if err := r.writeState("activate:state-awaiting", id, State{State: Awaiting, Previous: p.Live, Deadline: &deadline}); err != nil {
			return err
		}
		if err = advanceAuthorization(authorization, g.AuthorizationDigest); err != nil {
			return err
		}
		p.Live = &id
		if err := r.writePointers("activate:pointers-live", p); err != nil {
			restoreErr := advanceAuthorization(authorization, previousDigest)
			return errors.Join(err, restoreErr)
		}
		return nil
	}

	restore, stop := context.WithTimeout(context.WithoutCancel(ctx), restoreLimit)
	defer stop()
	var earlier *Generation
	if p.Live != nil {
		if g, err := read(dir, *p.Live, p); err == nil {
			earlier = &g
		}
	}
	restored, err := r.restorable(previous, p)
	if err == nil {
		err = r.toward(restore, earlier, func() error {
			if err := r.place(next, restored); err != nil {
				return err
			}
			// Until a reload succeeds networkd may still run the new files: the timer keeps this generation.
			return r.settle(restore, next, restored)
		})
	}
	if err != nil {
		// What was live could not be fully restored; the armed timer still owns this generation.
		return fmt.Errorf("activate %d: %s; restoring what ran before failed: %w", id, reason, err)
	}
	return r.failed(id, p, reason)
}

// failed records an activation that was undone.
func (r Realizer) failed(id int, p Pointers, reason string) error {
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
		return previous, nil // the bare set already carries its content
	}
	g, err := read(r.dir(), *p.Live, p)
	if err != nil {
		return nil, err
	}
	return r.files(g)
}

// DeclaredDNS returns the servers a .network file names, in order.
func DeclaredDNS(data []byte) []netip.Addr {
	var servers []netip.Addr
	section := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "[") {
			section = line
		}
		// [IPv6SendRA] also has DNS=: the servers advertised to clients.
		if value, ok := strings.CutPrefix(line, "DNS="); ok && section == "[Network]" {
			if a, err := netip.ParseAddr(value); err == nil {
				servers = append(servers, a)
			}
		}
	}
	return servers
}

func (r Realizer) swapAndVerify(ctx context.Context, previous, next []placed, names []string, route bool) string {
	if err := r.place(previous, next); err != nil {
		return err.Error()
	}
	if err := r.settle(ctx, previous, next); err != nil {
		return err.Error()
	}
	// networkd hands DNS servers to systemd-resolved only; where resolved does not manage resolv.conf there is nothing to check.
	withDNS := host.ResolvedInCharge(r.Root) && slices.ContainsFunc(next, func(f placed) bool { return len(DeclaredDNS(f.data)) > 0 })
	verify, cancel := context.WithTimeout(ctx, r.VerifyWait)
	defer cancel()
	reason := "networkd was not observed"
	for {
		if o, err := r.observe(verify, withDNS); err != nil {
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
func (r Realizer) Rollback(ctx context.Context, id int, reason string) (result error) {
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
		} else if found && pending.GenerationID == id {
			defer func() { result = errors.Join(result, authorization.ClearPending(id, "")) }()
		}
	}
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
	currentDigest, err := generationAuthorizationDigest(dir, p, p.Live)
	if err != nil {
		return err
	}
	if authorization != nil {
		if err = advanceAuthorization(authorization, currentDigest); err != nil {
			return err
		}
	}
	// The timer is armed before activation, so it can fire inside the window the operator was shown; it retries until the deadline.
	if reason == "deadline" && g.State.State == Awaiting && g.Deadline != nil && r.Now().Before(*g.Deadline) {
		return fmt.Errorf("roll back %d: the confirmation deadline has not passed", id)
	}
	// from is this generation whatever the live pointer says: a kill may have left the pointer behind the files.
	from := make([]placed, 0, len(g.Files))
	for _, f := range g.Files {
		if allowed(f.Path) && f.Path != filterEntry {
			from = append(from, placed{path: f.Path})
		}
	}
	target, live, what, loaded, err := r.fallback(ctx, p, g.Previous)
	if err != nil {
		if errors.Is(err, errFilterLoad) {
			return fmt.Errorf("roll back %d: %w", id, err) // still pending: the timer tries again
		}
		p.Pending = nil
		if e := r.writePointers("rollback:pointers", p); e != nil {
			return e
		}
		if e := r.writeState("rollback:state", id, State{State: Failed, Previous: g.Previous, Reason: reason + ": nothing to restore: " + err.Error()}); e != nil {
			return e
		}
		return fmt.Errorf("roll back %d: nothing to restore: %w", id, err)
	}
	// Files first: once they are back, any later reload or boot is correct. But only a reload makes networkd stop
	// running the unconfirmed files, so until one succeeds the generation stays pending and the timer retries.
	// fallback already loaded the restored generation's filter; going back to none, the bare filter goes first.
	restore := func() error {
		if err := r.place(from, target); err != nil {
			return err
		}
		if err := r.settle(ctx, from, target); err != nil {
			return fmt.Errorf("files restored, reload failed: %w", err)
		}
		return nil
	}
	if loaded {
		err = restore()
	} else {
		// A restored generation without a filter runs without the table; only no generation at all gets the bare filter.
		var restored *Generation
		if live != nil {
			// Unreadable now, it cannot be told apart from no generation: refuse rather than fence its files.
			g, readErr := read(dir, *live, p)
			if readErr != nil {
				return fmt.Errorf("roll back %d: read generation %d: %w", id, *live, readErr)
			}
			restored = &g
		}
		err = r.toward(ctx, restored, restore)
	}
	if err != nil {
		return fmt.Errorf("roll back %d: %w", id, err)
	}
	if what != "" {
		reason += " (restored " + what + ")"
	}
	targetDigest, err := generationAuthorizationDigest(dir, p, live)
	if err != nil {
		return err
	}
	if err = advanceAuthorization(authorization, targetDigest); err != nil {
		return err
	}
	p.Pending, p.Live = nil, live
	if err := r.writePointers("rollback:pointers", p); err != nil {
		restoreErr := advanceAuthorization(authorization, currentDigest)
		return errors.Join(err, restoreErr)
	}
	return r.writeState("rollback:state", id, State{State: RolledBack, Previous: g.Previous, Reason: reason})
}

// fallback picks what to restore: the previous generation, else last-known-good, else the bare set.
// A generation is chosen only once its filter has loaded (loaded true), so one whose filter fails gives way to the next.
// what is empty when the choice is the one the generation recorded.
func (r Realizer) fallback(ctx context.Context, p Pointers, previous *int) (set []placed, live *int, what string, loaded bool, err error) {
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
		var path, comment string
		var ok bool
		if err == nil {
			path, comment, ok, err = r.filter(g)
		}
		if err == nil && ok {
			if err = r.loadFilter(ctx, path, comment); err != nil {
				err = fmt.Errorf("%w: %w", errFilterLoad, err)
			}
		}
		if err == nil {
			return set, candidate.id, candidate.what, ok, nil
		}
		failures = append(failures, err)
	}
	if previous == nil {
		return r.bare(), nil, "", false, nil // the rollback of the first apply goes back to no generation
	}
	if !r.resetTree() {
		// Removing every file would take the host off the network; what runs now at least works.
		return nil, nil, "", false, errors.Join(failures...)
	}
	// A reset tree has no generation to fall back to but its fenced bare set.
	return r.bare(), nil, "baseline", false, nil
}

// errFilterLoad marks a generation whose filter nft did not take, which a later try may: rollback stays pending.
var errFilterLoad = errors.New("its packet filter does not load")

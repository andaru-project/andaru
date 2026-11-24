package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/notify"
)

// noticeSource names the engine's own notifications: they travel on notification/engine.
const noticeSource = "engine"

// The disk notification rises at these shares of the state directory's file system in use, and clears below the last.
const (
	diskWarning  = 0.90
	diskCritical = 0.97
	diskClear    = 0.85
)

const (
	upgradeKey = "upgrade"
	diskKey    = "disk/state"
)

// notices states the engine's notifications: applies that failed or were rolled back since the last confirmed one,
// saved settings that wait for upgrade decisions, and a filling disk. Each run works all of them out again and
// publishes what changed since the last; the first publishes them all, so one lost to a crash comes back.
type notices struct {
	publish  func(notify.Raise) error
	stateDir string
	upgrade  func(context.Context) (bool, error)
	disk     func() (float64, error)
	// sent is what the engine last said of each of its notifications that is raised.
	sent map[string]notify.Raise
	poke chan struct{}
}

func newNotices(events *bus.Bus, stateDir string, upgrade func(context.Context) (bool, error)) *notices {
	return &notices{
		publish:  func(r notify.Raise) error { return notify.Publish(events, noticeSource, r) },
		stateDir: stateDir,
		upgrade:  upgrade,
		disk:     func() (float64, error) { return diskUsed(stateDir) },
		sent:     map[string]notify.Raise{},
		poke:     make(chan struct{}, 1),
	}
}

// nudge asks for a run soon: something one of the notifications depends on changed.
func (n *notices) nudge() {
	select {
	case n.poke <- struct{}{}:
	default:
	}
}

// run states everything once, given what the layer shows as active, then again at each nudge and each minute.
func (n *notices) run(ctx context.Context, active []notify.Notification) {
	for _, a := range active {
		if a.Source == noticeSource {
			n.sent[a.Key] = a.Raise
		}
	}
	n.state(ctx, true)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.poke:
		case <-tick.C:
		}
		n.state(ctx, false)
	}
}

func (n *notices) state(ctx context.Context, all bool) {
	want := map[string]notify.Raise{}
	// A part that cannot be read keeps what was said of it, rather than clearing it.
	keep := func(part string, err error, match func(string) bool) {
		slog.Warn("notifications: read "+part, "error", err)
		for k, r := range n.sent {
			if match(k) {
				want[k] = r
			}
		}
	}
	if err := n.applies(want); err != nil {
		keep("applies", err, func(k string) bool { return strings.HasPrefix(k, "apply/") })
	}
	if pending, err := n.upgrade(ctx); err != nil {
		keep("upgrade decisions", err, func(k string) bool { return k == upgradeKey })
	} else if pending {
		want[upgradeKey] = notify.Raise{Key: upgradeKey, Severity: notify.Warning,
			Subject: "The settings wait for decisions after an upgrade",
			Detail:  "Nothing can be saved or applied until they are answered.",
			Link:    "/system/upgrade", Requires: string(authz.ConfigurationRead)}
	}
	if used, err := n.disk(); err != nil {
		keep("disk use", err, func(k string) bool { return k == diskKey })
	} else if r, ok := diskNotice(used, n.sent[diskKey].Severity != ""); ok {
		want[diskKey] = r
	}
	for k, r := range want {
		if (all || n.sent[k] != r) && n.say(r) {
			n.sent[k] = r
		}
	}
	for k := range n.sent {
		if _, ok := want[k]; !ok && n.say(notify.Raise{Key: k}) {
			delete(n.sent, k)
		}
	}
}

// say publishes r, a clear when it has no severity. A failure is tried again at the next run.
func (n *notices) say(r notify.Raise) bool {
	if err := n.publish(r); err != nil {
		slog.Warn("notifications: publish", "key", r.Key, "error", err)
		return false
	}
	return true
}

// applies adds each apply that failed or was rolled back after the last confirmed one.
func (n *notices) applies(want map[string]notify.Raise) error {
	p, gens, err := generation.List(generation.Dir(n.stateDir))
	if err != nil {
		return err
	}
	for _, g := range gens {
		// One never started is also one whose apply is starting now, and one that could not start said so to its caller.
		if p.Confirmed != nil && g.ID <= *p.Confirmed || g.Reason == "never started" {
			continue
		}
		r := notify.Raise{Key: fmt.Sprintf("apply/%d", g.ID), Detail: g.Reason, Link: "/changes", Requires: string(authz.ConfigurationRead)}
		switch g.State.State {
		case generation.Failed:
			r.Severity, r.Subject = notify.Warning, fmt.Sprintf("Apply %d failed", g.ID)
		case generation.RolledBack:
			// A rollback nobody asked for means the network did not come up as applied, or nobody confirmed it.
			r.Severity, r.Subject = notify.Critical, fmt.Sprintf("Apply %d was rolled back", g.ID)
			if strings.HasPrefix(g.Reason, "requested") {
				r.Severity = notify.Warning
			}
		default:
			continue
		}
		want[r.Key] = r
	}
	return nil
}

// diskNotice is the disk notification for used, the share of the file system in use; raised says whether it is raised now.
func diskNotice(used float64, raised bool) (notify.Raise, bool) {
	r := notify.Raise{Key: diskKey, Severity: notify.Warning, Subject: "The disk holding Andaru's state is over 90% full",
		Detail: "Andaru keeps its settings, generations and logs there. Free some space before it runs out."}
	switch {
	case used >= diskCritical:
		r.Severity, r.Subject = notify.Critical, "The disk holding Andaru's state is over 97% full"
	case used >= diskWarning, raised && used >= diskClear:
	default:
		return notify.Raise{}, false
	}
	return r, true
}

// diskUsed is the share of dir's file system in use, as df counts it: blocks kept for root count as unavailable.
func diskUsed(dir string) (float64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, err
	}
	used := s.Blocks - s.Bfree
	if used+s.Bavail == 0 {
		return 0, nil
	}
	return float64(used) / float64(used+s.Bavail), nil
}

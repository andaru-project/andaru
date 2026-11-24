package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
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

// The disk notification rises to a warning and to critical at these shares of the state directory's file system in
// use, and falls back below the lower mark of each, so a disk that hovers at one mark does not flip it every minute.
const (
	diskWarning  = 0.90
	diskClear    = 0.85
	diskCritical = 0.97
	diskEased    = 0.95
)

const (
	upgradeKey = "upgrade"
	diskKey    = "disk/state"
)

// notices states the engine's notifications: the last apply, when it failed or was rolled back after the last
// confirmed one, saved settings that wait for upgrade decisions, and a filling disk. Each run works all of them out
// again and publishes what changed since the last. The first publishes them all, so one lost to a crash comes back,
// and clears every one that may be raised and is not, since what the layer shows at start may still be behind.
type notices struct {
	publish  func(notify.Raise) error
	stateDir string
	upgrade  func(context.Context) (bool, error)
	disk     func() (float64, error)
	// sent is what the engine last said of each of its notifications that is raised.
	sent map[string]notify.Raise
	// interrupted is the apply the last run found interrupted: one is raised only when two runs in a row find it,
	// since an apply between two of its writes reads as interrupted for a moment.
	interrupted string
	// failing is the error each part last failed to read with, so a part that stays unreadable is logged once.
	failing map[string]string
	poke    chan struct{}
}

func newNotices(events *bus.Bus, stateDir string, upgrade func(context.Context) (bool, error)) *notices {
	return &notices{
		publish:  func(r notify.Raise) error { return notify.Publish(events, noticeSource, r) },
		stateDir: stateDir,
		upgrade:  upgrade,
		disk:     func() (float64, error) { return diskUsed(stateDir) },
		sent:     map[string]notify.Raise{},
		failing:  map[string]string{},
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
	// may are the keys that may be raised from an earlier start, cleared by the first run when not wanted.
	may := []string{upgradeKey, diskKey}
	// A part that cannot be read keeps what was said of it, rather than clearing it.
	read := func(part string, err error, match func(string) bool) bool {
		if err == nil {
			delete(n.failing, part)
			return true
		}
		if n.failing[part] != err.Error() {
			n.failing[part] = err.Error()
			slog.Warn("notifications: read "+part, "error", err)
		}
		for k, r := range n.sent {
			if match(k) {
				want[k] = r
			}
		}
		may = slices.DeleteFunc(may, match)
		return false
	}
	ended, err := n.applies(want)
	if read("applies", err, func(k string) bool { return strings.HasPrefix(k, "apply/") }) {
		may = append(may, ended...)
	}
	if pending, err := n.upgrade(ctx); read("upgrade decisions", err, func(k string) bool { return k == upgradeKey }) && pending {
		want[upgradeKey] = notify.Raise{Key: upgradeKey, Severity: notify.Warning,
			Subject: "The settings wait for decisions after an upgrade",
			Detail:  "Nothing can be saved or applied until they are answered.",
			Link:    "/system/upgrade", Requires: string(authz.ConfigurationRead)}
	}
	if used, err := n.disk(); read("disk use", err, func(k string) bool { return k == diskKey }) {
		if r, ok := diskNotice(used, n.sent[diskKey].Severity); ok {
			want[diskKey] = r
		}
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
	if all {
		for _, k := range may {
			if _, ok := want[k]; !ok {
				n.say(notify.Raise{Key: k})
			}
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

// applies adds the last apply that started, when it failed or was rolled back after the last confirmed one. It
// returns the other applies that failed or were rolled back, which an earlier run may have raised.
func (n *notices) applies(want map[string]notify.Raise) ([]string, error) {
	p, gens, err := generation.List(generation.Dir(n.stateDir))
	if err != nil {
		return nil, err
	}
	var ended []string
	last, interrupted := true, ""
	for _, g := range gens { // the newest first
		// One never started is also one whose apply is starting now, and one that could not start said so to its caller.
		if g.Reason == "never started" {
			continue
		}
		r := notify.Raise{Key: fmt.Sprintf("apply/%d", g.ID), Detail: notify.Detail(g.Reason), Link: "/changes", Requires: string(authz.ConfigurationRead)}
		switch g.State.State {
		case generation.Failed:
			r.Severity, r.Subject = notify.Warning, fmt.Sprintf("Apply %d failed", g.ID)
		case generation.RolledBack:
			// A rollback nobody asked for means the network did not come up as applied, or nobody confirmed it.
			r.Severity, r.Subject = notify.Critical, fmt.Sprintf("Apply %d was rolled back", g.ID)
			if strings.HasPrefix(g.Reason, "requested") {
				r.Severity = notify.Warning
			}
		}
		switch {
		case r.Severity == "":
		case !last || p.Confirmed != nil && g.ID <= *p.Confirmed:
			ended = append(ended, r.Key)
		case g.Reason == "interrupted" && n.interrupted != r.Key && n.sent[r.Key] != r:
			interrupted = r.Key
		default:
			want[r.Key] = r
		}
		last = false
	}
	n.interrupted = interrupted
	return ended, nil
}

// diskNotice is the disk notification for used, the share of the file system in use, given the severity it has now.
func diskNotice(used float64, now string) (notify.Raise, bool) {
	switch {
	case used >= diskCritical, now == notify.Critical && used >= diskEased:
		return notify.Raise{Key: diskKey, Severity: notify.Critical, Subject: "The disk holding Andaru's state is almost full",
			Detail: "Andaru keeps its settings, generations and logs there. Free some space now: a full disk stops saves and applies."}, true
	case used >= diskWarning, now != "" && used >= diskClear:
		return notify.Raise{Key: diskKey, Severity: notify.Warning, Subject: "The disk holding Andaru's state is filling up",
			Detail: "Andaru keeps its settings, generations and logs there. Free some space before it runs out."}, true
	}
	return notify.Raise{}, false
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

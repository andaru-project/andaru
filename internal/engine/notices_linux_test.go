package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/notify"
)

// noticeBench is notices with its inputs in the test's hands and what it published in said.
type noticeBench struct {
	*notices
	dir     string
	waiting bool
	used    float64
	fail    error
	said    []notify.Raise
}

func newNoticeBench(t *testing.T) *noticeBench {
	b := &noticeBench{dir: t.TempDir()}
	b.notices = newNotices(nil, b.dir, func(context.Context) (bool, error) { return b.waiting, b.fail })
	b.publish = func(r notify.Raise) error { b.said = append(b.said, r); return nil }
	b.disk = func() (float64, error) { return b.used, b.fail }
	return b
}

// generation writes generation id in state with reason, and the confirmed pointer.
func (b *noticeBench) generation(t *testing.T, id int, state, reason string, confirmed string) {
	t.Helper()
	own := filepath.Join(generation.Dir(b.dir), fmt.Sprint(id))
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(own, "manifest.json"), fmt.Sprintf(`{"id":%d,"files":[]}`, id))
	if state != "" {
		body, err := json.Marshal(generation.State{State: state, Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(own, "state.json"), string(body))
	}
	write(filepath.Join(generation.Dir(b.dir), "pointers.json"), fmt.Sprintf(`{"confirmed":%s,"lastKnownGood":null,"pending":null,"live":null}`, confirmed))
}

// pending points the pending pointer at id, as for an apply that waits for confirmation.
func (b *noticeBench) pending(t *testing.T, id int) {
	path := filepath.Join(generation.Dir(b.dir), "pointers.json")
	if err := os.WriteFile(path, fmt.Appendf(nil, `{"confirmed":1,"lastKnownGood":null,"pending":%d,"live":%d}`, id, id), 0o600); err != nil {
		t.Fatal(err)
	}
}

// run states once, after a start that found active when start, and returns what was published, by key; a clear has no severity.
func (b *noticeBench) run(start bool, active ...notify.Notification) map[string]notify.Raise {
	b.said = nil
	if start {
		b.begin(active)
	}
	b.state(context.Background())
	out := map[string]notify.Raise{}
	for _, r := range b.said {
		out[r.Key] = r
	}
	return out
}

func TestNoticesApplies(t *testing.T) {
	b := newNoticeBench(t)
	b.generation(t, 1, generation.Confirmed, "", "1")
	b.generation(t, 2, generation.Failed, "networkctl: exit 1:\tbad\x1b[0m", "1")
	b.generation(t, 3, "", "", "1") // created, not started yet
	said := b.run(true)
	if len(said) != 3 || said["apply/2"].Severity != notify.Warning || said["apply/2"].Detail != "networkctl: exit 1: bad [0m" ||
		said["apply/2"].Requires != "configuration.read" || said[upgradeKey].Severity != "" || said[diskKey].Severity != "" {
		t.Fatalf("said %+v", said)
	}
	if said := b.run(false); len(said) != 0 {
		t.Fatalf("nothing changed, said %+v", said)
	}
	// Each later apply stands for the ones before it.
	for _, step := range []struct {
		id            int
		state, reason string
		want          string
	}{{4, generation.RolledBack, "deadline", notify.Critical}, {5, generation.RolledBack, "requested", notify.Warning}, {6, generation.Awaiting, "", ""}} {
		b.generation(t, step.id, step.state, step.reason, "1")
		if step.state == generation.Awaiting {
			b.pending(t, step.id)
		}
		said = b.run(false)
		was := fmt.Sprintf("apply/%d", step.id-1)
		if said[was].Severity != "" || said[fmt.Sprintf("apply/%d", step.id)].Severity != step.want || len(said) != 1+min(1, len(step.want)) {
			t.Fatalf("apply %d: said %+v", step.id, said)
		}
	}
	// One read between two writes of its apply looks interrupted: it is raised once a second run finds it so too.
	b.generation(t, 7, generation.Activating, "", "6")
	if said := b.run(false); len(said) != 0 {
		t.Fatalf("said %+v", said)
	}
	if said := b.run(false); said["apply/7"].Severity != notify.Critical || len(said) != 1 {
		t.Fatalf("said %+v", said)
	}
	// After a start, what is still so is said again, and what may be raised from before is cleared.
	said = b.run(true, notify.Notification{Source: noticeSource, Raise: b.sent["apply/7"]})
	if said["apply/7"].Severity != notify.Critical || said["apply/2"].Severity != "" || said["apply/5"].Severity != "" {
		t.Fatalf("said %+v", said)
	}
}

func TestNoticesUpgradeAndDisk(t *testing.T) {
	b := newNoticeBench(t)
	b.waiting, b.used = true, 0.91
	said := b.run(false)
	if said[upgradeKey].Link != "/system/upgrade" || said[diskKey].Severity != notify.Warning {
		t.Fatalf("said %+v", said)
	}
	for _, step := range []struct {
		used float64
		want string // the disk notification's severity once said, "" for cleared, "-" for nothing said
	}{{0.97, notify.Critical}, {0.96, "-"}, {0.94, notify.Warning}, {0.86, "-"}, {0.96, "-"}, {0.849, ""}, {0.89, "-"}, {0.90, notify.Warning}} {
		b.used = step.used
		r, ok := b.run(false)[diskKey]
		if step.want == "-" && ok || step.want != "-" && (!ok || r.Severity != step.want) {
			t.Fatalf("at %v: said %+v %v, want %q", step.used, r, ok, step.want)
		}
	}
	// What cannot be read is left as it was said, not cleared.
	b.fail = errors.New("unreadable")
	if said := b.run(false); len(said) != 0 {
		t.Fatalf("said %+v", said)
	}
	b.fail, b.waiting = nil, false
	if said := b.run(false); len(said) != 1 || said[upgradeKey].Severity != "" {
		t.Fatalf("said %+v", said)
	}
}

func TestNoticesRetryWhatFailedToPublish(t *testing.T) {
	b := newNoticeBench(t)
	b.waiting = true
	b.publish = func(notify.Raise) error { return errors.New("bus closed") }
	b.run(false)
	b.publish = func(r notify.Raise) error { b.said = append(b.said, r); return nil }
	if said := b.run(false); said[upgradeKey].Severity != notify.Warning {
		t.Fatalf("said %+v", said)
	}
}

// What may be raised from before a start is cleared once it can be, even when the first try cannot.
func TestNoticesStartClearsRetry(t *testing.T) {
	b := newNoticeBench(t)
	b.generation(t, 1, generation.Failed, "bad", "null")
	b.generation(t, 2, generation.Confirmed, "", "2")
	pointers := filepath.Join(generation.Dir(b.dir), "pointers.json")
	if err := os.WriteFile(pointers, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.publish = func(notify.Raise) error { return errors.New("bus closed") }
	b.run(true)
	b.publish = func(r notify.Raise) error { b.said = append(b.said, r); return nil }
	// The publish works again, the applies cannot be read yet: the upgrade and the disk are cleared.
	if said := b.run(false); len(said) != 2 || said[upgradeKey].Severity != "" || said[diskKey].Severity != "" {
		t.Fatalf("said %+v", said)
	}
	b.generation(t, 2, generation.Confirmed, "", "2")
	if said := b.run(false); len(said) != 1 || said["apply/1"].Key == "" || said["apply/1"].Severity != "" {
		t.Fatalf("said %+v", said)
	}
	if said := b.run(false); len(said) != 0 {
		t.Fatalf("said %+v", said)
	}
}

// A part that cannot be read at start is neither said nor cleared until it can be.
func TestNoticesStartWaitsForUnreadable(t *testing.T) {
	b := newNoticeBench(t)
	b.fail = errors.New("unreadable")
	if said := b.run(true, notify.Notification{Source: noticeSource, Raise: notify.Raise{Key: diskKey, Severity: notify.Warning}}); len(said) != 0 {
		t.Fatalf("said %+v", said)
	}
	b.fail, b.used = nil, 0.88
	if said := b.run(false); len(said) != 2 || said[diskKey].Severity != notify.Warning || said[upgradeKey].Severity != "" {
		t.Fatalf("said %+v", said)
	}
}

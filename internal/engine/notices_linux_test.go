package engine

import (
	"context"
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
	pending bool
	used    float64
	fail    error
	said    []notify.Raise
}

func newNoticeBench(t *testing.T) *noticeBench {
	b := &noticeBench{dir: t.TempDir()}
	b.notices = &notices{stateDir: b.dir, sent: map[string]notify.Raise{}, poke: make(chan struct{}, 1),
		publish: func(r notify.Raise) error { b.said = append(b.said, r); return nil },
		upgrade: func(context.Context) (bool, error) { return b.pending, b.fail },
		disk:    func() (float64, error) { return b.used, b.fail },
	}
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
		write(filepath.Join(own, "state.json"), fmt.Sprintf(`{"state":%q,"previous":null,"reason":%q}`, state, reason))
	}
	write(filepath.Join(generation.Dir(b.dir), "pointers.json"), fmt.Sprintf(`{"confirmed":%s,"lastKnownGood":null,"pending":null,"live":null}`, confirmed))
}

// run states once and returns what was published, by key; a clear has no severity.
func (b *noticeBench) run(all bool) map[string]notify.Raise {
	b.said = nil
	b.state(context.Background(), all)
	out := map[string]notify.Raise{}
	for _, r := range b.said {
		out[r.Key] = r
	}
	return out
}

func TestNoticesApplies(t *testing.T) {
	b := newNoticeBench(t)
	b.generation(t, 1, generation.Confirmed, "", "1")
	b.generation(t, 2, generation.Failed, "networkd refused it", "1")
	b.generation(t, 3, generation.RolledBack, "deadline", "1")
	b.generation(t, 4, generation.RolledBack, "requested", "1")
	b.generation(t, 5, "", "", "1") // created, not started yet
	said := b.run(true)
	if len(said) != 3 || said["apply/2"].Severity != notify.Warning || said["apply/2"].Detail != "networkd refused it" ||
		said["apply/3"].Severity != notify.Critical || said["apply/4"].Severity != notify.Warning || said["apply/3"].Requires != "configuration.read" {
		t.Fatalf("said %+v", said)
	}
	if said := b.run(false); len(said) != 0 {
		t.Fatalf("nothing changed, said %+v", said)
	}
	// A later confirmed apply clears them all.
	b.generation(t, 6, generation.Confirmed, "", "6")
	said = b.run(false)
	if len(said) != 3 || said["apply/2"].Severity != "" || said["apply/3"].Severity != "" || said["apply/4"].Severity != "" {
		t.Fatalf("said %+v", said)
	}
	// The first run after a start says everything again, and clears what the layer shows that is no longer so.
	b.sent = map[string]notify.Raise{"apply/2": {Key: "apply/2"}}
	if said := b.run(true); len(said) != 1 || said["apply/2"].Severity != "" {
		t.Fatalf("said %+v", said)
	}
}

func TestNoticesUpgradeAndDisk(t *testing.T) {
	b := newNoticeBench(t)
	b.pending, b.used = true, 0.91
	said := b.run(false)
	if said[upgradeKey].Link != "/system/upgrade" || said[diskKey].Severity != notify.Warning {
		t.Fatalf("said %+v", said)
	}
	for _, step := range []struct {
		used float64
		want string // the disk notification's severity once said, "" for cleared, "-" for nothing said
	}{{0.97, notify.Critical}, {0.92, notify.Warning}, {0.86, "-"}, {0.95, "-"}, {0.849, ""}, {0.89, "-"}, {0.90, notify.Warning}} {
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
	b.fail, b.pending = nil, false
	if said := b.run(false); len(said) != 1 || said[upgradeKey].Severity != "" {
		t.Fatalf("said %+v", said)
	}
}

func TestNoticesRetryWhatFailedToPublish(t *testing.T) {
	b := newNoticeBench(t)
	b.pending = true
	b.publish = func(notify.Raise) error { return errors.New("bus closed") }
	b.run(false)
	b.publish = func(r notify.Raise) error { b.said = append(b.said, r); return nil }
	if said := b.run(false); said[upgradeKey].Severity != notify.Warning {
		t.Fatalf("said %+v", said)
	}
}

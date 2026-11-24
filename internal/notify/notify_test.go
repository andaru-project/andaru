package notify

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
)

type bench struct {
	t       *testing.T
	dir     string
	events  *bus.Bus
	layer   *Layer
	changes chan struct{}
}

func newBench(t *testing.T, lost bool) *bench {
	b := &bench{t: t, dir: t.TempDir(), changes: make(chan struct{}, 64)}
	b.open(lost)
	t.Cleanup(b.close)
	return b
}

func (b *bench) open(lost bool) {
	var err error
	if b.events, err = bus.Open(bus.Options{Path: filepath.Join(b.dir, "bus.db"), LostHistory: lost}); err != nil {
		b.t.Fatal(err)
	}
	if _, err = b.events.Subscribe(ChangedTopic, func(string, []byte) { b.changes <- struct{}{} }); err != nil {
		b.t.Fatal(err)
	}
	if b.layer, err = Open(filepath.Join(b.dir, "notifications.db"), b.events, slog.New(slog.DiscardHandler)); err != nil {
		b.t.Fatal(err)
	}
}

func (b *bench) close() {
	if b.layer != nil {
		b.layer.Close()
		b.events.Close()
		b.layer = nil
	}
}

func (b *bench) publish(source string, r Raise) {
	b.t.Helper()
	if err := Publish(b.events, source, r); err != nil {
		b.t.Fatal(err)
	}
}

// changed waits for a notifications/changed event; settled checks none came.
func (b *bench) changed() {
	b.t.Helper()
	select {
	case <-b.changes:
	case <-time.After(5 * time.Second):
		b.t.Fatal("no change told")
	}
}

func (b *bench) settled() {
	b.t.Helper()
	select {
	case <-b.changes:
		b.t.Fatal("a change told for nothing")
	case <-time.After(200 * time.Millisecond):
	}
}

func (b *bench) active() []Notification {
	b.t.Helper()
	list, err := b.layer.Active()
	if err != nil {
		b.t.Fatal(err)
	}
	return list
}

func (b *bench) history() int {
	var n int
	if err := b.layer.db.QueryRow(`SELECT count(*) FROM history`).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

var full = Raise{Key: "var", Severity: Warning, Subject: "The disk is filling up", Detail: "91% used", Link: "/system", Requires: "status.read"}

func TestRaiseUpdateClear(t *testing.T) {
	b := newBench(t, false)
	b.publish("disk", full)
	b.changed()
	if got := b.active(); len(got) != 1 || got[0].Source != "disk" || got[0].Raise != full {
		t.Fatalf("active %+v", got)
	}
	b.publish("disk", full) // the same again, as at each start
	b.settled()
	worse := full
	worse.Severity, worse.Detail = Critical, "98% used"
	b.publish("disk", worse)
	b.changed()
	if got := b.active(); len(got) != 1 || got[0].Raise != worse {
		t.Fatalf("active %+v", got)
	}
	b.publish("disk", Raise{Key: "var"})
	b.changed()
	if got := b.active(); len(got) != 0 || b.history() != 1 {
		t.Fatalf("active %+v, history %d", got, b.history())
	}
	b.publish("disk", Raise{Key: "var"}) // a clear of nothing
	b.settled()
}

// An event delivered again, after a crash between the store and the bus's cursor, changes nothing twice.
func TestEventSeenOnce(t *testing.T) {
	b := newBench(t, false)
	payload, err := bus.NewEvent(RaisedType, "disk", full)
	if err != nil {
		t.Fatal(err)
	}
	m := bus.Message{Seq: 1, Topic: "notification/disk", Payload: payload}
	if err = b.layer.handle(m); err != nil {
		t.Fatal(err)
	}
	b.changed()
	if _, err = b.layer.Dismiss(b.active()[0].ID, "usr_1"); err != nil {
		t.Fatal(err)
	}
	b.changed()
	if err = b.layer.handle(m); err != nil {
		t.Fatal(err)
	}
	b.settled()
	if got := b.active(); len(got) != 0 {
		t.Fatalf("the event raised again: %+v", got)
	}
}

func TestDismissAndRaiseAgain(t *testing.T) {
	b := newBench(t, false)
	b.publish("disk", full)
	b.changed()
	id := b.active()[0].ID
	if ok, err := b.layer.Dismiss(id, "usr_1"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	b.changed()
	var by string
	if err := b.layer.db.QueryRow(`SELECT dismissed_by FROM history`).Scan(&by); err != nil || by != "usr_1" {
		t.Fatalf("dismissed by %q: %v", by, err)
	}
	if ok, err := b.layer.Dismiss(id, "usr_1"); err != nil || ok {
		t.Fatal("dismissed twice", ok, err)
	}
	b.publish("disk", full)
	b.changed()
	if got := b.active(); len(got) != 1 {
		t.Fatalf("a dismissed notification raised again is not back: %+v", got)
	}
}

func TestRefusedEventsAreSkipped(t *testing.T) {
	b := newBench(t, false)
	forged, _ := bus.NewEvent(RaisedType, "applies", full) // on another source's topic
	if err := b.events.Publish("notification/disk", forged, true); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Raise{
		{Key: "x", Severity: "loud", Subject: "s"},
		{Key: "x", Severity: Info, Subject: ""},
		{Key: "x", Severity: Info, Subject: "s", Link: "https://example.com"},
		{Key: "x", Severity: Info, Subject: "s", Link: "//example.com"},
		{Key: "x", Severity: Info, Subject: "s", Requires: "everything"},
		{Key: "", Severity: Info, Subject: "s"},
	} {
		b.publish("disk", r)
	}
	b.publish("disk", full) // read past them
	b.changed()
	b.settled()
	if got := b.active(); len(got) != 1 || got[0].Source != "disk" || got[0].Raise != full {
		t.Fatalf("active %+v", got)
	}
}

func TestListSurvivesRestart(t *testing.T) {
	b := newBench(t, false)
	b.publish("disk", full)
	b.changed()
	b.close()
	b.open(false)
	if got := b.active(); len(got) != 1 || got[0].Raise != full {
		t.Fatalf("active %+v", got)
	}
	b.settled() // the event was done with: it is not read again
}

func TestGapRaisesMissed(t *testing.T) {
	b := newBench(t, false)
	b.close()
	for _, suffix := range []string{"", "-wal", "-shm"} { // the bus's store was replaced, as openBus does with one it cannot read
		if err := os.Remove(filepath.Join(b.dir, "bus.db"+suffix)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	b.open(true)
	b.changed()
	got := b.active()
	if len(got) != 1 || got[0].Source != "notifications" || got[0].Key != "missed" || got[0].Severity != Warning {
		t.Fatalf("active %+v", got)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	b := newBench(t, false)
	tx, err := b.layer.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range historySize + 10 {
		r := full
		r.Key = fmt.Sprint(i)
		if _, err = raise(tx, "disk", r, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err = end(tx, `source = 'disk' AND key = ?`, []any{r.Key}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := b.history(); n != historySize {
		t.Fatalf("history holds %d", n)
	}
}

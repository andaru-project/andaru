package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	sent    chan sent
}

// sent is a raise the layer handed on to the channels.
type sent struct {
	event   bus.Event
	raise   Raise
	payload string
}

func newBench(t *testing.T) *bench {
	b := &bench{t: t, dir: t.TempDir(), changes: make(chan struct{}, 64), sent: make(chan sent, 64)}
	b.open(false)
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
	if b.layer, err = Open(filepath.Join(b.dir, "notifications.db"), b.events, slog.New(slog.DiscardHandler), func(e bus.Event, r Raise, payload []byte) error {
		b.sent <- sent{e, r, string(payload)}
		return nil
	}); err != nil {
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

func all(string) bool { return true }

var full = Raise{Key: "var", Severity: Warning, Subject: "The disk is filling up", Detail: "91% used", Link: "/system", Requires: "status.read"}

func TestRaiseUpdateClear(t *testing.T) {
	b := newBench(t)
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
	b := newBench(t)
	payload, err := bus.NewEvent(RaisedType, "disk", full)
	if err != nil {
		t.Fatal(err)
	}
	m := bus.Message{Seq: 1, Topic: "notification/disk", Payload: payload}
	if err = b.layer.handle(m); err != nil {
		t.Fatal(err)
	}
	b.changed()
	if _, err = b.layer.Dismiss(b.active()[0].ID, "usr_1", all); err != nil {
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
	b := newBench(t)
	b.publish("disk", full)
	b.changed()
	id := b.active()[0].ID
	if ok, err := b.layer.Dismiss(id, "usr_1", all); err != nil || !ok {
		t.Fatal(ok, err)
	}
	b.changed()
	var by string
	if err := b.layer.db.QueryRow(`SELECT dismissed_by FROM history`).Scan(&by); err != nil || by != "usr_1" {
		t.Fatalf("dismissed by %q: %v", by, err)
	}
	if ok, err := b.layer.Dismiss(id, "usr_1", all); err != nil || ok {
		t.Fatal("dismissed twice", ok, err)
	}
	b.publish("disk", full)
	b.changed()
	if got := b.active(); len(got) != 1 {
		t.Fatalf("a dismissed notification raised again is not back: %+v", got)
	}
}

func TestRefusedEventsAreSkipped(t *testing.T) {
	b := newBench(t)
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
		{Key: "x", Severity: Info, Subject: "s", Link: "/\\evil.example"},
		{Key: "x", Severity: Info, Subject: "s", Link: "/\x7f"},
		{Key: "x", Severity: Info, Subject: "ok\u202egnp.exe"},
		{Key: "x", Severity: Info, Subject: "two\u2028lines"},
		{Key: "x", Severity: Info, Subject: "s", Detail: "a\u0085b"},
		{Key: "x\ny", Severity: Info, Subject: "s"},
		{Key: "x", Severity: Info, Subject: "\ufeffs"},
		{Key: "x\u200b", Severity: Info, Subject: "s"},
		{Key: "x", Severity: Info, Subject: "tag\U000e0041"},
	} {
		b.publish("disk", r)
	}
	for i, at := range []time.Time{{}, time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("", 3600))} {
		timeless, _ := json.Marshal(bus.Event{ID: fmt.Sprint("e", i), Type: RaisedType, Time: at, Source: "disk", Data: Raise{Key: "timeless", Severity: Info, Subject: "s"}})
		if err := b.events.Publish("notification/disk", timeless, true); err != nil {
			t.Fatal(err)
		}
	}
	joined := Raise{Key: "joined", Severity: Info, Subject: "Backup 👨\u200d👩\u200d👧 done", Detail: "می\u200cخواهم"} // joiners are text
	b.publish("disk", joined)
	b.changed()
	b.publish("disk", full) // read past them
	b.changed()
	b.settled()
	if got := b.active(); len(got) != 2 || got[0].Source != "disk" || got[0].Raise != full || got[1].Raise != joined {
		t.Fatalf("active %+v", got)
	}
}

func TestListSurvivesRestart(t *testing.T) {
	b := newBench(t)
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
	b := newBench(t)
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
	b := newBench(t)
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

// The same event ID from two sources is two events: one source cannot suppress another's by reusing its ID.
func TestEventSeenPerSource(t *testing.T) {
	b := newBench(t)
	for _, source := range []string{"addon", "disk"} {
		payload, _ := json.Marshal(bus.Event{ID: "same", Type: RaisedType, Time: time.Now(), Source: source, Data: full})
		if err := b.events.Publish("notification/"+source, payload, true); err != nil {
			t.Fatal(err)
		}
		b.changed()
	}
	if got := b.active(); len(got) != 2 {
		t.Fatalf("active %+v", got)
	}
}

// A source cannot date a notification into the future to keep it on top; one read late keeps its time.
func TestTimes(t *testing.T) {
	b := newBench(t)
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	before := time.Now().Truncate(time.Second)
	for i, at := range []time.Time{time.Now().AddDate(100, 0, 0), past} {
		payload, _ := json.Marshal(bus.Event{ID: fmt.Sprint(i), Type: RaisedType, Time: at, Source: fmt.Sprint("s", i), Data: full})
		if err := b.events.Publish(fmt.Sprint("notification/s", i), payload, true); err != nil {
			t.Fatal(err)
		}
		b.changed()
	}
	times := map[string]time.Time{}
	for _, n := range b.active() {
		if !n.Raised.Equal(n.Updated) {
			t.Fatalf("%s raised %v, updated %v", n.Source, n.Raised, n.Updated)
		}
		times[n.Source] = n.Raised
	}
	if future := times["s0"]; future.Before(before) || future.After(time.Now()) {
		t.Fatalf("one from the future dated %v", future)
	}
	if !times["s1"].Equal(past) {
		t.Fatalf("one from the past dated %v, want %v", times["s1"], past)
	}
}

// A change from a source whose clock went back does not date the notification earlier than it was.
func TestUpdatedNeverGoesBack(t *testing.T) {
	b := newBench(t)
	b.publish("disk", full)
	b.changed()
	first := b.active()[0].Updated
	changed := full
	changed.Detail = "92% used"
	payload, _ := json.Marshal(bus.Event{ID: "back", Type: RaisedType, Time: first.Add(-time.Hour), Source: "disk", Data: changed})
	if err := b.events.Publish("notification/disk", payload, true); err != nil {
		t.Fatal(err)
	}
	b.changed()
	if got := b.active()[0]; got.Detail != changed.Detail || got.Updated.Before(first) {
		t.Fatalf("%+v, updated before %v", got, first)
	}
}

// One that may not be seen is not dismissed.
func TestDismissOnlyWhatIsSeen(t *testing.T) {
	b := newBench(t)
	b.publish("disk", full)
	b.changed()
	if ok, err := b.layer.Dismiss(b.active()[0].ID, "usr_1", func(requires string) bool { return requires != "status.read" }); err != nil || ok {
		t.Fatal("dismissed one it may not see", ok, err)
	}
	if len(b.active()) != 1 {
		t.Fatal("it ended")
	}
}

// What is kept of ended notifications, and of events seen, goes after 30 days: at the next event, with nothing
// ending, and at the next start.
func TestOldRecordsArePruned(t *testing.T) {
	b := newBench(t)
	old := time.Now().Add(-historyAge - time.Hour).UnixMilli()
	plant := func() {
		if _, err := b.layer.db.Exec(`INSERT INTO history (source, key, severity, subject, raised, ended, dismissed_by) VALUES ('disk', 'old', 'info', 's', ?1, ?1, '');
			INSERT INTO seen (source, event, time) VALUES ('disk', 'old', ?1)`, old); err != nil {
			t.Fatal(err)
		}
	}
	kept := func() int {
		var n int
		if err := b.layer.db.QueryRow(`SELECT (SELECT count(*) FROM history WHERE key = 'old') + (SELECT count(*) FROM seen WHERE event = 'old')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	plant()
	b.publish("disk", full)
	b.changed()
	if n := kept(); n != 0 {
		t.Fatalf("an event kept %d old records", n)
	}
	plant()
	b.close()
	b.open(false)
	if n := kept(); n != 0 {
		t.Fatalf("a start kept %d old records", n)
	}
}

// A change within the same second as other raises still puts its notification first.
func TestChangeSortsFirst(t *testing.T) {
	b := newBench(t)
	other := full
	other.Key = "other"
	b.publish("disk", full)
	b.changed()
	time.Sleep(5 * time.Millisecond)
	b.publish("disk", other)
	b.changed()
	time.Sleep(5 * time.Millisecond)
	changed := full
	changed.Detail = "92% used"
	b.publish("disk", changed)
	b.changed()
	if got := b.active(); got[0].Key != full.Key {
		t.Fatalf("the changed one sorts after %q", got[0].Key)
	}
}

func TestDetailIsAccepted(t *testing.T) {
	for _, s := range []string{"exit 1:\tbad\r\n\x1b[0m‮ \xff", strings.Repeat("é", detailMax)} {
		if d := Detail(s); !text(d, detailMax, true) || len(s) > detailMax && len(d) < detailMax-1 {
			t.Errorf("Detail(%q) = %q", s[:min(len(s), 40)], d[:min(len(d), 40)])
		}
	}
	if d := Detail("line one\nline two"); d != "line one\nline two" {
		t.Errorf("lines changed: %q", d)
	}
}

// Each raise that changes the list is handed on with its event as published; one that changes nothing, and a clear,
// are not. One delivered again is handed on again, with the same ID.
func TestRaisesAreDelivered(t *testing.T) {
	b := newBench(t)
	payload, err := bus.NewEvent(RaisedType, "disk", full)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.events.Publish("notification/disk", payload, true); err != nil {
		t.Fatal(err)
	}
	got := receiveSent(t, b.sent)
	var published bus.Event
	if err = json.Unmarshal(payload, &published); err != nil {
		t.Fatal(err)
	}
	if got.payload != string(payload) || got.raise != full || got.event.ID != published.ID || got.event.Source != "disk" || !got.event.Time.Equal(published.Time) {
		t.Fatalf("sent %+v", got)
	}
	b.publish("disk", full) // changes nothing
	b.publish("disk", Raise{Key: "var"})
	b.publish("disk", Raise{Key: "other", Severity: Info, Subject: "s"})
	if got := receiveSent(t, b.sent); got.raise.Key != "other" {
		t.Fatalf("sent %+v, want only the new raise", got)
	}
	if err = b.layer.handle(bus.Message{Seq: 1, Topic: "notification/disk", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if again := receiveSent(t, b.sent); again.event.ID != published.ID || again.payload != string(payload) {
		t.Fatalf("sent again %+v", again)
	}
	select {
	case s := <-b.sent:
		t.Fatalf("sent %+v", s)
	case <-time.After(200 * time.Millisecond):
	}
}

// A raise the channels could not be told of is logged, not retried: the notifications after it are still recorded
// and handed on.
func TestDeliveryFailureHoldsNothingUp(t *testing.T) {
	b := newBench(t)
	b.layer.Close()
	fails := 1
	var err error
	b.layer, err = Open(filepath.Join(b.dir, "notifications.db"), b.events, slog.New(slog.DiscardHandler), func(e bus.Event, r Raise, payload []byte) error {
		if fails > 0 {
			fails--
			return errors.New("bus closed")
		}
		b.sent <- sent{e, r, string(payload)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b.publish("disk", full)
	next := Raise{Key: "temp", Severity: Critical, Subject: "The CPU is hot"}
	b.publish("sensors", next)
	if got := receiveSent(t, b.sent); got.raise != next {
		t.Fatalf("sent %+v", got)
	}
	if got := b.active(); len(got) != 2 {
		t.Fatalf("active %+v", got)
	}
}

func TestGapIsDelivered(t *testing.T) {
	b := newBench(t)
	b.close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(b.dir, "bus.db"+suffix)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	b.open(true)
	got := receiveSent(t, b.sent)
	var e bus.Event
	if got.event.Source != "notifications" || got.raise.Key != "missed" || json.Unmarshal([]byte(got.payload), &e) != nil || e.ID != got.event.ID || e.Type != RaisedType {
		t.Fatalf("sent %+v", got)
	}
}

func receiveSent(t *testing.T, ch <-chan sent) sent {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("nothing sent")
	}
	return sent{}
}

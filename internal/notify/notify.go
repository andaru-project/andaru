// Package notify keeps the notifications an administrator must hear about without looking for them. Notifications
// are durable events on the bus, on notification/<source>; the layer is their only reader. It keeps the active ones
// in its own store, ends one when its source clears it or someone dismisses it, and tells the web UI of each change
// with a non-durable notifications/changed event.
package notify

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
)

// Topics, event types and the consumer the layer reads.
const (
	Filter       = "notification/#"
	ChangedTopic = "notifications/changed"
	ChangedType  = "notifications.changed"
	RaisedType   = "notification.raised.v1"
	ClearedType  = "notification.cleared.v1"
	consumer     = "notifications"
)

// Severities, from the least to the most urgent.
const (
	Info     = "info"
	Warning  = "warning"
	Critical = "critical"
)

// The history keeps ended notifications for this long, and at most this many.
const (
	historyAge  = 30 * 24 * time.Hour
	historySize = 500
)

// detailMax is the longest detail, in bytes.
const detailMax = 4096

// Raise is what a source says about one of its notifications: data of a notification.raised.v1 event.
type Raise struct {
	Key      string `json:"key"`
	Severity string `json:"severity"`
	Subject  string `json:"subject"`
	Detail   string `json:"detail,omitempty"`
	Link     string `json:"link,omitempty"`
	Requires string `json:"requires,omitempty"`
}

// Notification is an active notification.
type Notification struct {
	ID     int64     `json:"id"`
	Source string    `json:"source"`
	Raised time.Time `json:"raised"`
	// Updated is when it was raised or last changed.
	Updated time.Time `json:"updated"`
	Raise
}

// Layer is the notifications' reader and store.
type Layer struct {
	db      *sql.DB
	events  *bus.Bus
	stop    func()
	log     *slog.Logger
	deliver Deliver
}

// Deliver hands a raise on to the channels that send it: e is its event, and payload that event as the bus carried
// it. It is called once the raise is recorded, and again for an event delivered again, so what it publishes must be
// known by e.ID. An error retries the event.
type Deliver func(e bus.Event, r Raise, payload []byte) error

// Open opens the store at path and starts reading notifications from events, handing each raise to deliver, if not
// nil. It attaches its consumer before it returns, so a list read after Open never misses a notification published
// after it.
func Open(path string, events *bus.Bus, log *slog.Logger, deliver Deliver) (*Layer, error) {
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS active (
			id INTEGER PRIMARY KEY AUTOINCREMENT, source TEXT NOT NULL, key TEXT NOT NULL, severity TEXT NOT NULL,
			subject TEXT NOT NULL, detail TEXT NOT NULL, link TEXT NOT NULL, requires TEXT NOT NULL,
			raised INTEGER NOT NULL, updated INTEGER NOT NULL, UNIQUE (source, key));
		CREATE TABLE IF NOT EXISTS history (
			id INTEGER PRIMARY KEY, source TEXT NOT NULL, key TEXT NOT NULL, severity TEXT NOT NULL,
			subject TEXT NOT NULL, raised INTEGER NOT NULL, ended INTEGER NOT NULL, dismissed_by TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS seen (
			source TEXT NOT NULL, event TEXT NOT NULL, time INTEGER NOT NULL, PRIMARY KEY (source, event)) WITHOUT ROWID;
		CREATE INDEX IF NOT EXISTS seen_time ON seen (time);
		CREATE INDEX IF NOT EXISTS history_ended ON history (ended)`); err == nil {
		_, err = db.Exec(expire, expiry())
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	l := &Layer{db: db, events: events, log: log, deliver: deliver}
	if l.stop, err = events.Consume(consumer, Filter, l.handle); err != nil {
		db.Close()
		return nil, err
	}
	return l, nil
}

// Close stops reading and closes the store.
func (l *Layer) Close() error {
	l.stop()
	return l.db.Close()
}

// Active lists the active notifications, the newest first.
func (l *Layer) Active() ([]Notification, error) {
	rows, err := l.db.Query(`SELECT id, source, key, severity, subject, detail, link, requires, raised, updated
		FROM active ORDER BY updated DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Notification{}
	for rows.Next() {
		var n Notification
		var raised, updated int64
		if err = rows.Scan(&n.ID, &n.Source, &n.Key, &n.Severity, &n.Subject, &n.Detail, &n.Link, &n.Requires, &raised, &updated); err != nil {
			return nil, err
		}
		n.Raised, n.Updated = time.UnixMilli(raised).UTC(), time.UnixMilli(updated).UTC()
		list = append(list, n)
	}
	return list, rows.Err()
}

// Times in the store are Unix milliseconds. What is kept of ended notifications and of events seen expires with
// age at each start and each event, besides the history's limit in size at each end.
const expire = `DELETE FROM history WHERE ended < ?1; DELETE FROM seen WHERE time < ?1`

func expiry() int64 { return time.Now().Add(-historyAge).UnixMilli() }

// Dismiss ends the active notification id on behalf of by, if sees says by may see what it requires. It comes back
// if its source raises it again. False when there is no such notification by may see.
func (l *Layer) Dismiss(id int64, by string, sees func(requires string) bool) (bool, error) {
	tx, err := l.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Read in the transaction that ends it: a raise in between may have changed what it requires.
	var requires string
	if err = tx.QueryRow(`SELECT requires FROM active WHERE id = ?`, id).Scan(&requires); errors.Is(err, sql.ErrNoRows) || err == nil && !sees(requires) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	ended, err := end(tx, `id = ?`, []any{id}, by)
	if err != nil || !ended {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	l.changed()
	return true, nil
}

// Publish raises or clears a notification of source: r with an empty Severity is a clear of r.Key. The event is
// durable, and in the bus's log when Publish returns.
func Publish(events *bus.Bus, source string, r Raise) error {
	topic, payload, err := Event(source, r)
	if err != nil {
		return err
	}
	return events.Publish(topic, payload, true)
}

// Event is the topic and payload that raise or clear a notification of source, as Publish sends them, for a process
// that publishes through a bus.Client.
func Event(source string, r Raise) (topic string, payload []byte, err error) {
	typ, data := RaisedType, any(r)
	if r.Severity == "" {
		typ, data = ClearedType, map[string]string{"key": r.Key}
	}
	payload, err = bus.NewEvent(typ, source, data)
	return "notification/" + source, payload, err
}

func (l *Layer) handle(m bus.Message) error {
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var change, send bool
	var r Raise
	e := bus.Event{Data: &r}
	payload := m.Payload
	if m.Gap {
		// Notifications aged out of the bus, or its store was replaced: some may never have reached the layer.
		l.log.Warn("notifications may have been missed")
		r = Raise{Key: "missed", Severity: Warning, Subject: "Notifications may have been missed",
			Detail: "Some notifications were lost before Andaru could record them. Check the engine journal for what happened."}
		// shortcut: a gap is not marked seen, so one whose delivery fails is not sent on its retry; mark it if it matters.
		e = bus.Event{ID: uuid.NewString(), Type: RaisedType, Time: time.Now().UTC(), Source: "notifications", Data: r}
		if change, err = raise(tx, e.Source, r, e.Time); err == nil && change {
			payload, err = json.Marshal(e)
			send = err == nil
		}
	} else {
		change, send, err = l.apply(tx, m, &e)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if change {
		l.changed()
	}
	// The channels hear of the raise once it is recorded, and again if it comes again: their jobs keep its ID. A
	// failure is logged, not retried: retrying the event would hold up every notification after it.
	if send && l.deliver != nil {
		if err := l.deliver(e, r, payload); err != nil {
			l.log.Error("notification not handed on to its channels", "source", e.Source, "key", r.Key, "error", err)
		}
	}
	return nil
}

// apply records the event in m, decoded into e, and says whether the list changed and whether the event is a raise
// the channels should hear of: one that changed the list, or one seen before. An event it cannot read is logged and
// skipped: retrying it would hold up every notification after it.
func (l *Layer) apply(tx *sql.Tx, m bus.Message, e *bus.Event) (change, send bool, err error) {
	r := e.Data.(*Raise)
	err = json.Unmarshal(m.Payload, e)
	if err == nil {
		err = validate(m.Topic, *e, *r)
	}
	if err != nil {
		l.log.Error("notification skipped", "topic", m.Topic, "seq", m.Seq, "error", err)
		return false, false, nil
	}
	raised := e.Type == RaisedType
	// An event seen before is one delivered again after a crash: it changes nothing twice.
	res, err := tx.Exec(`INSERT INTO seen (source, event, time) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, e.Source, e.ID, time.Now().UnixMilli())
	if err != nil {
		return false, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, raised, err
	}
	if _, err = tx.Exec(expire, expiry()); err != nil {
		return false, false, err
	}
	l.log.Info("notification", "type", e.Type, "source", e.Source, "key", r.Key, "severity", r.Severity, "subject", r.Subject)
	if !raised {
		change, err = end(tx, `source = ? AND key = ?`, []any{e.Source, r.Key}, "")
		return change, false, err
	}
	// An event from the future is dated now: a source does not keep its notification on top by its clock. One read
	// late, from a backlog, keeps the time it happened.
	at := time.Now()
	if e.Time.Before(at) {
		at = e.Time
	}
	change, err = raise(tx, e.Source, *r, at)
	return change, change, err
}

// text is valid UTF-8 of at most max bytes with no control character but the line breaks lines allows, and no
// invisible one that hides, reorders or breaks what is shown: format characters (bidi controls, zero-width spaces,
// tags) and the line and paragraph separators. The zero-width joiner and non-joiner stay: emoji and many scripts
// need them.
func text(s string, max int, lines bool) bool {
	return utf8.ValidString(s) && len(s) <= max && !strings.ContainsFunc(s, func(c rune) bool { return banned(c, lines) })
}

// banned reports whether text may not hold c: control characters (but a new line where lines may break), invisible
// format characters other than the zero-width joiner and non-joiner, and line and paragraph separators.
func banned(c rune, lines bool) bool {
	return unicode.IsControl(c) && !(lines && c == '\n') || unicode.Is(unicode.Cf, c) && c != '\u200c' && c != '\u200d' ||
		c == '\u2028' || c == '\u2029'
}

// Detail makes s, such as an error a tool wrote, a detail the layer accepts: what text may not hold becomes a space,
// and it is cut to the detail's limit.
func Detail(s string) string { return clean(s, detailMax, true) }

// Subject makes s a subject the layer accepts, as Detail does a detail: one line of at most 256 bytes.
func Subject(s string) string { return clean(s, 256, false) }

func clean(s string, max int, lines bool) string {
	s = strings.Map(func(c rune) rune {
		if banned(c, lines) {
			return ' '
		}
		return c
	}, strings.ToValidUTF8(s, "\uFFFD"))
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "") // drops a character the cut split
	}
	return s
}

// path reports whether link is a path in the web UI: one a browser cannot read as another site's (//host, /\host).
func path(link string) bool {
	u, err := url.Parse(link)
	return err == nil && u.Scheme == "" && u.Host == "" && strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "//") &&
		!strings.Contains(link, "\\") && text(link, 1024, false)
}

func validate(topic string, e bus.Event, r Raise) error {
	switch {
	case e.ID == "" || len(e.ID) > 64:
		return errors.New("invalid event ID")
	case e.Time.Before(time.Unix(0, 0)): // none, or one JSON cannot write back
		return errors.New("no time, or one before 1970")
	case topic != "notification/"+e.Source:
		return fmt.Errorf("source %q does not match the topic", e.Source)
	case r.Key == "" || !text(r.Key, 256, false):
		return errors.New("invalid key")
	case e.Type == ClearedType:
		return nil
	case e.Type != RaisedType:
		return fmt.Errorf("unknown type %q", e.Type)
	case r.Severity != Info && r.Severity != Warning && r.Severity != Critical:
		return fmt.Errorf("unknown severity %q", r.Severity)
	case r.Subject == "" || !text(r.Subject, 256, false):
		return errors.New("invalid subject")
	case !text(r.Detail, detailMax, true):
		return errors.New("invalid detail")
	case r.Link != "" && !path(r.Link):
		return errors.New("a link must be a path in the web UI")
	case r.Requires != "" && !slices.Contains(authz.Catalog(), authz.Capability(r.Requires)):
		return fmt.Errorf("unknown capability %q", r.Requires)
	}
	return nil
}

// raise adds or updates the notification; false when it is already active as raised.
func raise(tx *sql.Tx, source string, r Raise, at time.Time) (bool, error) {
	res, err := tx.Exec(`INSERT INTO active (source, key, severity, subject, detail, link, requires, raised, updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, key) DO UPDATE SET severity = excluded.severity, subject = excluded.subject,
			detail = excluded.detail, link = excluded.link, requires = excluded.requires, updated = max(updated, excluded.updated)
		WHERE (severity, subject, detail, link, requires) IS NOT (excluded.severity, excluded.subject, excluded.detail, excluded.link, excluded.requires)`,
		source, r.Key, r.Severity, r.Subject, r.Detail, r.Link, r.Requires, at.UnixMilli(), at.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// end moves the active notifications matching where to the history; false when none matched.
func end(tx *sql.Tx, where string, args []any, dismissedBy string) (bool, error) {
	now := time.Now()
	res, err := tx.Exec(`INSERT INTO history (source, key, severity, subject, raised, ended, dismissed_by)
		SELECT source, key, severity, subject, raised, ?, ? FROM active WHERE `+where, append([]any{now.UnixMilli(), dismissedBy}, args...)...)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if _, err = tx.Exec(`DELETE FROM active WHERE `+where, args...); err != nil {
		return false, err
	}
	_, err = tx.Exec(`DELETE FROM history WHERE id <= (SELECT max(id) FROM history) - ?`, historySize)
	return err == nil, err
}

// changed tells subscribers, the web UI's event streams, that the list changed. It is not durable: a stream reads
// the list again whenever it opens.
func (l *Layer) changed() {
	payload, err := bus.NewEvent(ChangedType, "engine", struct{}{})
	if err == nil {
		err = l.events.Publish(ChangedTopic, payload, false)
	}
	if err != nil {
		l.log.Warn("publish notifications changed", "error", err)
	}
}

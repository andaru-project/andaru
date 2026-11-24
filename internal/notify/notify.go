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
	db     *sql.DB
	events *bus.Bus
	stop   func()
	log    *slog.Logger
}

// Open opens the store at path and starts reading notifications from events. It attaches its consumer before it
// returns, so a list read after Open never misses a notification published after it.
func Open(path string, events *bus.Bus, log *slog.Logger) (*Layer, error) {
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
			source TEXT NOT NULL, event TEXT NOT NULL, time INTEGER NOT NULL, PRIMARY KEY (source, event)) WITHOUT ROWID`); err != nil {
		db.Close()
		return nil, err
	}
	l := &Layer{db: db, events: events, log: log}
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
		n.Raised, n.Updated = time.Unix(raised, 0).UTC(), time.Unix(updated, 0).UTC()
		list = append(list, n)
	}
	return list, rows.Err()
}

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
	typ, data := RaisedType, any(r)
	if r.Severity == "" {
		typ, data = ClearedType, map[string]string{"key": r.Key}
	}
	payload, err := bus.NewEvent(typ, source, data)
	if err != nil {
		return err
	}
	return events.Publish("notification/"+source, payload, true)
}

func (l *Layer) handle(m bus.Message) error {
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var change bool
	if m.Gap {
		// Notifications aged out of the bus, or its store was replaced: some may never have reached the layer.
		l.log.Warn("notifications may have been missed")
		change, err = raise(tx, "notifications", Raise{Key: "missed", Severity: Warning,
			Subject: "Notifications may have been missed",
			Detail:  "Some notifications were lost before Andaru could record them. Check the engine journal for what happened."}, time.Now())
	} else {
		change, err = l.apply(tx, m)
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
	return nil
}

// apply records the event in m. An event it cannot read is logged and skipped: retrying it would hold up every
// notification after it.
func (l *Layer) apply(tx *sql.Tx, m bus.Message) (bool, error) {
	var r Raise
	e := bus.Event{Data: &r}
	err := json.Unmarshal(m.Payload, &e)
	if err == nil {
		err = validate(m.Topic, e, r)
	}
	if err != nil {
		l.log.Error("notification skipped", "topic", m.Topic, "seq", m.Seq, "error", err)
		return false, nil
	}
	// An event seen before is one delivered again after a crash: it changes nothing twice.
	res, err := tx.Exec(`INSERT INTO seen (source, event, time) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, e.Source, e.ID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if _, err = tx.Exec(`DELETE FROM seen WHERE time < ?`, time.Now().Add(-historyAge).Unix()); err != nil {
		return false, err
	}
	l.log.Info("notification", "type", e.Type, "source", e.Source, "key", r.Key, "severity", r.Severity, "subject", r.Subject)
	if e.Type == ClearedType {
		return end(tx, `source = ? AND key = ?`, []any{e.Source, r.Key}, "")
	}
	// An event from the future is dated now: a source does not keep its notification on top by its clock. One read
	// late, from a backlog, keeps the time it happened.
	at := time.Now()
	if e.Time.Before(at) {
		at = e.Time
	}
	return raise(tx, e.Source, r, at)
}

// text is valid UTF-8 of at most max bytes with no control character but the line breaks lines allows, and none
// that reorders or breaks what is shown: the bidi controls, the line and paragraph separators, a byte order mark.
// Joiners stay: emoji and many scripts need them.
func text(s string, max int, lines bool) bool {
	return utf8.ValidString(s) && len(s) <= max && !strings.ContainsFunc(s, func(c rune) bool {
		return unicode.IsControl(c) && !(lines && c == '\n') || unicode.Is(unicode.Bidi_Control, c) ||
			c == '\u2028' || c == '\u2029' || c == '\ufeff'
	})
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
	case e.Time.IsZero():
		return errors.New("no time")
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
	case !text(r.Detail, 4096, true):
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
			detail = excluded.detail, link = excluded.link, requires = excluded.requires, updated = excluded.updated
		WHERE (severity, subject, detail, link, requires) IS NOT (excluded.severity, excluded.subject, excluded.detail, excluded.link, excluded.requires)`,
		source, r.Key, r.Severity, r.Subject, r.Detail, r.Link, r.Requires, at.Unix(), at.Unix())
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
		SELECT source, key, severity, subject, raised, ?, ? FROM active WHERE `+where, append([]any{now.Unix(), dismissedBy}, args...)...)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if _, err = tx.Exec(`DELETE FROM active WHERE `+where, args...); err != nil {
		return false, err
	}
	_, err = tx.Exec(`DELETE FROM history WHERE ended < ? OR id <= (SELECT max(id) FROM history) - ?`, now.Add(-historyAge).Unix(), historySize)
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

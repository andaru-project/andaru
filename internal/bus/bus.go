// Package bus is Andaru's event bus: a log of events kept in SQLite, with live subscribers in memory and durable
// consumers that read the log from a cursor.
//
// A durable event is written to the log before Publish returns. Each durable consumer reads the log after its
// cursor and moves the cursor when it has handled an event, so it gets every durable event at least once, across
// restarts of the bus and of the consumer. A non-durable event is only handed to the live subscribers, and is
// lost if a process dies. A consumer that falls behind the log's retention is told it missed events (a gap) and
// carries on from the oldest event kept.
package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Grants are the topic filters a client may publish to and subscribe to.
type Grants struct {
	Publish   []string
	Subscribe []string
}

type Options struct {
	// Path is the SQLite file holding the log and the consumers' cursors.
	Path string
	// Grants by user name. A client whose user name has no grants cannot connect.
	Grants map[string]Grants
	// Authenticate checks a client's user name and token on Listeners. Nil refuses every client.
	Authenticate func(user, token string) bool
	// Listeners by ID, for clients outside the engine.
	Listeners map[string]net.Listener
	// Retention is how long the log keeps an event, and how long a consumer nobody attaches is kept. Zero means
	// seven days.
	Retention time.Duration
	// LostHistory says the store at Path replaces one that was lost: every consumer that comes back within ten
	// times Retention is told of a gap.
	LostHistory bool
	Logger      *slog.Logger
}

// Message is an event as a subscriber or consumer gets it. Seq orders durable events; it is 0 for an event a
// live subscriber got that was not durable. Gap is a message of its own: events this consumer should have had
// were pruned before it read them.
type Message struct {
	Seq     int64
	Topic   string
	Payload json.RawMessage
	Gap     bool
}

// maxPayload and maxTopic bound an event, so that every frame that carries one fits a line of the protocol: frames
// are encoded without escaping, so a payload takes its own size in a frame.
const (
	maxPayload = 512 << 10
	maxTopic   = 1 << 10
)

// ErrStore wraps a failure to open the store at Options.Path: a damaged file, or a disk that fails while it is
// opened.
var ErrStore = errors.New("bus store unusable")

type Bus struct {
	db  *sql.DB
	opt Options
	log *slog.Logger

	attachMu  sync.Mutex // one attach at a time, from its transaction to its registration
	mu        sync.Mutex
	subs      map[int]*subscriber
	nextSub   int
	attached  map[string]*attachment // durable consumers being read, by name
	owners    atomic.Int64           // numbers attachments, from the time the bus opened so as not to repeat after a restart
	wake      chan struct{}          // closed and replaced at each durable event
	listeners []net.Listener
	conns     map[*conn]struct{}

	closed chan struct{}
	done   sync.WaitGroup
}

type subscriber struct {
	filter  string
	deliver func(Message) // must not block
}

// Open opens the store, prunes it and starts serving Listeners.
func Open(o Options) (*Bus, error) {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Retention == 0 {
		o.Retention = 7 * 24 * time.Hour
	}
	for user, g := range o.Grants {
		if user == "" || strings.ContainsAny(user, "/\x00") {
			return nil, fmt.Errorf("bus: user name %q is empty or holds a slash", user)
		}
		for _, filter := range append(slices.Clone(g.Publish), g.Subscribe...) {
			if !validFilter(filter) {
				return nil, fmt.Errorf("bus: %s's grant %q is not a filter", user, filter)
			}
		}
	}
	db, err := openStore(o.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	b := &Bus{db: db, opt: o, log: o.Logger, subs: map[int]*subscriber{}, attached: map[string]*attachment{},
		wake: make(chan struct{}), conns: map[*conn]struct{}{}, closed: make(chan struct{})}
	b.owners.Store(time.Now().UnixNano())
	if o.LostHistory {
		if _, err = db.Exec(`INSERT INTO meta (key, value) VALUES ('lost_until', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			time.Now().Add(10*o.Retention).Unix()); err != nil {
			db.Close()
			return nil, fmt.Errorf("%w: %w", ErrStore, err)
		}
	}
	// A store that opened is kept: a prune that fails now is tried again within the hour.
	if err = b.prune(time.Now()); err != nil {
		b.log.Error("bus prune failed", "error", err)
	}
	b.done.Add(1)
	go b.pruneEvery(time.Hour)
	for _, l := range o.Listeners {
		b.listeners = append(b.listeners, l)
		b.done.Add(1)
		go b.serve(l)
	}
	return b, nil
}

func openStore(path string) (*sql.DB, error) {
	// The path is escaped, so a ? or # in it is part of the file's name and not of the URI's options.
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			seq INTEGER PRIMARY KEY AUTOINCREMENT, topic TEXT NOT NULL, payload BLOB NOT NULL, time INTEGER NOT NULL);
		CREATE INDEX IF NOT EXISTS events_time ON events (time);
		CREATE TABLE IF NOT EXISTS consumers (
			name TEXT PRIMARY KEY, filter TEXT NOT NULL, cursor INTEGER NOT NULL, gap INTEGER NOT NULL DEFAULT 0,
			seen INTEGER NOT NULL, owner INTEGER NOT NULL DEFAULT 0) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS expired (name TEXT PRIMARY KEY, time INTEGER NOT NULL) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL) WITHOUT ROWID`); err != nil {
		db.Close()
		return nil, err
	}
	for _, column := range []string{"gap", "owner"} {
		var has bool
		if err = db.QueryRow(`SELECT count(*) > 0 FROM pragma_table_info('consumers') WHERE name = ?`, column).Scan(&has); err == nil && !has {
			_, err = db.Exec(`ALTER TABLE consumers ADD COLUMN ` + column + ` INTEGER NOT NULL DEFAULT 0`)
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Close stops the listeners and the clients' connections, waits for them, and closes the store.
func (b *Bus) Close() error {
	b.mu.Lock()
	select {
	case <-b.closed:
		b.mu.Unlock()
		return nil
	default:
	}
	close(b.closed)
	for _, l := range b.listeners {
		_ = l.Close()
	}
	conns := make([]*conn, 0, len(b.conns))
	for c := range b.conns {
		conns = append(conns, c)
	}
	b.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
	b.done.Wait()
	return b.db.Close()
}

// Publish sends an event from the engine. A durable event is in the log when Publish returns; the payload must be
// JSON.
func (b *Bus) Publish(topic string, payload []byte, durable bool) error {
	if !validTopic(topic) || len(topic) > maxTopic {
		return fmt.Errorf("bus: invalid topic %q", topic)
	}
	if len(payload) > maxPayload || !json.Valid(payload) {
		return errors.New("bus: the payload is not JSON, or larger than 512 KiB")
	}
	m := Message{Topic: topic, Payload: payload}
	if durable {
		res, err := b.db.Exec(`INSERT INTO events (topic, payload, time) VALUES (?, ?, ?)`, topic, payload, time.Now().Unix())
		if err != nil {
			return err
		}
		if m.Seq, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	b.mu.Lock()
	targets := make([]*subscriber, 0, len(b.subs))
	for _, s := range b.subs {
		if match(s.filter, topic) {
			targets = append(targets, s)
		}
	}
	if durable {
		close(b.wake)
		b.wake = make(chan struct{})
	}
	b.mu.Unlock()
	for _, s := range targets {
		s.deliver(m)
	}
	return nil
}

// Subscribe calls handler for every event on filter published from now on, durable or not, in the publisher's
// goroutine: it must not block. The returned function ends the subscription.
func (b *Bus) Subscribe(filter string, handler func(topic string, payload []byte)) (func(), error) {
	return b.subscribe(filter, func(m Message) { handler(m.Topic, m.Payload) })
}

func (b *Bus) subscribe(filter string, deliver func(Message)) (func(), error) {
	if !validFilter(filter) {
		return nil, fmt.Errorf("bus: invalid filter %q", filter)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextSub++
	id := b.nextSub
	b.subs[id] = &subscriber{filter: filter, deliver: deliver}
	return func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}, nil
}

// waitFor returns a channel that is closed at the next durable event.
func (b *Bus) waitFor() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wake
}

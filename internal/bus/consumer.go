package bus

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// A durable consumer has a name, a filter and a cursor: the sequence number of the last durable event it is done
// with. It reads the log after its cursor. A new consumer starts at the end of the log: it reads the state it
// follows when it starts, and needs only what changes after.

// attachment is a consumer being read, in the engine or by a client. One name is read by one attachment at a time;
// a new one takes the name over.
type attachment struct {
	name   string
	stop   chan struct{}
	closed bool
}

const batch = 100

// attach loads or creates the consumer name with filter, and reports its cursor and whether events it should
// have had were pruned. It takes the name over from any earlier attachment.
func (b *Bus) attach(name, filter string) (*attachment, int64, bool, error) {
	if !validFilter(filter) {
		return nil, 0, false, fmt.Errorf("bus: invalid filter %q", filter)
	}
	now := time.Now().Unix()
	var cursor, pruned int64
	expired := false
	err := b.db.QueryRow(`SELECT cursor FROM consumers WHERE name = ?`, name).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		// A consumer that expired comes back to a gap, and reads what is kept; a new one starts at the end.
		var res sql.Result
		if res, err = b.db.Exec(`DELETE FROM expired WHERE name = ?`, name); err == nil {
			var n int64
			if n, err = res.RowsAffected(); err == nil && n > 0 {
				expired = true
				err = b.db.QueryRow(`SELECT coalesce((SELECT min(seq) FROM events), (SELECT coalesce(max(seq), 0) FROM sqlite_sequence WHERE name = 'events') + 1) - 1`).Scan(&cursor)
			} else if err == nil {
				cursor, err = b.tail()
			}
		}
		if err == nil {
			_, err = b.db.Exec(`INSERT INTO consumers (name, filter, cursor, seen) VALUES (?, ?, ?, ?)`, name, filter, cursor, now)
		}
	} else if err == nil {
		_, err = b.db.Exec(`UPDATE consumers SET filter = ?, seen = ? WHERE name = ?`, filter, now, name)
	}
	if err == nil {
		err = b.db.QueryRow(`SELECT coalesce((SELECT value FROM meta WHERE key = 'pruned'), 0)`).Scan(&pruned)
	}
	if err != nil {
		return nil, 0, false, err
	}
	gap := expired || cursor < pruned
	if gap {
		cursor = pruned
		if err = b.setCursor(name, cursor); err != nil {
			return nil, 0, false, err
		}
	}
	a := &attachment{name: name, stop: make(chan struct{})}
	b.mu.Lock()
	if old := b.attached[name]; old != nil {
		old.detach()
	}
	b.attached[name] = a
	b.mu.Unlock()
	return a, cursor, gap, nil
}

// detach ends the attachment; b.mu is held.
func (a *attachment) detach() {
	if !a.closed {
		a.closed = true
		close(a.stop)
	}
}

func (b *Bus) release(a *attachment) {
	b.mu.Lock()
	a.detach()
	if b.attached[a.name] == a {
		delete(b.attached, a.name)
	}
	b.mu.Unlock()
}

// tail is the sequence number of the newest durable event ever written.
func (b *Bus) tail() (int64, error) {
	var seq int64
	err := b.db.QueryRow(`SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'events'), 0)`).Scan(&seq)
	return seq, err
}

func (b *Bus) setCursor(name string, cursor int64) error {
	_, err := b.db.Exec(`UPDATE consumers SET cursor = ?, seen = ? WHERE name = ? AND cursor < ?`, cursor, time.Now().Unix(), name, cursor)
	return err
}

// read returns the durable events after seq, oldest first.
func (b *Bus) read(after int64) ([]Message, error) {
	rows, err := b.db.Query(`SELECT seq, topic, payload FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, after, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Seq, &m.Topic, &m.Payload); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Consume reads the durable consumer name in the engine: handler gets each durable event on filter after the
// consumer's cursor, in order, and a Gap message first if events it should have had were pruned. An event is done
// with once handler returns nil; an error retries it after a pause. The returned function stops reading and
// returns once handler will not be called again; it must not be called from handler. The bus closes only once
// handler has returned.
func (b *Bus) Consume(name, filter string, handler func(Message) error) (func(), error) {
	a, cursor, gap, err := b.attach("engine/"+name, filter)
	if err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	b.done.Add(1)
	go func() {
		defer b.done.Done()
		defer close(exited)
		defer b.release(a)
		if gap && !b.retry(a, func() error { return handler(Message{Gap: true}) }) {
			return
		}
		for {
			wake := b.waitFor()
			msgs, err := b.read(cursor)
			if err != nil {
				b.log.Error("bus consumer read failed", "consumer", a.name, "error", err)
				msgs = nil
			}
			for _, m := range msgs {
				select {
				case <-a.stop:
					return
				default:
				}
				if match(filter, m.Topic) && !b.retry(a, func() error { return handler(m) }) {
					return
				}
				cursor = m.Seq
				if err := b.setCursor(a.name, cursor); err != nil {
					b.log.Error("bus consumer cursor not saved", "consumer", a.name, "error", err)
				}
			}
			if len(msgs) == batch {
				continue
			}
			select {
			case <-wake:
			case <-a.stop:
				return
			case <-b.closed:
				return
			}
		}
	}()
	return func() {
		b.release(a)
		<-exited
	}, nil
}

// retry calls f until it returns nil, pausing between tries; false when the attachment ends first.
func (b *Bus) retry(a *attachment, f func() error) bool {
	for pause := 100 * time.Millisecond; ; pause = min(2*pause, 30*time.Second) {
		err := f()
		if err == nil {
			return true
		}
		b.log.Warn("bus consumer will retry", "consumer", a.name, "error", err)
		select {
		case <-time.After(pause):
		case <-a.stop:
			return false
		case <-b.closed:
			return false
		}
	}
}

// prune removes the consumers nobody attached for Retention, the events every consumer is done with, and the
// events older than Retention. Pruning by age records how far it went, so a consumer behind it learns of the gap.
func (b *Bus) prune(now time.Time) error {
	horizon := now.Add(-b.opt.Retention).Unix()
	b.mu.Lock()
	for name := range b.attached {
		_, _ = b.db.Exec(`UPDATE consumers SET seen = ? WHERE name = ?`, now.Unix(), name)
	}
	b.mu.Unlock()
	// An expired consumer leaves its name, so that coming back it is told of the gap rather than started anew.
	if _, err := b.db.Exec(`INSERT OR REPLACE INTO expired (name, time) SELECT name, ? FROM consumers WHERE seen < ?`, now.Unix(), horizon); err != nil {
		return err
	}
	if _, err := b.db.Exec(`DELETE FROM consumers WHERE seen < ?`, horizon); err != nil {
		return err
	}
	if _, err := b.db.Exec(`DELETE FROM expired WHERE time < ?`, now.Add(-10*b.opt.Retention).Unix()); err != nil {
		return err
	}
	if _, err := b.db.Exec(`DELETE FROM events WHERE seq <= (SELECT coalesce(min(cursor), (SELECT coalesce(max(seq), 0) FROM events)) FROM consumers)`); err != nil {
		return err
	}
	var aged sql.NullInt64
	if err := b.db.QueryRow(`SELECT max(seq) FROM events WHERE time < ?`, horizon).Scan(&aged); err != nil || !aged.Valid {
		return err
	}
	if _, err := b.db.Exec(`DELETE FROM events WHERE seq <= ?`, aged.Int64); err != nil {
		return err
	}
	_, err := b.db.Exec(`INSERT INTO meta (key, value) VALUES ('pruned', ?) ON CONFLICT (key) DO UPDATE SET value = max(value, excluded.value)`, aged.Int64)
	return err
}

func (b *Bus) pruneEvery(every time.Duration) {
	defer b.done.Done()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			if err := b.prune(now); err != nil {
				b.log.Error("bus prune failed", "error", err)
			}
		case <-b.closed:
			return
		}
	}
}

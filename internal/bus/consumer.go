package bus

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// A durable consumer has a name, a filter, a cursor (the sequence number of the last durable event it is done
// with) and maybe a pending gap. It reads the log after its cursor. A new consumer starts at the end of the log: it
// reads the state it follows when it starts, and needs only what changes after.
//
// A gap is pending when events the consumer should have had are gone: they aged out of the log, the consumer was
// removed for being away, or the store was replaced. The gap is stored with the consumer and delivered before
// anything else, and stays pending until the consumer is done with it, as an event would.

// attachment is a consumer being read, in the engine or by a client. One name is read by one attachment at a time;
// a new one takes the name over.
type attachment struct {
	name   string
	owner  int64 // stored with the consumer: only this attachment writes its cursor and gap
	stop   chan struct{}
	closed bool
}

const batch = 100

// readRetry is how long a consumer waits after the log could not be read.
const readRetry = time.Second

// attach loads or creates the consumer name with filter, and returns its cursor and whether a gap is pending. It
// takes the name over from any earlier attachment.
func (b *Bus) attach(name, filter string) (*attachment, int64, bool, error) {
	if !validFilter(filter) {
		return nil, 0, false, fmt.Errorf("bus: invalid filter %q", filter)
	}
	now := time.Now().Unix()
	owner := b.owners.Add(1)
	var cursor int64
	var gap bool
	// One transaction: the store has one connection, so a prune cannot run between the reads and the writes.
	tx, err := b.db.Begin()
	if err != nil {
		return nil, 0, false, err
	}
	defer tx.Rollback()
	var stored string
	err = tx.QueryRow(`SELECT cursor, gap, filter FROM consumers WHERE name = ?`, name).Scan(&cursor, &gap, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		// A consumer that expired, or that the store lost when it was replaced, comes back to a gap; a new one
		// starts at the end of the log.
		var res sql.Result
		if res, err = tx.Exec(`DELETE FROM expired WHERE name = ?`, name); err == nil {
			var n, lostUntil int64
			if n, err = res.RowsAffected(); err == nil {
				err = tx.QueryRow(`SELECT coalesce((SELECT value FROM meta WHERE key = 'lost_until'), 0)`).Scan(&lostUntil)
			}
			if err == nil {
				if gap = n > 0 || now < lostUntil; !gap {
					err = tx.QueryRow(`SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'events'), 0)`).Scan(&cursor)
				}
			}
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO consumers (name, filter, cursor, gap, seen, owner) VALUES (?, ?, ?, ?, ?, ?)`, name, filter, cursor, gap, now, owner)
		}
	} else if err == nil {
		// Events the old filter skipped may match the new one: they are not coming back.
		gap = gap || stored != filter
		_, err = tx.Exec(`UPDATE consumers SET filter = ?, gap = ?, seen = ?, owner = ? WHERE name = ?`, filter, gap, now, owner, name)
	}
	var pruned int64
	if err == nil {
		err = tx.QueryRow(`SELECT coalesce((SELECT value FROM meta WHERE key = 'pruned'), 0)`).Scan(&pruned)
	}
	if err == nil && cursor < pruned {
		cursor, gap = pruned, true
		_, err = tx.Exec(`UPDATE consumers SET gap = 1, cursor = ? WHERE name = ?`, cursor, name)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, 0, false, err
	}
	a := &attachment{name: name, owner: owner, stop: make(chan struct{})}
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

// pruned is the sequence number up to which events were removed by age: a consumer behind it has a gap.
func (b *Bus) pruned() (int64, error) {
	var seq int64
	err := b.db.QueryRow(`SELECT coalesce((SELECT value FROM meta WHERE key = 'pruned'), 0)`).Scan(&seq)
	return seq, err
}

// The consumer's row is written only by the attachment that owns it: one taken over may still be finishing a
// pass, and must not move the new reader's cursor or touch its gap.

func (b *Bus) saveCursor(a *attachment, cursor int64) {
	if _, err := b.db.Exec(`UPDATE consumers SET cursor = ?, seen = ? WHERE name = ? AND owner = ? AND cursor < ?`,
		cursor, time.Now().Unix(), a.name, a.owner, cursor); err != nil {
		b.log.Error("bus consumer cursor not saved", "consumer", a.name, "error", err)
	}
}

// markGap stores a pending gap, with the cursor moved past the events that are gone.
func (b *Bus) markGap(a *attachment, to int64) {
	if _, err := b.db.Exec(`UPDATE consumers SET gap = 1, cursor = max(cursor, ?), seen = ? WHERE name = ? AND owner = ?`,
		to, time.Now().Unix(), a.name, a.owner); err != nil {
		b.log.Error("bus consumer gap not stored", "consumer", a.name, "error", err)
	}
}

func (b *Bus) clearGap(a *attachment) {
	if _, err := b.db.Exec(`UPDATE consumers SET gap = 0, seen = ? WHERE name = ? AND owner = ?`, time.Now().Unix(), a.name, a.owner); err != nil {
		b.log.Error("bus consumer gap not cleared", "consumer", a.name, "error", err)
	}
}

// read returns the durable events after seq, oldest first, or the point the log was pruned to when events after
// seq are gone.
func (b *Bus) read(after int64) ([]Message, int64, error) {
	pruned, err := b.pruned()
	if err != nil {
		return nil, 0, err
	}
	if after < pruned {
		return nil, pruned, nil
	}
	rows, err := b.db.Query(`SELECT seq, topic, payload FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, after, batch)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Seq, &m.Topic, &m.Payload); err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, 0, nil
}

// Consume reads the durable consumer name in the engine: handler gets a Gap message first if one is pending, then
// each durable event on filter after the consumer's cursor, in order. A message is done with once handler returns
// nil; an error retries it after a pause. The returned function stops reading and returns once handler will not
// be called again; it must not be called from handler. The bus closes only once handler has returned.
func (b *Bus) Consume(name, filter string, handler func(Message) error) (func(), error) {
	if !b.track() {
		return nil, errors.New("bus: closed")
	}
	a, cursor, gap, err := b.attach(engineConsumer(name), filter)
	if err != nil {
		b.done.Done()
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		defer b.done.Done()
		defer close(exited)
		defer b.release(a)
		for {
			if gap {
				if !b.retry(a, func() error { return handler(Message{Gap: true, Seq: cursor}) }) {
					return
				}
				b.clearGap(a)
				gap = false
			}
			wake := b.waitFor()
			msgs, gapTo, err := b.read(cursor)
			var pause <-chan time.Time
			switch {
			case err != nil:
				b.log.Error("bus consumer read failed", "consumer", a.name, "error", err)
				pause = time.After(readRetry)
			case gapTo > 0:
				b.markGap(a, gapTo)
				cursor, gap = gapTo, true
				continue
			}
			for _, m := range msgs {
				select {
				case <-a.stop:
					return
				default:
				}
				if match(filter, m.Topic) {
					if !b.retry(a, func() error { return handler(m) }) {
						return
					}
					b.saveCursor(a, m.Seq)
				}
				cursor = m.Seq
			}
			if len(msgs) > 0 {
				b.saveCursor(a, cursor) // past the events that were not this consumer's
			}
			if len(msgs) == batch {
				continue
			}
			select {
			case <-wake:
			case <-pause:
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

// engineConsumer and clientConsumer name consumers so that the engine's and each user's never meet: user names
// hold no slash.
func engineConsumer(name string) string       { return "engine/" + name }
func clientConsumer(user, name string) string { return "client/" + user + "/" + name }

// track counts a goroutine Close waits for, unless the bus is closing.
func (b *Bus) track() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.closed:
		return false
	default:
		b.done.Add(1)
		return true
	}
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
	attached := make([]string, 0, len(b.attached))
	for name := range b.attached {
		attached = append(attached, name)
	}
	b.mu.Unlock()
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, name := range attached {
		if _, err := tx.Exec(`UPDATE consumers SET seen = ? WHERE name = ?`, now.Unix(), name); err != nil {
			return err
		}
	}
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		// An expired consumer leaves its name, so that coming back it is told of the gap rather than started anew.
		{`INSERT OR REPLACE INTO expired (name, time) SELECT name, ? FROM consumers WHERE seen < ?`, []any{now.Unix(), horizon}},
		{`DELETE FROM consumers WHERE seen < ?`, []any{horizon}},
		{`DELETE FROM expired WHERE time < ?`, []any{now.Add(-10 * b.opt.Retention).Unix()}},
		{`DELETE FROM events WHERE seq <= (SELECT coalesce(min(cursor), (SELECT coalesce(max(seq), 0) FROM events)) FROM consumers)`, nil},
	} {
		if _, err := tx.Exec(stmt.sql, stmt.args...); err != nil {
			return err
		}
	}
	var aged sql.NullInt64
	if err := tx.QueryRow(`SELECT max(seq) FROM events WHERE time < ?`, horizon).Scan(&aged); err != nil {
		return err
	}
	if aged.Valid {
		if _, err := tx.Exec(`DELETE FROM events WHERE seq <= ?`, aged.Int64); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('pruned', ?) ON CONFLICT (key) DO UPDATE SET value = max(value, excluded.value)`, aged.Int64); err != nil {
			return err
		}
	}
	return tx.Commit()
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

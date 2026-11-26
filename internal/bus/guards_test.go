package bus

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rawClient says hello and subscribes to config/#, then reads nothing: a client that stopped reading.
func rawClient(t *testing.T, h *harness) net.Conn {
	t.Helper()
	nc, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	_ = nc.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(nc)
	for _, line := range []string{`{"op":"hello","id":1,"user":"web","token":"secret"}`, `{"op":"subscribe","id":2,"filter":"config/#"}`} {
		if _, err = nc.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		if reply, err := r.ReadString('\n'); err != nil || !strings.Contains(reply, `"op":"ok"`) {
			t.Fatalf("%s: %q %v", line, reply, err)
		}
	}
	return nc
}

// payload is a JSON string of n bytes, so a few frames fill the socket's buffer.
func payload(n int) string { return `"` + strings.Repeat("x", n-2) + `"` }

// disconnected reports whether the bus closed nc: what was sent drains, then the connection ends.
func disconnected(t *testing.T, nc net.Conn) bool {
	t.Helper()
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.Copy(io.Discard, nc)
	return !errors.Is(err, os.ErrDeadlineExceeded) // EOF or a reset; a timeout means it is still open
}

// gone waits until the bus has let go of every client connection.
func gone(t *testing.T, h *harness) {
	t.Helper()
	h.eventually(func() bool {
		h.bus.mu.Lock()
		defer h.bus.mu.Unlock()
		return len(h.bus.conns) == 0
	})
}

// shortWrites lowers writeTimeout until the test and its cleanups, the bus's Close included, are done.
func shortWrites(t *testing.T) {
	d := writeTimeout
	t.Cleanup(func() { writeTimeout = d }) // registered first, so it runs last
	writeTimeout = 200 * time.Millisecond
}

// A client too far behind is disconnected, and the publisher is never held up by it.
func TestSlowClientIsDisconnected(t *testing.T) {
	h := newHarness(t, Options{})
	nc := rawClient(t, h)
	// The socket's buffer takes a few frames and the writer blocks on the next: then the queue fills. Held up, a
	// publish would wait for the writer, which waits writeTimeout.
	for range queued + 64 {
		start := time.Now()
		h.publish("config/head", payload(64<<10), false)
		if took := time.Since(start); took > writeTimeout/4 {
			t.Fatalf("a publish took %v: the slow client held up the publisher", took)
		}
	}
	if !disconnected(t, nc) {
		t.Fatal("the slow client is still connected")
	}
}

// A client that stops reading loses its connection once a write has waited writeTimeout, even with its queue short.
func TestStalledWriteTimesOut(t *testing.T) {
	shortWrites(t)
	h := newHarness(t, Options{})
	nc := rawClient(t, h)
	for range 16 { // far fewer than queued: only the write's deadline can end this connection
		h.publish("config/head", payload(256<<10), false)
	}
	gone(t, h) // before reading, which would let the stalled write finish
	if !disconnected(t, nc) {
		t.Fatal("a stalled write never timed out")
	}
}

// A client whose write to a bus that stopped reading cannot finish gives up after writeTimeout.
func TestClientWriteTimesOut(t *testing.T) {
	shortWrites(t)
	dir, err := os.MkdirTemp("", "bus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	l, err := net.Listen("unix", filepath.Join(dir, "bus.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // a bus that answers hello, then reads no more
		nc, err := l.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		if _, err = bufio.NewReader(nc).ReadString('\n'); err == nil {
			_, _ = nc.Write([]byte(`{"op":"ok","id":1}` + "\n"))
		}
		<-stop
	}()
	c, err := Dial(ctx(t), filepath.Join(dir, "bus.sock"), "web", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan error, 1)
	go func() {
		var err error
		// Frames with no reply to wait for, until the socket's buffer is full and a write waits out its deadline.
		for err == nil {
			err = c.write(context.Background(), frame{Op: "publish", Topic: "config/head", Payload: []byte(payload(256 << 10))})
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("the write failed with %v, want its deadline", err)
		}
	case <-time.After(5 * time.Second):
		c.Close()
		<-done // the writer reads writeTimeout: it must be gone before the cleanup restores it
		t.Fatal("a write to a bus that stopped reading never gave up")
	}
}

// hideMeta makes the consumers' reads of the log fail until the returned function puts the table back.
func hideMeta(t *testing.T, h *harness) func() {
	t.Helper()
	if _, err := h.bus.db.Exec(`ALTER TABLE meta RENAME TO meta_away`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := h.bus.db.Exec(`ALTER TABLE meta_away RENAME TO meta`); err != nil {
			t.Fatal(err)
		}
	}
}

// A consumer whose read of the log fails reads again after readRetry, without another event to wake it.
func TestConsumerRetriesAFailedRead(t *testing.T) {
	h := newHarness(t, Options{})
	got := make(chan string, 4)
	if _, err := h.bus.Consume("notify", "config/#", func(m Message) error { got <- string(m.Payload); return nil }); err != nil {
		t.Fatal(err)
	}
	restore := hideMeta(t, h)
	h.publish("config/head", "1", true) // wakes the consumer, whose read fails
	nothing(t, got)
	restore()
	if g := receive(t, got); g != "1" {
		t.Fatalf("got %s", g)
	}
}

// The same over the socket.
func TestClientConsumerRetriesAFailedRead(t *testing.T) {
	h := newHarness(t, Options{})
	c := h.dial("web")
	if err := c.Consume(ctx(t), "notify", "config/#"); err != nil {
		t.Fatal(err)
	}
	restore := hideMeta(t, h)
	h.publish("config/head", "1", true)
	nothing(t, c.Messages())
	restore()
	if d := receive(t, c.Messages()); string(d.Payload) != "1" {
		t.Fatalf("got %+v", d)
	}
}

// A store whose prune fails as it opens is kept, not refused, and the prune is tried again later.
func TestPruneThatFailsAtOpenIsRetried(t *testing.T) {
	defer func(d time.Duration) { pruneInterval = d }(pruneInterval)
	pruneInterval = 50 * time.Millisecond
	dir := t.TempDir()
	path := filepath.Join(dir, "bus.db")
	db, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// An event older than the retention, which a consumer has not read, so only the prune by age removes it; and an
	// expired table the prune cannot write.
	for _, stmt := range []string{
		`INSERT INTO events (topic, payload, time) VALUES ('config/head', '1', 0)`,
		`INSERT INTO consumers (name, filter, cursor, seen) VALUES ('engine/behind', 'config/#', 0, unixepoch())`,
		`DROP TABLE expired`, `CREATE VIEW expired AS SELECT '' AS name, 0 AS time`,
	} {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	b, err := Open(Options{Path: path, Retention: time.Hour})
	if err != nil {
		t.Fatalf("the store was refused: %v", err)
	}
	defer b.Close()
	count := func() int {
		var n int
		if err := b.db.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	time.Sleep(4 * pruneInterval)
	if count() != 1 {
		t.Fatal("the prune succeeded, so this test cannot show it is retried")
	}
	if _, err = b.db.Exec(`DROP VIEW expired; CREATE TABLE expired (name TEXT PRIMARY KEY, time INTEGER NOT NULL) WITHOUT ROWID`); err != nil {
		t.Fatal(err)
	}
	for start := time.Now(); count() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("the prune was never tried again")
		}
	}
}


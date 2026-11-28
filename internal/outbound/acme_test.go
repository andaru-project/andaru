package outbound

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/bus"
)

// An order and its result cross the bus non-durably: neither is in the bus's log, which would keep the credentials.
func TestACMEOrderRoundTripIsNotDurable(t *testing.T) {
	b := newBench(t)
	results := make(chan acme.Result, 1)
	if _, err := b.bus.Subscribe(acme.ResultFilter, func(_ string, payload []byte) {
		var e struct{ Data acme.Result }
		_ = json.Unmarshal(payload, &e)
		results <- e.Data
	}); err != nil {
		t.Fatal(err)
	}
	b.run(&Service{})
	job := acme.Job{JobID: "j1", OrderID: "aco_1", AccountID: "aca_1", DirectoryURL: "https://acme.invalid/directory",
		AccountKey: "not a key: secret-material", Domains: []string{"edge.example.com"}, CSR: []byte{1}, Challenge: acme.Challenge{Type: "http-01"}}
	payload, err := bus.NewEvent(acme.JobType, "engine", job)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.bus.Publish(acme.OrderTopic("aco_1"), payload, false); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-results:
		if r.JobID != "j1" || r.OrderID != "aco_1" || r.AccountID != "aca_1" || r.OK || r.Error == "" {
			t.Fatalf("result %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no result")
	}
	db, err := sql.Open("sqlite", filepath.Join(b.dir, "bus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE topic LIKE 'acme-%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d ACME events in the bus's log", n)
	}
}

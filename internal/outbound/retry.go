package outbound

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/delivery"
)

// retry is a job whose first send failed: it waits in the cache directory, so the bus's cursor need not, and one dead
// channel holds up no other. The file holds the job's credentials, as the bus's log does; it is 0600 in a 0700
// directory and goes once the job is sent or given up.
type retry struct {
	Job      delivery.Job `json:"job"`
	First    time.Time    `json:"first"` // the first attempt: a job is given up an hour after it, restarts or not
	Attempts int          `json:"attempts"`
	Next     time.Time    `json:"next"`
	busy     bool
}

type retries struct {
	mu    sync.Mutex
	dir   string
	items map[string]*retry // by job ID
}

func openRetries(dir string) (*retries, error) {
	r := &retries{dir: filepath.Join(dir, "retry"), items: map[string]*retry{}}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" { // a .tmp a crash left before its rename
			_ = os.RemoveAll(filepath.Join(r.dir, e.Name()))
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
		var item retry
		if err != nil || json.Unmarshal(data, &item) != nil || item.Job.ID == "" {
			_ = os.Remove(filepath.Join(r.dir, e.Name())) // a damaged entry cannot be sent
			continue
		}
		item.Next = time.Time{} // after a restart, a queued job is tried at once
		r.items[item.Job.ID] = &item
	}
	return r, nil
}

func (r *retries) file(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(r.dir, hex.EncodeToString(sum[:])+".json")
}

func (r *retries) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.items[id]
	return ok
}

// save keeps item and writes it. It is kept even when the write fails, so its next attempt still waits its turn.
func (r *retries) save(item *retry) error {
	r.mu.Lock()
	r.items[item.Job.ID] = item
	r.mu.Unlock()
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return writeFile(r.file(item.Job.ID), data)
}

func (r *retries) remove(id string) error {
	r.mu.Lock()
	delete(r.items, id)
	r.mu.Unlock()
	if err := os.Remove(r.file(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// due marks and returns the retries whose time has come and that no attempt is sending.
func (r *retries) due(now time.Time) []*retry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*retry
	for _, item := range r.items {
		if !item.busy && !now.Before(item.Next) {
			item.busy = true
			out = append(out, item)
		}
	}
	return out
}

func (r *retries) release(item *retry) {
	r.mu.Lock()
	item.busy = false
	r.mu.Unlock()
}

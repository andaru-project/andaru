package outbound

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sentFor is how long a sent job's ID is remembered: a job the bus delivers again after a crash is not sent twice.
const sentFor = 24 * time.Hour

// sent is the IDs of the jobs sent in the last day, kept in a file so they survive a restart.
type sent struct {
	mu   sync.Mutex
	path string
	ids  map[string]time.Time
}

func openSent(dir string) (*sent, error) {
	s := &sent{path: filepath.Join(dir, "sent.json"), ids: map[string]time.Time{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	// A damaged file costs at most a repeated message; it is not a reason to stop sending.
	_ = json.Unmarshal(data, &s.ids)
	return s, nil
}

func (s *sent) seen(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.ids[id]
	return ok && now.Sub(at) < sentFor
}

// add records id and writes the file, dropping IDs older than a day.
func (s *sent) add(id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = now
	for k, at := range s.ids {
		if now.Sub(at) >= sentFor {
			delete(s.ids, k)
		}
	}
	data, err := json.Marshal(s.ids)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

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
	return writeFile(s.path, data)
}

// writeFile replaces path with data, mode 0600, and syncs it and its directory: a job the bus has been told is
// handled must survive a power loss.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp) // it may hold a job's credentials
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

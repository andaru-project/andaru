package generation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/andaru-project/andaru/internal/kept"
	"github.com/andaru-project/andaru/internal/render"
)

func (r Realizer) disasterBoot() error {
	store, err := kept.Open(r.StateDir)
	if err != nil {
		return err
	}
	var last error
	for _, name := range []kept.Name{"last-lan", "first"} {
		body, err := store.Read(name)
		if err != nil {
			last = err
			continue
		}
		if err = r.writeDisaster(string(name), body); err != nil {
			last = err
			continue
		}
		return nil
	}
	if last == nil {
		last = errors.New("no kept LAN")
	}
	return errors.Join(errors.New("CONSOLE_REQUIRED"), last)
}

func (r Realizer) writeDisaster(name string, body []byte) error {
	var doc struct {
		Links      json.RawMessage `json:"links"`
		Segments   json.RawMessage `json:"segments"`
		Zones      json.RawMessage `json:"zones"`
		Management json.RawMessage `json:"management"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	decl, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"links":         rawOrEmpty(doc.Links, "[]"),
		"segments":      rawOrEmpty(doc.Segments, "[]"),
		"zones":         rawOrEmpty(doc.Zones, "[]"),
	})
	if err != nil {
		return err
	}
	if _, err = render.Network(render.Input{Declaration: decl}); err != nil {
		return err
	}
	dir := filepath.Join(r.Root, "run", "andaru")
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "disaster-source"), []byte(name), 0o644)
}

func rawOrEmpty(raw json.RawMessage, empty string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(empty)
	}
	return raw
}

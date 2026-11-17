package generation

import (
	"errors"
	"os"

	"github.com/andaru-project/andaru/internal/kept"
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
		if r.RenderDisaster != nil {
			if err = r.RenderDisaster(body); err != nil {
				last = err
				continue
			}
		}
		if err = r.markDisaster(string(name)); err != nil {
			return err
		}
		return nil
	}
	if last == nil {
		last = errors.New("no kept LAN")
	}
	return errors.Join(errors.New("CONSOLE_REQUIRED"), last)
}

func (r Realizer) markDisaster(name string) error {
	dir := r.Root + "/run/andaru"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(dir+"/disaster-source", []byte(name), 0o644)
}

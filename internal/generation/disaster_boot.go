package generation

import (
	"context"
	"errors"
	"os"

	"github.com/andaru-project/andaru/internal/kept"
)

// ErrRenderCleanup means a failed render left files that could not be removed.
// Boot must not try the next copy over that residue.
var ErrRenderCleanup = errors.New("disaster render cleanup failed")

func (r Realizer) disasterBoot(ctx context.Context) (result error) {
	if !r.setUp() {
		// A box never set up has nothing kept to bring back: the factory network runs behind the factory filter.
		return r.factoryFilter(ctx)
	}
	// The kept LAN names no WAN: the catch-all keeps it from the factory network's DHCP, which no filter stops.
	if err := r.place(nil, []placed{catchAll}); err != nil {
		return err
	}
	// Nothing came up: deny-all, and only the console gets in.
	defer func() {
		if result != nil {
			result = errors.Join(result, r.DenyAll(ctx))
		}
	}()
	var store *kept.Store
	var err error
	if r.KeptRoot != "" {
		store, err = kept.OpenIn(r.KeptRoot, r.StateDir)
	} else {
		store, err = kept.Open(r.StateDir)
	}
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
				if errors.Is(err, ErrRenderCleanup) {
					return err
				}
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

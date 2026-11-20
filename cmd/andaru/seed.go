package main

import (
	"os"
	"path/filepath"
)

// stagedSeed is where the ISO's installer leaves the first settings (docs/dev/install-seed.md); tests replace it.
var stagedSeed = "/var/andaru-install/install-seed.json"

// moveSeed moves the installer's seed into the state directory: written there under a temporary name, synced and
// renamed, the directory synced, and only then the staged copy removed. A staged copy next to an existing target is
// stale and discarded. The temporary file is created in the state directory, so it takes that directory's label.
func moveSeed(stateDir string) error {
	body, err := os.ReadFile(stagedSeed)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	target := filepath.Join(stateDir, "install-seed.json")
	if _, err = os.Lstat(target); err == nil {
		return os.Remove(stagedSeed)
	} else if !os.IsNotExist(err) {
		return err
	}
	// A fresh install has no state directory yet: create it as the engine unit's StateDirectoryMode would.
	created := true
	if err = os.Mkdir(stateDir, 0o700); os.IsExist(err) {
		created = false
	} else if err != nil {
		return err
	}
	if err = commitRendered(target, body, 0o600); err != nil {
		return err
	}
	if err = syncDir(stateDir); err != nil {
		return err
	}
	if created {
		if err = syncDir(filepath.Dir(stateDir)); err != nil {
			return err
		}
	}
	return os.Remove(stagedSeed)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

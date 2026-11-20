package main

import (
	"errors"
	"os"
	"path/filepath"
)

// stagedSeed is where the ISO's installer leaves the first settings (docs/dev/install-seed.md); tests replace it.
var stagedSeed = "/var/andaru-install/install-seed.json"

// moveSeed moves the installer's seed into the state directory: written there under a temporary name, synced and
// renamed, the directories synced, and only then the staged copy removed. A staged copy next to an existing target is
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
	// A target already there wins; it is still synced below, as an earlier move may have failed after its rename.
	if _, err = os.Lstat(target); os.IsNotExist(err) {
		// A fresh install has no state directory yet: create it as the engine unit's StateDirectoryMode would.
		if err = os.Mkdir(stateDir, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		// A temporary file left by a power cut mid-move would make commitRendered's exclusive create fail.
		if err = os.Remove(target + ".andaru-tmp"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err = commitRendered(target, body, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// The parent too, for a state directory the move created.
	if err = errors.Join(syncDir(stateDir), syncDir(filepath.Dir(stateDir))); err != nil {
		return err
	}
	return os.Remove(stagedSeed)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

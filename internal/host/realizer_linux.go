package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// RealizerFacts are host observations that decide whether rendered network
// files could work here. They are read-only and carry no configuration.
type RealizerFacts struct {
	// ForeignFiles are networkd files Andaru did not render, as sorted absolute paths.
	ForeignFiles []string
	PPPDaemon    bool
	PPPoEPlugin  bool
	// PPPDevice reports /dev/ppp; Debian cloud kernels ship without PPP.
	PPPDevice bool
}

// ReadRealizerFacts inspects the host without changing it.
func ReadRealizerFacts() (RealizerFacts, error) {
	return readRealizerFacts("/etc/systemd/network", "/run/systemd/network", "/usr/sbin/pppd", "/usr/lib/pppd/*/pppoe.so", "/dev/ppp")
}

func readRealizerFacts(etcDir, runDir, pppd, pluginGlob, device string) (RealizerFacts, error) {
	facts := RealizerFacts{ForeignFiles: []string{}}
	for _, dir := range []string{etcDir, runDir} {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return RealizerFacts{}, fmt.Errorf("list network configuration: %w", err)
		}
		for _, entry := range entries {
			name := entry.Name()
			networkd := slices.Contains([]string{".network", ".netdev", ".link"}, filepath.Ext(name))
			// networkd reads /etc before /run, so anything there overrides Andaru; in /run only Andaru's prefix is its own.
			own := dir == runDir && strings.HasPrefix(name, "50-andaru-")
			if networkd && !own && !entry.IsDir() {
				facts.ForeignFiles = append(facts.ForeignFiles, filepath.Join(dir, name))
			}
		}
	}
	slices.Sort(facts.ForeignFiles)
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	plugins, _ := filepath.Glob(pluginGlob)
	facts.PPPDaemon, facts.PPPoEPlugin, facts.PPPDevice = exists(pppd), len(plugins) > 0, exists(device)
	return facts, nil
}

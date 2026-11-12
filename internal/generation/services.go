package generation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The service files a generation may carry, and what the lease sync keeps beside them at runtime.
const (
	keaFile      = "/run/andaru-kea/kea-dhcp4.conf"
	unboundFile  = "/run/andaru-unbound/andaru.conf"
	resolvedFile = "/run/systemd/resolved.conf.d/50-andaru.conf"
	leasesDir    = "/run/andaru-leases"
	leaseMarker  = "/run/kea/andaru-leases-changed"
)

func find(set []placed, path string) (placed, bool) {
	for _, f := range set {
		if f.path == path {
			return f, true
		}
	}
	return placed{}, false
}

// changed reports a file that appears, goes, or differs; an entry known only by its path counts as different.
func changed(from, to []placed, path string) bool {
	a, was := find(from, path)
	b, is := find(to, path)
	return was != is || is && (a.data == nil || !bytes.Equal(a.data, b.data))
}

// waitFor polls check until it succeeds or VerifyWait passes.
func (r Realizer) waitFor(ctx context.Context, check func() error) error {
	verify, cancel := context.WithTimeout(ctx, r.VerifyWait)
	defer cancel()
	for {
		err := check()
		if err == nil {
			return nil
		}
		select {
		case <-verify.Done():
			return err
		case <-time.After(r.Poll):
		}
	}
}

func (r Realizer) active(ctx context.Context, unit string) bool {
	_, err := r.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", unit)
	return err == nil
}

// services moves the DHCP server, the DNS service and the host resolver from one set of files to the other,
// after networkd has the addresses they serve on. A generation without any of them touches no daemon.
func (r Realizer) services(ctx context.Context, from, to []placed) error {
	_, kea := find(to, keaFile)
	_, hadKea := find(from, keaFile)
	_, unbound := find(to, unboundFile)
	_, hadUnbound := find(from, unboundFile)
	if !kea && !hadKea && !unbound && !hadUnbound && !changed(from, to, resolvedFile) {
		return nil
	}
	// Unbound refuses an include glob over a directory that does not exist.
	if err := os.MkdirAll(r.Root+leasesDir, 0o755); err != nil {
		return err
	}
	switch {
	case kea && (changed(from, to, keaFile) || !r.active(ctx, "kea-dhcp4-server.service")):
		if _, err := r.Run(ctx, "/usr/sbin/kea-dhcp4", "-t", keaFile); err != nil {
			return fmt.Errorf("the DHCP server refuses its configuration: %w", err)
		}
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "restart", "kea-dhcp4-server.service"); err != nil {
			return fmt.Errorf("start the DHCP server: %w", err)
		}
		if err := r.waitFor(ctx, func() error {
			if !r.active(ctx, "kea-dhcp4-server.service") {
				return errors.New("the DHCP server is not running")
			}
			return nil
		}); err != nil {
			return err
		}
	case !kea && hadKea:
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "stop", "kea-dhcp4-server.service"); err != nil {
			return fmt.Errorf("stop the DHCP server: %w", err)
		}
	}
	if unbound && (changed(from, to, unboundFile) || !r.active(ctx, "unbound.service")) || !unbound && hadUnbound {
		if unbound {
			if _, err := r.Run(ctx, "/usr/sbin/unbound-checkconf"); err != nil {
				return fmt.Errorf("the DNS service refuses its configuration: %w", err)
			}
		}
		// A restart, not a reload: Unbound binds new interfaces only when it starts.
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "restart", "unbound.service"); err != nil {
			return fmt.Errorf("reload the DNS service: %w", err)
		}
		if unbound {
			if err := r.waitFor(ctx, func() error {
				_, err := r.Run(ctx, "/usr/sbin/unbound-control", "status")
				return err
			}); err != nil {
				return fmt.Errorf("the DNS service is not answering: %w", err)
			}
		}
	}
	if changed(from, to, resolvedFile) {
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "try-reload-or-restart", "systemd-resolved.service"); err != nil {
			return fmt.Errorf("reload the host resolver: %w", err)
		}
	}
	if kea {
		// The path unit runs the lease sync; /run/kea exists once Kea has started.
		if err := os.WriteFile(r.Root+leaseMarker, nil, 0o644); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// serviceDirs are where the service files and lease names live; Unbound's includes need them at boot.
func (r Realizer) serviceDirs() error {
	for _, dir := range []string{filepath.Dir(keaFile), filepath.Dir(unboundFile), leasesDir} {
		if err := os.MkdirAll(r.Root+dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

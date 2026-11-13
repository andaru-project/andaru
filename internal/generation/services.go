package generation

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The service files a generation may carry, and what the lease sync keeps beside them at runtime.
const (
	keaFile      = "/run/andaru-kea/kea-dhcp4.conf"
	kea6File     = "/run/andaru-kea/kea-dhcp6.conf"
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

// changed reports a file that the new set carries or the old one did: what ran before is known only by its
// paths, so a daemon whose file is present is always given the new one.
func changed(from, to []placed, path string) bool {
	_, was := find(from, path)
	_, is := find(to, path)
	return was || is
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
	_, kea6 := find(to, kea6File)
	_, hadKea6 := find(from, kea6File)
	_, unbound := find(to, unboundFile)
	_, hadUnbound := find(from, unboundFile)
	if !kea && !hadKea && !kea6 && !hadKea6 && !unbound && !hadUnbound && !changed(from, to, resolvedFile) {
		return nil
	}
	// Unbound refuses an include glob over a directory that does not exist.
	if err := os.MkdirAll(r.Root+leasesDir, 0o755); err != nil {
		return err
	}
	if _, ok := find(to, kea6Template); ok {
		// Kea checks and starts on the file filled with the prefix the WAN holds now.
		if err := r.syncPrefixes(ctx, false); err != nil {
			return err
		}
		if err := r.checkTemplate(ctx); err != nil {
			return err
		}
	}
	for _, s := range []keaServer{
		{keaFile, "/usr/sbin/kea-dhcp4", "kea-dhcp4-server.service", keaSocket, "DHCP server", kea, hadKea},
		{kea6File, "/usr/sbin/kea-dhcp6", "kea-dhcp6-server.service", kea6Socket, "DHCPv6 server", kea6, hadKea6},
	} {
		if err := r.keaServe(ctx, s); err != nil {
			return err
		}
	}
	if !kea && hadKea || !kea6 && hadKea6 {
		// No sync answers for a server that is gone, so its clients' names go now, before Unbound starts again.
		if err := writeAtomic(r.Root+leasesDir+"/leases.conf", []byte("server:\n"), 0o644); err != nil {
			return err
		}
	}
	if unbound || hadUnbound {
		if unbound {
			// Debian's config names root.key, which only the unit's ExecStartPre creates; run it, ignoring failure as the unit does.
			_, _ = r.Run(ctx, "/usr/libexec/unbound-helper", "root_trust_anchor_update")
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
	// Also when DHCPv6 goes: the sync removes its routes, which networkd leaves alone.
	if kea || kea6 || hadKea6 {
		// Started, not signalled: root never writes into Kea's own directory, where _kea could plant a link.
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "start", "--no-block", "andaru-leases.service"); err != nil {
			slog.Warn("start the lease sync", "error", err)
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

// keaStatus asks Kea over its control socket whether it runs.
func (r Realizer) keaStatus(ctx context.Context, socket string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", r.Root+socket)
	if err != nil {
		return err
	}
	defer c.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	if _, err := io.WriteString(c, `{"command":"status-get"}`); err != nil {
		return err
	}
	var reply struct {
		Result    int    `json:"result"`
		Text      string `json:"text"`
		Arguments struct {
			Sockets struct{ Status string } `json:"sockets"`
		} `json:"arguments"`
	}
	if err := json.NewDecoder(c).Decode(&reply); err != nil {
		return err
	}
	if reply.Result != 0 {
		return fmt.Errorf("%d: %s", reply.Result, reply.Text)
	}
	// Kea answers while it still retries, or has failed, to bind its DHCP sockets.
	if s := reply.Arguments.Sockets.Status; s != "ready" {
		return fmt.Errorf("its DHCP sockets are %s", cmp.Or(s, "unknown"))
	}
	return nil
}

// keaCheck runs Kea's own checker on file, waiting while it names an interface networkd has not created yet.
func (r Realizer) keaCheck(ctx context.Context, tool, file, what string) error {
	var refused error
	if err := r.waitFor(ctx, func() error {
		_, err := r.Run(ctx, tool, "-t", file)
		if err != nil && !strings.Contains(err.Error(), "doesn't exist in the system") && !strings.Contains(err.Error(), "is not present in the system") {
			refused = err // anything else is the configuration itself: no point waiting
			return nil
		}
		return err
	}); err != nil || refused != nil {
		return fmt.Errorf("the %s refuses its configuration: %w", what, cmp.Or(refused, err))
	}
	return nil
}

// keaServer is one of Kea's two daemons and whether the generations carry its file.
type keaServer struct {
	file, tool, unit, socket, what string
	serve, served                  bool
}

// keaServe checks and restarts a Kea daemon whose file the new generation carries, or stops one it dropped.
func (r Realizer) keaServe(ctx context.Context, s keaServer) error {
	switch {
	case s.serve:
		// Kea refuses an interface that does not exist, and networkctl reload returns before networkd has
		// created every VLAN and bridge.
		if err := r.keaCheck(ctx, s.tool, s.file, s.what); err != nil {
			return err
		}
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "restart", s.unit); err != nil {
			return fmt.Errorf("start the %s: %w", s.what, err)
		}
		// systemd reports Kea active as soon as it forks; only its own answer says it serves.
		if err := r.waitFor(ctx, func() error { return r.keaStatus(ctx, s.socket) }); err != nil {
			return fmt.Errorf("the %s is not answering: %w", s.what, err)
		}
	case s.served:
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "stop", s.unit); err != nil {
			return fmt.Errorf("stop the %s: %w", s.what, err)
		}
	}
	return nil
}

package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// leaseLabel is what a client's hostname must be cut to before it is published; clients choose their names.
var leaseLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

const keaSocket = "/run/kea/kea4-ctrl-socket"

type keaConfig struct {
	Dhcp4 struct {
		Subnet4 []struct {
			ID           int `json:"id"`
			Reservations []struct {
				Hostname string `json:"hostname"`
			} `json:"reservations"`
			UserContext struct {
				Andaru struct {
					Domain   string `json:"domain"`
					Register bool   `json:"register"`
				} `json:"andaru"`
			} `json:"user-context"`
		} `json:"subnet4"`
	}
}

type keaLease struct {
	Address  string `json:"ip-address"`
	Hostname string `json:"hostname"`
	State    int    `json:"state"`
	Subnet   int    `json:"subnet-id"`
	Valid    int    `json:"valid-lft"`
	Cltt     int64  `json:"cltt"`
}

// keaLeases asks Kea for every lease over its control socket.
func (r Realizer) keaLeases(ctx context.Context) ([]keaLease, error) {
	// A wedged Kea must not hold every later sync: the oneshot unit has no start timeout.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", r.Root+keaSocket)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	if _, err := io.WriteString(c, `{"command":"lease4-get-all"}`); err != nil {
		return nil, err
	}
	var reply struct {
		Result    int    `json:"result"`
		Text      string `json:"text"`
		Arguments struct {
			Leases []keaLease `json:"leases"`
		} `json:"arguments"`
	}
	if err := json.NewDecoder(c).Decode(&reply); err != nil {
		return nil, err
	}
	// 3 is Kea's "empty": no lease yet.
	if reply.Result != 0 && reply.Result != 3 {
		return nil, fmt.Errorf("the DHCP server answered %d: %s", reply.Result, reply.Text)
	}
	return reply.Arguments.Leases, nil
}

func (r Realizer) markerStamp() string {
	info, err := os.Stat(r.Root + leaseMarker)
	if err != nil {
		return ""
	}
	return fmt.Sprint(info.ModTime().UnixNano(), info.Size())
}

// SyncLeases publishes the names DHCP clients gave in the DNS service, from Kea's leases and its live
// configuration only. It runs again when Kea signalled while it worked: the path unit does not restart it.
func (r Realizer) SyncLeases(ctx context.Context) error {
	for {
		// A burst of lease events becomes one update.
		time.Sleep(r.LeaseSettle)
		stamp := r.markerStamp()
		if err := r.syncLeases(ctx); err != nil {
			return err
		}
		if r.markerStamp() == stamp {
			return nil
		}
	}
}

func (r Realizer) syncLeases(ctx context.Context) error {
	// One snapshot of configuration and leases, never published across a generation change that removed them.
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	records := []string{}
	raw, err := os.ReadFile(r.Root + keaFile)
	switch {
	case os.IsNotExist(err):
		// No DHCP server: nothing to publish.
	case err != nil:
		return err
	default:
		var cfg keaConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("read the DHCP server's configuration: %w", err)
		}
		leases, err := r.keaLeases(ctx)
		if err != nil {
			return fmt.Errorf("ask the DHCP server for its leases: %w", err)
		}
		if r.afterLeaseRead != nil {
			r.afterLeaseRead()
		}
		records = leaseRecords(cfg, leases)
	}
	body := "server:\n"
	for _, line := range records {
		body += "\t" + line + "\n"
	}
	path := r.Root + leasesDir + "/leases.conf"
	if old, err := os.ReadFile(path); err == nil && string(old) == body {
		return nil
	}
	if err := os.MkdirAll(r.Root+leasesDir, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(path, []byte(body), 0o644); err != nil {
		return err
	}
	if !r.active(ctx, "unbound.service") {
		return nil // Unbound reads the file when it starts
	}
	if _, err = r.Run(ctx, "/usr/sbin/unbound-control", "reload_keep_cache"); err != nil {
		// Unbound still serves the old names; without the file the next sync writes it and reloads again.
		return errors.Join(err, os.Remove(path))
	}
	return nil
}

// leaseRecords turns assigned leases with valid names into A and PTR records, sorted.
// Reserved names win over leases; between two leases with one name, the most recently renewed wins.
func leaseRecords(cfg keaConfig, leases []keaLease) []string {
	// Reserved names are taken on every segment: they share one domain.
	domains, taken := map[int]string{}, map[string]bool{}
	for _, s := range cfg.Dhcp4.Subnet4 {
		for _, res := range s.Reservations {
			taken[strings.ToLower(res.Hostname)] = true
		}
		if s.UserContext.Andaru.Register && s.UserContext.Andaru.Domain != "" {
			domains[s.ID] = s.UserContext.Andaru.Domain
		}
	}
	newest := map[string]keaLease{}
	for _, l := range leases {
		domain, ok := domains[l.Subnet]
		label, _, _ := strings.Cut(strings.ToLower(l.Hostname), ".")
		if !ok || l.State != 0 || l.Hostname == "" {
			continue
		}
		if !leaseLabel.MatchString(label) || taken[label] {
			slog.Debug("lease name not published", "address", l.Address, "hostname", l.Hostname)
			continue
		}
		name := label + "." + domain + "."
		if old, seen := newest[name]; !seen || l.Cltt > old.Cltt {
			newest[name] = l
		}
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(newest)) {
		l := newest[name]
		ttl := l.Valid / 3 // as RFC 4702 suggests for names learned from DHCP
		out = append(out, fmt.Sprintf("local-data: %q", fmt.Sprintf("%s %d IN A %s", name, ttl, l.Address)),
			fmt.Sprintf("local-data-ptr: %q", fmt.Sprintf("%s %d %s", l.Address, ttl, name)))
	}
	return out
}

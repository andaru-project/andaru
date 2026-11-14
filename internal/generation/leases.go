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

const (
	keaSocket  = "/run/kea/kea4-ctrl-socket"
	kea6Socket = "/run/kea/kea6-ctrl-socket"
)

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
	Address   string `json:"ip-address"`
	PrefixLen int    `json:"prefix-len"`
	Type      string `json:"type"` // DHCPv6 only: IA_NA or IA_PD
	Hostname  string `json:"hostname"`
	State     int    `json:"state"`
	Subnet    int    `json:"subnet-id"`
	Valid     int    `json:"valid-lft"`
	Cltt      int64  `json:"cltt"`
}

// keaLeases asks Kea's DHCPv4 server for every lease over its control socket.
func (r Realizer) keaLeases(ctx context.Context) ([]keaLease, error) {
	return r.askLeases(ctx, keaSocket, "lease4-get-all")
}

// askLeases asks one of Kea's servers for every lease.
func (r Realizer) askLeases(ctx context.Context, socket, command string) ([]keaLease, error) {
	// A wedged Kea must not hold every later sync: the oneshot unit has no start timeout.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var args struct {
		Leases []keaLease `json:"leases"`
	}
	result, text, err := r.keaCommand(ctx, socket, `{"command":"`+command+`"}`, &args)
	if err != nil {
		return nil, err
	}
	// 3 is Kea's "empty": no lease yet.
	if result != 0 && result != 3 {
		return nil, fmt.Errorf("the DHCP server answered %d: %s", result, text)
	}
	return args.Leases, nil
}

// keaCommand sends one JSON request body over a Kea control socket and decodes the reply's arguments into args.
func (r Realizer) keaCommand(ctx context.Context, socket, body string, args any) (int, string, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", r.Root+socket)
	if err != nil {
		return 0, "", err
	}
	defer c.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	if _, err := io.WriteString(c, body); err != nil {
		return 0, "", err
	}
	var reply struct {
		Result    int             `json:"result"`
		Text      string          `json:"text"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(c).Decode(&reply); err != nil {
		return 0, "", err
	}
	if args != nil && len(reply.Arguments) > 0 {
		if err := json.Unmarshal(reply.Arguments, args); err != nil {
			return 0, "", err
		}
	}
	return reply.Result, reply.Text, nil
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

// publishedNames returns the records of one family that the published file holds, for a server that does not
// answer.
func (r Realizer) publishedNames(v6 bool) []string {
	data, _ := os.ReadFile(r.Root + leasesDir + "/leases.conf")
	var out []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "local-data") {
			continue
		}
		if family6 := strings.Contains(line, " IN AAAA ") || (fields[0] == "local-data-ptr:" && strings.Contains(fields[1], ":")); family6 == v6 {
			out = append(out, line)
		}
	}
	return out
}

func (r Realizer) syncLeases(ctx context.Context) error {
	// One snapshot of configuration and leases, never published across a generation change that removed them.
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	// DHCPv6 first: its routes are reconciled even when the DHCPv4 server does not answer.
	v6, err := r.syncKea6(ctx)
	if err != nil {
		return err
	}
	records := []string{}
	var failed error
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
			// Its names stay as published; DHCPv6's are still updated, and the failure is reported.
			failed = fmt.Errorf("ask the DHCP server for its leases: %w", err)
			records = r.publishedNames(false)
			break
		}
		if r.afterLeaseRead != nil {
			r.afterLeaseRead()
		}
		records = leaseRecords(cfg, leases)
	}
	records = append(records, v6...)
	body := "server:\n"
	for _, line := range records {
		body += "\t" + line + "\n"
	}
	path := r.Root + leasesDir + "/leases.conf"
	if old, err := os.ReadFile(path); err == nil && string(old) == body {
		return failed
	}
	if err := os.MkdirAll(r.Root+leasesDir, 0o755); err != nil {
		return errors.Join(failed, err)
	}
	if err := writeAtomic(path, []byte(body), 0o644); err != nil {
		return errors.Join(failed, err)
	}
	if !r.active(ctx, "unbound.service") {
		return failed // Unbound reads the file when it starts
	}
	if _, err = r.Run(ctx, "/usr/sbin/unbound-control", "reload_keep_cache"); err != nil {
		// Unbound still serves the old names; without the file the next sync writes it and reloads again.
		return errors.Join(failed, err, os.Remove(path))
	}
	return failed
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

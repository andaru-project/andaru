package generation

import (
	"github.com/andaru-project/andaru/internal/delegation"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

const (
	// pdEvents is where Kea's hook, as _kea, appends the router each delegated prefix went to.
	pdEvents = "/run/kea/andaru-pd-events"
	// pdTaken is where root moves it before reading: never opened in Kea's own directory.
	pdTaken = leasesDir + "/pd-events"
	// pdPrev is the last batch applied, read once more: the hook may have appended through a descriptor it
	// opened before the batch moved.
	pdPrev = leasesDir + "/pd-events.prev"
	// pdTable keeps the believed next hops, so routes return at boot before any router renews.
	pdTable = "/var/lib/andaru/pd-next-hops.json"
	// pdProto marks the routes Andaru installs for delegated prefixes; no other route is touched. 77 is assigned to
	// no routing daemon (FRR uses 186 to 198, so 188, OSPF, would delete its routes); rt_protos.d names it andaru.
	pdProto = "77"
	// pdEventsMax bounds what a hostile or runaway hook can make root read.
	pdEventsMax = 1 << 20
)

// validInterface is a kernel interface name: what the hook may name, and nothing that could be read as more.
var validInterface = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

type nextHop struct {
	Via string `json:"via"`
	Dev string `json:"dev"`
}

type kea6Config struct {
	Dhcp6 struct {
		Subnet6 []struct {
			ID        int    `json:"id"`
			Interface string `json:"interface"`
			PDPools   []struct {
				Prefix    string `json:"prefix"`
				PrefixLen int    `json:"prefix-len"`
			} `json:"pd-pools"`
			Reservations []struct {
				Hostname    string   `json:"hostname"`
				IPAddresses []string `json:"ip-addresses"`
			} `json:"reservations"`
			UserContext struct {
				Andaru struct {
					Domain   string `json:"domain"`
					Register bool   `json:"register"`
					// Delegated subnets are filled by root: their pools' routes and static names are root's.
					Delegated bool `json:"delegated"`
					Names     bool `json:"names"`
				} `json:"andaru"`
			} `json:"user-context"`
		} `json:"subnet6"`
	}
}

// syncKea6 returns the DHCPv6 lease names and reconciles the delegated-prefix routes; without a DHCPv6 server
// it removes every route it installed.
func (r Realizer) syncKea6(ctx context.Context) ([]string, error) {
	var cfg kea6Config
	var leases []keaLease
	// Taken before the lease list: the hook runs after Kea commits, so any lease an event names is in the list.
	events, done := r.takeEvents()
	raw, err := os.ReadFile(r.Root + kea6File)
	running := err == nil
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("read the DHCPv6 server's configuration: %w", err)
		}
	}
	// The events are gone from Kea's directory: what they taught is saved before anything else can fail.
	table, served := r.nextHops(cfg, events)
	if err := r.saveNextHops(table); err != nil {
		return nil, err
	}
	done()
	if running {
		if leases, err = r.askLeases(ctx, kea6Socket, "lease6-get-all"); err != nil {
			// Its own names and routes wait for it; the DHCPv4 names do not.
			slog.Warn("ask the DHCPv6 server for its leases", "error", err)
			return r.publishedNames(true), nil
		}
	}
	if err := r.syncRoutes(ctx, leases, table, served, delegatedPools(cfg), r.waitingInterfaces(cfg)); err != nil {
		return nil, err
	}
	return lease6Records(cfg, leases), nil
}

// lease6Records turns assigned address leases with valid names into AAAA and PTR records. DHCPv6's own static
// names win over its leases; DHCPv4's do not, since one device carries its name in both families.
func lease6Records(cfg kea6Config, leases []keaLease) []string {
	domains, taken := map[int]string{}, map[string]bool{}
	for _, s := range cfg.Dhcp6.Subnet6 {
		for _, res := range s.Reservations {
			// Only a static address publishes the name; a static prefix alone leaves it to the dynamic lease.
			if len(res.IPAddresses) > 0 {
				taken[strings.ToLower(res.Hostname)] = true
			}
		}
		if s.UserContext.Andaru.Register && s.UserContext.Andaru.Domain != "" {
			domains[s.ID] = s.UserContext.Andaru.Domain
		}
	}
	var static []string
	for _, s := range cfg.Dhcp6.Subnet6 {
		if a := s.UserContext.Andaru; a.Delegated && a.Names && a.Domain != "" {
			// The generation knew only their suffixes, so these static names are published here, at the current prefix.
			for _, res := range s.Reservations {
				if label := strings.ToLower(res.Hostname); len(res.IPAddresses) > 0 && leaseLabel.MatchString(label) {
					name := label + "." + a.Domain + "."
					static = append(static, fmt.Sprintf("local-data: %q", fmt.Sprintf("%s 3600 IN AAAA %s", name, res.IPAddresses[0])),
						fmt.Sprintf("local-data-ptr: %q", fmt.Sprintf("%s 3600 %s", res.IPAddresses[0], name)))
				}
			}
		}
	}
	newest := map[string]keaLease{}
	for _, l := range leases {
		domain, ok := domains[l.Subnet]
		label, _, _ := strings.Cut(strings.ToLower(l.Hostname), ".")
		if !ok || l.Type != "IA_NA" || l.State != 0 || !leaseLabel.MatchString(label) || taken[label] {
			continue
		}
		name := label + "." + domain + "."
		if old, seen := newest[name]; !seen || l.Cltt > old.Cltt {
			newest[name] = l
		}
	}
	out := static
	for _, name := range slices.Sorted(maps.Keys(newest)) {
		l := newest[name]
		ttl := l.Valid / 3
		out = append(out, fmt.Sprintf("local-data: %q", fmt.Sprintf("%s %d IN AAAA %s", name, ttl, l.Address)),
			fmt.Sprintf("local-data-ptr: %q", fmt.Sprintf("%s %d %s", l.Address, ttl, name)))
	}
	return out
}

// syncRoutes installs a route via the requesting router for each active delegated prefix whose next hop it
// believes, and removes every route of its own protocol nobody holds.
// delegatedPools are the pools of delegated subnets, which networkd does not route: unreachable where unleased.
func delegatedPools(cfg kea6Config) map[string]bool {
	pools := map[string]bool{}
	for _, s := range cfg.Dhcp6.Subnet6 {
		for _, p := range s.PDPools {
			if a, err := netip.ParseAddr(p.Prefix); err == nil && s.UserContext.Andaru.Delegated {
				pools[netip.PrefixFrom(a, p.PrefixLen).String()] = true
			}
		}
	}
	return pools
}

// waitingInterfaces are delegated segments the template serves but Kea's file leaves out: their prefix expired.
// Their routers' next hops are kept for when it returns.
func (r Realizer) waitingInterfaces(cfg kea6Config) map[string]bool {
	var tpl delegation.Template
	raw, err := os.ReadFile(r.Root + kea6Template)
	if err != nil || json.Unmarshal(raw, &tpl) != nil {
		return nil
	}
	waiting := map[string]bool{}
	for _, s := range tpl.Subnets {
		waiting[s.Interface] = true
	}
	for _, s := range cfg.Dhcp6.Subnet6 {
		delete(waiting, s.Interface)
	}
	return waiting
}

func (r Realizer) syncRoutes(ctx context.Context, leases []keaLease, table map[string]nextHop, served func(netip.Prefix, nextHop) bool, pools, waiting map[string]bool) error {
	wanted := map[string]nextHop{}
	for _, l := range leases {
		if l.Type != "IA_PD" || l.State != 0 {
			continue
		}
		a, err := netip.ParseAddr(l.Address)
		if err != nil {
			continue
		}
		prefix := netip.PrefixFrom(a, l.PrefixLen)
		key := prefix.String()
		if hop, ok := table[key]; ok && served(prefix, hop) {
			wanted[key] = hop
		}
	}
	for _, pool := range slices.Sorted(maps.Keys(pools)) {
		// A pool handing out itself is its router's route while leased, which an unreachable one would replace.
		if wanted[pool] != (nextHop{}) {
			continue
		}
		if _, err := r.Run(ctx, "/usr/sbin/ip", "-6", "route", "replace", "unreachable", pool, "proto", pdProto); err != nil {
			slog.Warn("install a delegation pool's route", "pool", pool, "error", err)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(wanted)) {
		hop := wanted[key]
		if _, err := r.Run(ctx, "/usr/sbin/ip", "-6", "route", "replace", key, "via", hop.Via, "dev", hop.Dev, "proto", pdProto); err != nil {
			slog.Warn("install a delegated prefix route", "prefix", key, "error", err)
		}
	}
	out, err := r.Run(ctx, "/usr/sbin/ip", "-6", "route", "show", "proto", pdProto)
	if err != nil {
		return fmt.Errorf("list the delegated prefix routes: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) > 1 && fields[0] == "unreachable":
			if !pools[fields[1]] {
				if _, err := r.Run(ctx, "/usr/sbin/ip", "-6", "route", "del", "unreachable", fields[1], "proto", pdProto); err != nil {
					slog.Warn("remove a delegation pool's route", "pool", fields[1], "error", err)
				}
			}
		case len(fields) > 0 && wanted[fields[0]] == (nextHop{}):
			if _, err := r.Run(ctx, "/usr/sbin/ip", "-6", "route", "del", fields[0], "proto", pdProto); err != nil {
				slog.Warn("remove a delegated prefix route", "prefix", fields[0], "error", err)
			}
		}
	}
	keep := maps.Clone(wanted)
	for prefix, hop := range table {
		if waiting[hop.Dev] {
			keep[prefix] = hop
		}
	}
	return r.saveNextHops(keep)
}

func (r Realizer) saveNextHops(table map[string]nextHop) error {
	data, _ := json.Marshal(table)
	if err := os.MkdirAll(r.Root+"/var/lib/andaru", 0o755); err != nil {
		return err
	}
	return writeAtomic(r.Root+pdTable, data, 0o600)
}

// nextHops reads the saved next hops and applies the hook's events to them, with the test of what the
// generation serves.
func (r Realizer) nextHops(cfg kea6Config, events []string) (map[string]nextHop, func(netip.Prefix, nextHop) bool) {
	table := map[string]nextHop{}
	if data, err := os.ReadFile(r.Root + pdTable); err == nil {
		_ = json.Unmarshal(data, &table) // a damaged table only costs routes until the routers renew
	}
	// What the generation lets a record claim: its interfaces, and the prefixes each may delegate.
	pools := map[string][]netip.Prefix{}
	for _, s := range cfg.Dhcp6.Subnet6 {
		for _, p := range s.PDPools {
			if a, err := netip.ParseAddr(p.Prefix); err == nil {
				pools[s.Interface] = append(pools[s.Interface], netip.PrefixFrom(a, p.PrefixLen))
			}
		}
	}
	// A record names a served interface and a prefix of that segment's pool; a saved hop is judged again,
	// since the generation may have changed since it was believed.
	served := func(prefix netip.Prefix, hop nextHop) bool {
		return slices.ContainsFunc(pools[hop.Dev], func(p netip.Prefix) bool { return p.Contains(prefix.Addr()) && p.Bits() <= prefix.Bits() })
	}
	for _, line := range events {
		op, prefix, hop, ok := parseEvent(line)
		switch {
		case !ok:
			slog.Warn("delegated prefix event ignored", "line", line)
		case !served(prefix, hop):
			slog.Warn("delegated prefix event outside the served pools", "prefix", prefix, "dev", hop.Dev)
		case op == "del":
			delete(table, prefix.String())
		default:
			table[prefix.String()] = hop
		}
	}
	return table, served
}

// takeEvents moves the hook's file out of Kea's directory and returns the lines of the previous batch and of
// this one, with what to call once their next hops are saved. A batch a failed sync left is read again before
// any new one is taken.
func (r Realizer) takeEvents() ([]string, func()) {
	taken, prev := r.Root+pdTaken, r.Root+pdPrev
	if err := os.MkdirAll(r.Root+leasesDir, 0o755); err != nil {
		return nil, func() {}
	}
	if _, err := os.Lstat(taken); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(r.Root+pdEvents, taken); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("take the delegated prefix events", "error", err)
		}
	}
	lines := append(r.readEvents(prev), r.readEvents(taken)...)
	return lines, func() {
		if err := os.Rename(taken, prev); errors.Is(err, os.ErrNotExist) {
			os.Remove(prev)
		}
	}
}

// readEvents reads a batch in root's own directory, believing nothing of a file that is not a regular one
// owned by Kea, the owner of its directory, or that is too large; a refused batch is deleted.
func (r Realizer) readEvents(path string) []string {
	// Root's own directory holds it, so what Lstat sees is what Open gets: a FIFO would block, a directory
	// would stop every later rename.
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		slog.Warn("delegated prefix events refused: not a regular file")
		os.RemoveAll(path)
		return nil
	}
	dir, err := os.Stat(r.Root + "/run/kea")
	if err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		slog.Warn("delegated prefix events refused", "error", err)
		os.Remove(path)
		return nil
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	kea, _ := dir.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || kea == nil || owner.Uid != kea.Uid || info.Size() > pdEventsMax {
		slog.Warn("delegated prefix events refused: not a regular file of Kea's, or too large")
		os.Remove(path)
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, pdEventsMax))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// parseEvent reads "add|del <prefix> <link-local next hop> <interface>".
func parseEvent(line string) (string, netip.Prefix, nextHop, bool) {
	f := strings.Fields(line)
	if len(f) != 4 || f[0] != "add" && f[0] != "del" || !validInterface.MatchString(f[3]) {
		return "", netip.Prefix{}, nextHop{}, false
	}
	p, err := netip.ParsePrefix(f[1])
	via, err2 := netip.ParseAddr(f[2])
	if err != nil || err2 != nil || !p.Addr().Is6() || p != p.Masked() || !via.Is6() || !via.IsLinkLocalUnicast() || via.Zone() != "" {
		return "", netip.Prefix{}, nextHop{}, false
	}
	return f[0], p, nextHop{Via: via.String(), Dev: f[3]}, true
}

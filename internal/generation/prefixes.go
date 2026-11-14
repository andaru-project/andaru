package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/delegation"
)

const (
	// kea6Template holds the delegated segments' DHCPv6 subnets, filled with the WAN's prefix into kea6File.
	kea6Template = "/run/andaru-kea/kea-dhcp6.template.json"
	// delegatedState is what the prefix sync last saw each WAN receive; the engine reads it too.
	delegatedState = "/var/lib/andaru/delegated-prefixes.json"
)

// delegatedLink is a delegated segment as its rendered networkd file describes it.
type delegatedLink struct {
	Interface, Uplink string
	SubnetID          uint64
	// Announce: networkd advertises the delegated /64 here, so a replaced one is deprecated here.
	Announce       bool
	RouterLifetime uint16
	Managed, Other bool
	Preference     string
}

// readDelegatedLinks parses Andaru's networkd files for the delegated segments and each uplink's requested length.
func (r Realizer) readDelegatedLinks() ([]delegatedLink, map[string]int, map[string]bool) {
	paths, _ := filepath.Glob(r.Root + "/run/systemd/network/50-andaru-*.network")
	var links []delegatedLink
	hints, wans := map[string]int{}, map[string]bool{}
	for _, path := range paths {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "50-andaru-"), ".network")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		l := delegatedLink{Interface: name, RouterLifetime: 1800, Preference: "medium"}
		section, delegated, sends := "", false, false
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = line
				if line == "[DHCPv6]" {
					wans[name] = true // a DHCPv6 client, which may receive a prefix of its own
				}
				continue
			}
			key, value, _ := strings.Cut(line, "=")
			switch section + key {
			case "[DHCPv6]PrefixDelegationHint":
				if n, err := strconv.Atoi(strings.TrimPrefix(value, "::/")); err == nil {
					hints[name] = n
				}
			case "[DHCPPrefixDelegation]UplinkInterface":
				l.Uplink, delegated = value, true
			case "[DHCPPrefixDelegation]SubnetId":
				l.SubnetID, _ = strconv.ParseUint(strings.TrimPrefix(value, "0x"), 16, 64)
			case "[DHCPPrefixDelegation]Announce":
				l.Announce = value == "yes"
			case "[IPv6SendRA]RouterLifetimeSec":
				if n, err := strconv.ParseUint(value, 10, 16); err == nil {
					l.RouterLifetime = uint16(n)
				}
			case "[IPv6SendRA]Managed":
				l.Managed = value == "yes"
			case "[IPv6SendRA]OtherInformation":
				l.Other = value == "yes"
			case "[IPv6SendRA]RouterPreference":
				l.Preference = value
			case "[Network]IPv6SendRA":
				sends = value == "yes"
			}
		}
		l.Announce = l.Announce && sends
		if delegated {
			links = append(links, l)
		}
	}
	return links, hints, wans
}

// seenPrefix is a delegated prefix networkd holds and the lifetimes of the LAN addresses numbered from it.
type seenPrefix struct {
	prefix             netip.Prefix
	preferred, valid   int
	numbersOneOfOurLAN bool
}

// readKernel returns the prefixes networkd holds per uplink: its unreachable route for each, matched to an
// uplink through the delegated LANs numbered from it.
func (r Realizer) readKernel(ctx context.Context, links []delegatedLink, wans map[string]bool) (map[string][]seenPrefix, error) {
	out, err := r.Run(ctx, "/usr/sbin/ip", "-j", "-6", "route", "show", "type", "unreachable", "proto", "dhcp")
	if err != nil {
		return nil, fmt.Errorf("list the delegated prefixes: %w", err)
	}
	var routes []struct {
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal(out, &routes); err != nil {
		return nil, fmt.Errorf("read the delegated prefixes: %w", err)
	}
	if out, err = r.Run(ctx, "/usr/sbin/ip", "-j", "-6", "addr", "show", "scope", "global"); err != nil {
		return nil, fmt.Errorf("list the LAN addresses: %w", err)
	}
	var ifaces []struct {
		Name  string `json:"ifname"`
		Addrs []struct {
			Local     string `json:"local"`
			Preferred int    `json:"preferred_life_time"`
			Valid     int    `json:"valid_life_time"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(out, &ifaces); err != nil {
		return nil, fmt.Errorf("read the LAN addresses: %w", err)
	}
	uplinks := map[string]bool{}
	for _, l := range links {
		uplinks[l.Uplink] = true
	}
	seen := map[string][]seenPrefix{}
	for _, rt := range routes {
		p, err := netip.ParsePrefix(rt.Dst)
		// A delegation longer than /64 numbers no segment.
		if err != nil || !p.Addr().Is6() || p.Bits() > 64 {
			continue
		}
		matched := false
		for _, l := range links {
			for _, ifc := range ifaces {
				if ifc.Name != l.Interface {
					continue
				}
				for _, a := range ifc.Addrs {
					if addr, err := netip.ParseAddr(a.Local); err == nil && p.Contains(addr) && !matched {
						seen[l.Uplink] = append(seen[l.Uplink], seenPrefix{p, a.Preferred, a.Valid, true})
						matched = true
					}
				}
			}
		}
		// A prefix numbering none of the LANs (none fits it) is placed only when the host has a single DHCPv6
		// client, which is then the only one that can hold it: so the view and the apply can say it does not fit.
		if !matched && len(uplinks) == 1 && len(wans) == 1 {
			for up := range uplinks {
				seen[up] = append(seen[up], seenPrefix{prefix: p})
			}
		}
	}
	return seen, nil
}

// currentPrefixes decides each uplink's current prefix, updates the saved state, and returns the prefixes to
// serve: the preferred one networkd holds, else the saved one while it is valid. Without kernel only the saved
// state counts, as at boot before networkd runs.
func (r Realizer) currentPrefixes(ctx context.Context, links []delegatedLink, hints map[string]int, wans map[string]bool, kernel bool) (map[string]delegation.Held, delegation.State, error) {
	state, err := delegation.ReadState(r.Root + delegatedState)
	if err != nil {
		slog.Warn("read the saved delegated prefixes", "error", err)
	}
	seen := map[string][]seenPrefix{}
	if kernel {
		if seen, err = r.readKernel(ctx, links, wans); err != nil {
			return nil, state, err
		}
	}
	now := r.Now()
	uplinks := map[string]bool{}
	for _, l := range links {
		uplinks[l.Uplink] = true
	}
	serve := map[string]delegation.Held{}
	for up := range uplinks {
		w := state.WANs[up]
		w.Requested = hints[up]
		var best *seenPrefix
		for _, s := range seen[up] {
			// The current prefix follows what networkd reports, also once it is no longer preferred.
			if s.numbersOneOfOurLAN && w.Current != nil && s.prefix == w.Current.Prefix {
				w.Current = &delegation.Held{Prefix: s.prefix, Preferred: now.Add(time.Duration(s.preferred) * time.Second), Valid: now.Add(time.Duration(s.valid) * time.Second)}
			}
		}
		for i, s := range seen[up] {
			// Graceful renumbering keeps the old prefix valid but not preferred: the preferred one is current.
			if s.numbersOneOfOurLAN && s.preferred > 0 && (best == nil || s.preferred > best.preferred) {
				best = &seen[up][i]
			}
		}
		if best == nil {
			for i, s := range seen[up] {
				if !s.numbersOneOfOurLAN {
					best = &seen[up][i]
				}
			}
		}
		if best != nil {
			held := &delegation.Held{Prefix: best.prefix, Preferred: now.Add(time.Duration(best.preferred) * time.Second), Valid: now.Add(time.Duration(best.valid) * time.Second)}
			if !best.numbersOneOfOurLAN && w.Current != nil && w.Current.Prefix == best.prefix {
				held = w.Current // no LAN carries its lifetimes; keep what was last seen
			} else if !best.numbersOneOfOurLAN {
				held.Preferred, held.Valid = now.Add(time.Hour), now.Add(time.Hour)
			}
			if w.Current != nil && w.Current.Prefix != held.Prefix {
				w.Replaced = append(w.Replaced, *w.Current)
			}
			w.Current = held
		}
		w.Replaced = slices.DeleteFunc(w.Replaced, func(h delegation.Held) bool {
			return !h.Valid.After(now) || w.Current != nil && h.Prefix == w.Current.Prefix
		})
		if w.Current != nil && w.Current.Valid.After(now) {
			serve[up] = *w.Current
		}
		state.WANs[up] = w
	}
	return serve, state, nil
}

// fillKea6 returns Kea's DHCPv6 file with the template's subnets filled under prefixes, replacing any filled
// before; false when there is no template.
func (r Realizer) fillKea6(prefixes map[string]delegation.Held) ([]byte, bool, error) {
	raw, err := os.ReadFile(r.Root + kea6Template)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var tpl delegation.Template
	if err := json.Unmarshal(raw, &tpl); err != nil {
		return nil, false, fmt.Errorf("read the DHCPv6 template: %w", err)
	}
	subnets, skipped := delegation.FillTemplate(tpl, prefixes, r.Now())
	for _, s := range skipped {
		slog.Warn("the delegated prefix has no room; not served", "what", s)
	}
	base, err := os.ReadFile(r.Root + kea6File)
	if err != nil {
		return nil, false, err
	}
	var conf map[string]map[string]any
	if err := json.Unmarshal(base, &conf); err != nil || conf["Dhcp6"] == nil {
		return nil, false, fmt.Errorf("read the DHCPv6 server's configuration: %v", err)
	}
	ours := map[float64]bool{}
	for _, s := range tpl.Subnets {
		ours[float64(s.ID)] = true
	}
	kept := []any{}
	if old, ok := conf["Dhcp6"]["subnet6"].([]any); ok {
		for _, s := range old {
			if m, ok := s.(map[string]any); !ok || !ours[toFloat(m["id"])] {
				kept = append(kept, s)
			}
		}
	}
	for _, s := range subnets {
		kept = append(kept, s)
	}
	conf["Dhcp6"]["subnet6"] = kept
	data, err := json.MarshalIndent(conf, "", "  ")
	return append(data, '\n'), true, err
}

// SyncPrefixes follows the prefixes the WANs receive: Kea's DHCPv6 subnets on delegated segments, and the state
// the engine shows. It runs on networkd's link changes, on a timer, and after the apply. It also reconciles the
// delegated-prefix routes, so a route whose install failed is retried within a minute, not at the router's renewal.
func (r Realizer) SyncPrefixes(ctx context.Context) error {
	links, _, _ := r.readDelegatedLinks()
	if _, err := os.Stat(r.Root + kea6File); len(links) == 0 && os.IsNotExist(err) {
		return nil // nothing delegated and no DHCPv6 server: the timer's run touches nothing
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	err = r.syncPrefixes(ctx, true)
	// Only the routes matter here: the lease sync publishes the names.
	_, routes := r.syncKea6(ctx, keaConfig{})
	return errors.Join(err, routes)
}

// syncPrefixes holds the lock. A running Kea is handed the new configuration first, and the file written only
// once Kea took it; the apply passes reconfigure false, since it restarts Kea on the file itself. The state is
// saved last, so a run that fails sees the same change again.
func (r Realizer) syncPrefixes(ctx context.Context, reconfigure bool) error {
	links, hints, wans := r.readDelegatedLinks()
	if len(links) == 0 {
		return nil
	}
	before, _ := delegation.ReadState(r.Root + delegatedState)
	prefixes, state, err := r.currentPrefixes(ctx, links, hints, wans, true)
	if err != nil {
		return err
	}
	data, ok, err := r.fillKea6(prefixes)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(r.Root + kea6File); ok && (err != nil || !bytes.Equal(old, data)) {
		if reconfigure {
			if err := r.keaConfigSet(ctx, data); err != nil {
				return err
			}
		}
		if err := writeAtomic(r.Root+kea6File, data, 0o644); err != nil {
			return err
		}
		if reconfigure {
			// Names and pool routes follow the new prefix; Kea's reconfiguration fires no lease event.
			if _, err := r.Run(ctx, "/usr/bin/systemctl", "start", "--no-block", "andaru-leases.service"); err != nil {
				slog.Warn("start the lease sync", "error", err)
			}
		}
	}
	now := r.Now() // one reading: sendDeprecations tells this run's fresh entries by it
	list, err := r.recordReplaced(links, before, state, now)
	if err != nil {
		return err
	}
	if reconfigure {
		r.sendDeprecations(links, list, now)
	}
	return r.saveState(before, state)
}

// bootPrefixes fills Kea's file from the saved prefixes before networkd and Kea start, so Kea's first lease
// sync still finds the delegated pools and keeps their next hops.
func (r Realizer) bootPrefixes() error {
	links, hints, wans := r.readDelegatedLinks()
	prefixes, _, _ := r.currentPrefixes(context.Background(), links, hints, wans, false)
	data, ok, err := r.fillKea6(prefixes)
	if err != nil || !ok {
		return err
	}
	return writeAtomic(r.Root+kea6File, data, 0o644)
}

func toFloat(v any) float64 { f, _ := v.(float64); return f }

func (r Realizer) saveState(before, s delegation.State) error {
	data, _ := json.Marshal(s)
	if old, _ := json.Marshal(before); bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.Root+delegatedState), 0o755); err != nil {
		return err
	}
	return writeAtomic(r.Root+delegatedState, data, 0o644)
}

// keaConfigSet hands a running Kea its new configuration. A Kea not running yet reads the file when it starts.
func (r Realizer) keaConfigSet(ctx context.Context, conf []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, text, err := r.keaCommand(ctx, kea6Socket, `{"command":"config-set","arguments":`+string(conf)+`}`, nil)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return nil // not running: it reads the file when it starts
	}
	if err != nil {
		return fmt.Errorf("reconfigure the DHCPv6 server: %w", err)
	}
	if result != 0 {
		return fmt.Errorf("the DHCPv6 server refused the new prefix's configuration (%d): %s", result, text)
	}
	return nil
}

// kea6Check is where the apply has Kea's checker judge the delegated subnets, filled with a documentation prefix.
const kea6Check = "/run/andaru-kea/kea-dhcp6.check.conf"

// checkTemplate fills the delegated subnets under 2001:db8::/<requested length> and has Kea's checker judge them,
// so a template Kea refuses fails the apply even before any prefix is known.
func (r Realizer) checkTemplate(ctx context.Context) error {
	links, hints, _ := r.readDelegatedLinks()
	doc := map[string]delegation.Held{}
	for _, l := range links {
		far := r.Now().Add(24 * time.Hour)
		doc[l.Uplink] = delegation.Held{Prefix: netip.PrefixFrom(netip.MustParseAddr("2001:db8::"), cmpOr(hints[l.Uplink], 48)), Preferred: far, Valid: far}
	}
	data, ok, err := r.fillKea6(doc)
	if err != nil || !ok {
		return err
	}
	if err := writeAtomic(r.Root+kea6Check, data, 0o644); err != nil {
		return err
	}
	defer os.Remove(r.Root + kea6Check)
	return r.keaCheck(ctx, "/usr/sbin/kea-dhcp6", kea6Check, "DHCPv6 server")
}

func cmpOr(n, fallback int) int {
	if n == 0 || n > 64 {
		return fallback
	}
	return n
}

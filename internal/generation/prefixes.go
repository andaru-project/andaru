package generation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
func (r Realizer) readDelegatedLinks() ([]delegatedLink, map[string]int) {
	paths, _ := filepath.Glob(r.Root + "/run/systemd/network/50-andaru-*.network")
	var links []delegatedLink
	hints := map[string]int{}
	for _, path := range paths {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "50-andaru-"), ".network")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		l := delegatedLink{Interface: name, RouterLifetime: 1800, Preference: "medium"}
		section, delegated := "", false
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = line
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
				if value != "yes" {
					l.Announce = false
				}
			}
		}
		if delegated {
			links = append(links, l)
		}
	}
	return links, hints
}

// seenPrefix is a delegated prefix networkd holds and the lifetimes of the LAN addresses numbered from it.
type seenPrefix struct {
	prefix             netip.Prefix
	preferred, valid   int
	numbersOneOfOurLAN bool
}

// readKernel returns the prefixes networkd holds per uplink: its unreachable route for each, matched to an
// uplink through the delegated LANs numbered from it.
func (r Realizer) readKernel(ctx context.Context, links []delegatedLink) (map[string][]seenPrefix, error) {
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
		if err != nil || !p.Addr().Is6() {
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
		// shortcut: a prefix numbering none of the LANs (none fits it) is only placed when one uplink delegates;
		// with several, it cannot be told whose it is until a LAN fits.
		if !matched && len(uplinks) == 1 {
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
func (r Realizer) currentPrefixes(ctx context.Context, links []delegatedLink, hints map[string]int, kernel bool) (map[string]netip.Prefix, delegation.State, error) {
	state, err := delegation.ReadState(r.Root + delegatedState)
	if err != nil {
		slog.Warn("read the saved delegated prefixes", "error", err)
	}
	seen := map[string][]seenPrefix{}
	if kernel {
		if seen, err = r.readKernel(ctx, links); err != nil {
			return nil, state, err
		}
	}
	now := r.Now()
	uplinks := map[string]bool{}
	for _, l := range links {
		uplinks[l.Uplink] = true
	}
	serve := map[string]netip.Prefix{}
	for up := range uplinks {
		w := state.WANs[up]
		w.Requested = hints[up]
		var best *seenPrefix
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
				w.Last = w.Current
			}
			w.Current = held
		}
		if w.Current != nil && w.Current.Valid.After(now) {
			serve[up] = w.Current.Prefix
		}
		state.WANs[up] = w
	}
	return serve, state, nil
}

// fillKea6 returns Kea's DHCPv6 file with the template's subnets filled under prefixes, replacing any filled
// before; false when there is no template.
func (r Realizer) fillKea6(prefixes map[string]netip.Prefix) ([]byte, bool, error) {
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
	subnets, skipped, err := delegation.FillTemplate(raw, prefixes)
	if err != nil {
		return nil, false, err
	}
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
// the engine shows. It runs on networkd's link changes, on a timer, and after the apply.
func (r Realizer) SyncPrefixes(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return r.syncPrefixes(ctx, true)
}

// syncPrefixes holds the lock. A reconfigure is sent to a running Kea and the file written only once Kea took
// it; the apply passes reconfigure false, since it restarts Kea on the file itself.
func (r Realizer) syncPrefixes(ctx context.Context, reconfigure bool) error {
	links, hints := r.readDelegatedLinks()
	prefixes, state, err := r.currentPrefixes(ctx, links, hints, true)
	if err != nil {
		return err
	}
	if err := r.saveState(state); err != nil {
		return err
	}
	data, ok, err := r.fillKea6(prefixes)
	if err != nil || !ok {
		return err
	}
	if old, err := os.ReadFile(r.Root + kea6File); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if !reconfigure {
		return writeAtomic(r.Root+kea6File, data, 0o644)
	}
	if err := r.keaConfigSet(ctx, data); err != nil {
		return err
	}
	if err := writeAtomic(r.Root+kea6File, data, 0o644); err != nil {
		return err
	}
	// Names and pool routes follow the new prefix; Kea's reconfiguration fires no lease event.
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "start", "--no-block", "andaru-leases.service"); err != nil {
		slog.Warn("start the lease sync", "error", err)
	}
	return nil
}

// bootPrefixes fills Kea's file from the saved prefixes before networkd and Kea start, so Kea's first lease
// sync still finds the delegated pools and keeps their next hops.
func (r Realizer) bootPrefixes() error {
	links, hints := r.readDelegatedLinks()
	prefixes, _, _ := r.currentPrefixes(context.Background(), links, hints, false)
	data, ok, err := r.fillKea6(prefixes)
	if err != nil || !ok {
		return err
	}
	return writeAtomic(r.Root+kea6File, data, 0o644)
}

func toFloat(v any) float64 { f, _ := v.(float64); return f }

func (r Realizer) saveState(s delegation.State) error {
	data, _ := json.Marshal(s)
	if err := os.MkdirAll(filepath.Dir(r.Root+delegatedState), 0o755); err != nil {
		return err
	}
	return writeAtomic(r.Root+delegatedState, data, 0o644)
}

// keaConfigSet hands a running Kea its new configuration. A Kea not running yet reads the file when it starts.
func (r Realizer) keaConfigSet(ctx context.Context, conf []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", r.Root+kea6Socket)
	if err != nil {
		return nil
	}
	defer c.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	if _, err := io.WriteString(c, `{"command":"config-set","arguments":`+string(conf)+`}`); err != nil {
		return err
	}
	var reply struct {
		Result int    `json:"result"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&reply); err != nil {
		return fmt.Errorf("reconfigure the DHCPv6 server: %w", err)
	}
	if reply.Result != 0 {
		return fmt.Errorf("the DHCPv6 server refused the new prefix's configuration (%d): %s", reply.Result, reply.Text)
	}
	return nil
}

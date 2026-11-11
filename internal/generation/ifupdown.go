package generation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/host"
)

const handBackWait = 30 * time.Second

// captureIfupdown records what ifupdown configures before a takeover replaces it. Until a takeover is
// confirmed each attempt captures afresh; afterwards ifupdown is masked and what it says is no baseline.
func (r Realizer) captureIfupdown(ctx context.Context, p Pointers) (IfupdownBaseline, error) {
	if p.Takeover {
		_, base, err := ReadBaselineKind(r.dir())
		if err == nil && base == nil {
			err = errors.New("the confirmed takeover recorded no ifupdown baseline")
		}
		if err != nil {
			return IfupdownBaseline{}, err
		}
		return *base, nil
	}
	o, err := r.observe(ctx, false)
	if err != nil {
		return IfupdownBaseline{}, err
	}
	interfaces, problems := host.ReadIfupdown(r.Root, o.links, []string{o.route})
	if len(problems) > 0 {
		return IfupdownBaseline{}, fmt.Errorf("ifupdown changed since the apply was judged: %s: %s", problems[0].File, problems[0].Message)
	}
	_, err = r.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "systemd-networkd.service")
	base := IfupdownBaseline{Interfaces: interfaces, NetworkdWasActive: err == nil, Connected: []string{}}
	for _, i := range interfaces {
		if l, ok := o.link(i.Name); ok && globalIPv4(l) {
			base.Connected = append(base.Connected, i.Name)
		}
	}
	if err := os.MkdirAll(r.baselineDir(), 0o700); err != nil {
		return IfupdownBaseline{}, err
	}
	return base, writeJSON(filepath.Join(r.baselineDir(), "manifest.json"), baselineManifest{Kind: "ifupdown", Files: []baselineFile{}, Ifupdown: &base})
}

// handOver stops dhcpcd without dropping its addresses and starts networkd, which then takes the interfaces.
func (r Realizer) handOver(ctx context.Context, b IfupdownBaseline) error {
	for _, i := range b.Interfaces {
		if i.DHCP {
			if _, err := r.Run(ctx, "/usr/sbin/dhcpcd", "-p", "-x", i.Name); err != nil {
				return err
			}
		}
	}
	_, err := r.Run(ctx, "/usr/bin/systemctl", "start", "systemd-networkd.service")
	return err
}

// handBack returns the interfaces to ifupdown after Andaru's files are gone. Every step runs even after an
// earlier one failed, and an interface already back is left alone, so the timer can repeat it safely.
// Only ifupdown's own units raise interfaces: a dhcpcd started from a realize unit dies with that unit.
// removed are the files just taken out of /run: the devices they made are deleted while networkd still runs.
// After a confirmed takeover ifupdown is masked, so it is unmasked first, as boot does.
func (r Realizer) handBack(ctx context.Context, b IfupdownBaseline, removed []placed, confirmed bool) error {
	var failures []error
	run := func(name string, args ...string) {
		if _, err := r.Run(ctx, name, args...); err != nil {
			failures = append(failures, err)
		}
	}
	if confirmed {
		run("/usr/bin/systemctl", "unmask", "networking.service", "ifup@.service")
	}
	// networkd keeps a device whose .netdev is gone, so delete the takeover's devices while it still runs.
	// No reload: with the files gone it could drop the addresses that stopping networkd keeps.
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "systemd-networkd.service"); err == nil {
		for _, f := range removed {
			if name, ok := strings.CutSuffix(filepath.Base(f.path), ".netdev"); ok {
				run("/usr/bin/networkctl", "delete", strings.TrimPrefix(name, "50-andaru-"))
			}
		}
	}
	if b.NetworkdWasActive {
		run("/usr/bin/networkctl", "reload")
	} else {
		run("/usr/bin/systemctl", "stop", "systemd-networkd.service", "systemd-networkd.socket")
	}
	o, err := r.observe(ctx, false)
	if err != nil {
		failures = append(failures, err)
	}
	auto := false
	for _, i := range b.Interfaces {
		if i.DHCP && host.DhcpcdRunning(r.Root, i.Name) || !i.DHCP && hasStatic(o, i) {
			continue
		}
		if i.Hotplug {
			run("/usr/bin/systemctl", "restart", "ifup@"+i.Name+".service")
		} else {
			auto = true
		}
	}
	if auto {
		run("/usr/bin/systemctl", "restart", "networking.service")
	}
	deadline := time.Now().Add(cmp.Or(r.HandBackWait, handBackWait))
	for missing := slices.Clone(b.Connected); ; time.Sleep(cmp.Or(r.Poll, time.Second)) {
		if o, err := r.observe(ctx, false); err == nil {
			missing = slices.DeleteFunc(missing, func(name string) bool { l, ok := o.link(name); return ok && globalIPv4(l) })
		}
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			failures = append(failures, fmt.Errorf("ifupdown did not bring back the IPv4 address of %v", missing))
			break
		}
	}
	return errors.Join(failures...)
}

// hasStatic reports that a static interface has every address ifupdown gives it.
func hasStatic(o observation, i host.IfupdownInterface) bool {
	l, ok := o.link(i.Name)
	want := slices.Concat(i.IPv4.Addresses, i.IPv6.Addresses)
	return ok && len(want) > 0 && !slices.ContainsFunc(want, func(w string) bool {
		return !slices.ContainsFunc(l.Addresses, func(a host.AddressStatus) bool { return fmt.Sprintf("%s/%d", a.Address, a.PrefixLength) == w })
	})
}

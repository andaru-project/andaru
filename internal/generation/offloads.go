package generation

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/host"
)

// offloadDefaultsPath records, per permanent MAC, the offloads each Ethernet device ran before Andaru set any. It is
// under /run because a device's defaults are whatever its driver chose this boot.
const offloadDefaultsPath = "/run/andaru-offloads/defaults.json"

// offloadRecordLimit bounds the record at boot, inside the network boot unit's 30s start limit.
const offloadRecordLimit = 5 * time.Second

// neverRestored is the feature a default never brings back: the kernel turns LRO off on a device that forwards, and
// turning a driver's LRO back on would break forwarding. It runs only where the operator turns it on, off elsewhere.
const neverRestored = "large-receive-offload"

// onDiskLinkFiles reads the Andaru .link files currently in /run/systemd/network under root.
func onDiskLinkFiles(root string) []placed {
	var onDisk []placed
	files, _ := filepath.Glob(root + "/run/systemd/network/50-andaru-*.link")
	for _, file := range files {
		if data, err := os.ReadFile(file); err == nil {
			onDisk = append(onDisk, placed{path: file, data: data})
		}
	}
	return onDisk
}

// FillOffloadDefaults populates each reported offload's Default on devices from the recorded driver defaults and
// active .link files under root. Fixed offloads keep their active state; non-fixed LRO defaults to off; an offload
// already declared before the device was recorded has no known driver default and stays nil.
func FillOffloadDefaults(root string, devices []host.DeviceOffloads) {
	record := map[string]map[string]bool{}
	if _, err := readJSON(root+offloadDefaultsPath, &record); err != nil {
		slog.Warn("read the recorded device offloads", "error", err)
		record = map[string]map[string]bool{}
	}
	applied := declaredOffloads(onDiskLinkFiles(root))
	for i := range devices {
		mac := strings.ToLower(devices[i].PermanentAddress)
		rec, hasRec := record[mac]
		decl := applied[mac]
		for _, o := range host.Offloads {
			state, ok := devices[i].Offloads[o.Key]
			if !ok || state.Default != nil {
				continue
			}
			switch {
			case state.Fixed:
				v := state.Active
				state.Default = &v
			case slices.Contains(o.Features, neverRestored):
				v := false
				state.Default = &v
			case hasRec:
				seen, on := false, false
				for _, name := range o.Features {
					if v, recorded := rec[name]; recorded {
						seen = true
						on = on || v
					}
				}
				if seen {
					state.Default = &on
				}
			default:
				declared := false
				for _, name := range o.Features {
					if _, isDeclared := decl[name]; isDeclared {
						declared = true
						break
					}
				}
				if !declared {
					v := state.Active
					state.Default = &v
				}
			}
			devices[i].Offloads[o.Key] = state
		}
	}
}

// recordOffloadDefaults records every Ethernet device not recorded yet, as it runs now, and returns the record and
// the devices. udev applies an Andaru .link file when a device appears, so the features the files on disk declare
// for a device say nothing of its driver and are left out: at boot there are none yet; a verb records before it
// places its files, so a device that appeared under the old ones is judged by those.
func (r Realizer) recordOffloadDefaults(ctx context.Context) (map[string]map[string]bool, []host.LinkStatus, error) {
	links, err := host.EthernetLinks(ctx, r.Run)
	if err != nil {
		return nil, nil, err
	}
	path := r.Root + offloadDefaultsPath
	record := map[string]map[string]bool{}
	if _, err := readJSON(path, &record); err != nil {
		// Rewritten below: a device whose defaults are lost only keeps what it runs until the next boot.
		slog.Warn("read the recorded device offloads", "error", err)
		record = map[string]map[string]bool{}
	}
	applied := declaredOffloads(onDiskLinkFiles(r.Root))
	changed := false
	for _, l := range links {
		mac := strings.ToLower(l.PermanentAddress)
		if _, ok := record[mac]; ok {
			continue
		}
		features, err := host.ReadFeatures(ctx, r.Run, l.Name)
		if err != nil {
			slog.Warn("record the device offloads", "device", l.Name, "error", err)
			continue
		}
		defaults := map[string]bool{}
		for _, o := range host.Offloads {
			for _, name := range o.Features {
				f, ok := features[name]
				_, declared := applied[mac][name]
				if ok && !f.Fixed && !declared && name != neverRestored {
					defaults[name] = f.Requested
				}
			}
		}
		record[mac], changed = defaults, true
	}
	if changed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, nil, err
		}
		if err := writeJSON(path, record); err != nil {
			return nil, nil, err
		}
	}
	return record, links, nil
}

// noteOffloadDefaults records the devices not recorded yet before a verb replaces the files udev applied to them.
// shortcut: a device this cannot read is recorded after the files change, judged by the new ones; a device hot-plugged
// under a file that set an offload, then unreadable here, records that offload as its default until the next boot.
func (r Realizer) noteOffloadDefaults(ctx context.Context) {
	if _, _, err := r.recordOffloadDefaults(ctx); err != nil {
		slog.Warn("record the device offloads", "error", err)
	}
}

// declaredOffloads reads the kernel features each .link file in set turns on or off, by permanent MAC.
func declaredOffloads(set []placed) map[string]map[string]bool {
	byKey := map[string]host.Offload{}
	for _, o := range host.Offloads {
		byKey[o.Networkd] = o
	}
	out := map[string]map[string]bool{}
	for _, f := range set {
		if !strings.HasSuffix(f.path, ".link") {
			continue
		}
		mac, features := "", map[string]bool{}
		for line := range strings.SplitSeq(string(f.data), "\n") {
			key, value, _ := strings.Cut(line, "=")
			if key == "PermanentMACAddress" {
				mac = strings.ToLower(value)
			} else if o, ok := byKey[key]; ok {
				for _, name := range o.Features {
					features[name] = value == "yes"
				}
			}
		}
		if mac != "" && len(features) > 0 {
			out[mac] = features
		}
	}
	return out
}

// applyOffloads runs every Ethernet device, configured or not, with the offloads set declares and its recorded
// defaults for the rest, so an offload set back to the default, a rollback and a reset take effect without a reboot.
// udev applies .link files only when a device appears, and asking it to again would also rename devices that are up.
// Failures are logged: a device that refuses a feature still forwards, and the live state shows what it runs.
func (r Realizer) applyOffloads(ctx context.Context, set []placed) {
	record, links, err := r.recordOffloadDefaults(ctx)
	if err != nil {
		slog.Warn("read the device offloads", "error", err)
		return
	}
	declared := declaredOffloads(set)
	for _, l := range links {
		mac := strings.ToLower(l.PermanentAddress)
		want := map[string]bool{neverRestored: false}
		maps.Copy(want, record[mac])
		maps.Copy(want, declared[mac])
		features, err := host.ReadFeatures(ctx, r.Run, l.Name)
		if err != nil {
			slog.Warn("read the device offloads", "device", l.Name, "error", err)
			continue
		}
		var args []string
		for _, name := range slices.Sorted(maps.Keys(want)) {
			if f, ok := features[name]; ok && !f.Fixed && f.Requested != want[name] {
				value := "off"
				if want[name] {
					value = "on"
				}
				args = append(args, name, value)
			}
		}
		if len(args) == 0 {
			continue
		}
		if _, err := r.Run(ctx, host.Ethtool, append([]string{"-K", l.Name}, args...)...); err != nil {
			slog.Warn("set the device offloads", "device", l.Name, "error", err)
		}
	}
}

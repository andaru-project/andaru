package generation

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/host"
)

// offloadDefaultsPath records, per permanent MAC, the offloads each Ethernet device ran before Andaru set any. It is
// under /run because a device's defaults are whatever its driver chose this boot.
const offloadDefaultsPath = "/run/andaru-offloads/defaults.json"

// offloadDefaults returns the recorded defaults and the Ethernet devices, first recording any device not seen yet as
// it runs now: at boot that is before any .link file applies; a device that appears later has had none applied either.
func (r Realizer) offloadDefaults(ctx context.Context) (map[string]map[string]bool, []host.LinkStatus, error) {
	data, err := r.Run(ctx, "/usr/bin/ip", "-d", "-j", "address", "show")
	if err != nil {
		return nil, nil, err
	}
	links, err := host.ParseLinks(data)
	if err != nil {
		return nil, nil, err
	}
	links = slices.DeleteFunc(links, func(l host.LinkStatus) bool { return !host.Physical(l) })
	path := r.Root + offloadDefaultsPath
	record := map[string]map[string]bool{}
	if _, err := readJSON(path, &record); err != nil {
		return nil, nil, err
	}
	changed := false
	for _, l := range links {
		mac := strings.ToLower(l.PermanentAddress)
		if _, ok := record[mac]; ok {
			continue
		}
		features, err := r.features(ctx, l.Name)
		if err != nil {
			return nil, nil, err
		}
		defaults := map[string]bool{}
		for _, o := range host.Offloads {
			for _, name := range o.Features {
				if f, ok := features[name]; ok && !f.Fixed {
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

func (r Realizer) features(ctx context.Context, device string) (map[string]host.Feature, error) {
	data, err := r.Run(ctx, host.Ethtool, "--json", "-k", device)
	if err != nil {
		return nil, err
	}
	return host.ParseFeatures(data)
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

// applyOffloads runs every Ethernet device with the offloads set declares and its recorded defaults for the rest, so
// an offload set back to the default, a rollback and a reset take effect without a reboot. udev applies .link files
// only when a device appears, and asking it to again would also rename devices that are up. Failures are logged: a
// device that refuses a feature still forwards, and the live state shows what it runs.
func (r Realizer) applyOffloads(ctx context.Context, set []placed) {
	record, links, err := r.offloadDefaults(ctx)
	if err != nil {
		slog.Warn("read the device offloads", "error", err)
		return
	}
	declared := declaredOffloads(set)
	for _, l := range links {
		mac := strings.ToLower(l.PermanentAddress)
		want := maps.Clone(record[mac])
		if want == nil {
			want = map[string]bool{}
		}
		maps.Copy(want, declared[mac])
		features, err := r.features(ctx, l.Name)
		if err != nil {
			slog.Warn("read the device offloads", "device", l.Name, "error", err)
			continue
		}
		var args []string
		for _, name := range slices.Sorted(maps.Keys(want)) {
			if f, ok := features[name]; ok && !f.Fixed && f.Requested != want[name] {
				args = append(args, name, map[bool]string{true: "on", false: "off"}[want[name]])
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

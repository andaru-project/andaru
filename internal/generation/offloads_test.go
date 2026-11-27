package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

// offloadHost fakes ip and ethtool for one Ethernet device whose features change as ethtool -K asks.
type offloadHost struct {
	requested, active, fixed map[string]bool
	set                      [][]string
}

func (h *offloadHost) run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch {
	case name == "/usr/bin/ip":
		return []byte(`[{"ifindex":2,"ifname":"wan","link_type":"ether","mtu":1500,"operstate":"UP","flags":[],"address":"52:54:00:00:00:01","permaddr":"52:54:00:aa:00:01","addr_info":[]}]`), nil
	case strings.HasSuffix(name, "ethtool") && args[0] == "--json":
		entry := map[string]any{"ifname": args[2], "tx-checksumming": map[string]any{"active": true, "fixed": nil, "requested": nil}}
		for f, on := range h.requested {
			entry[f] = map[string]any{"active": on && h.active[f], "fixed": h.fixed[f], "requested": on}
		}
		return json.Marshal([]any{entry})
	case strings.HasSuffix(name, "ethtool") && args[0] == "-K":
		h.set = append(h.set, args[1:])
		for i := 2; i+1 < len(args); i += 2 {
			h.requested[args[i]] = args[i+1] == "on"
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s %v", name, args)
}

func newOffloadHost() *offloadHost {
	return &offloadHost{
		// TSO is asked for but does not run (its checksum dependency is off): what was asked for is the default.
		requested: map[string]bool{"generic-receive-offload": true, "rx-vlan-offload": true, "tx-tcp-segmentation": true, "large-receive-offload": true, "rx-gro-hw": false},
		active:    map[string]bool{"generic-receive-offload": true, "rx-vlan-offload": true, "large-receive-offload": true},
		fixed:     map[string]bool{"rx-gro-hw": true},
	}
}

const wanLink = "[Match]\nPermanentMACAddress=52:54:00:AA:00:01\n\n[Link]\nName=wan\n"

func TestOffloadsFollowTheLinkFileAndReturnToTheDefaults(t *testing.T) {
	h := newOffloadHost()
	r := Realizer{Root: t.TempDir(), Run: h.run}
	link := placed{path: "/run/systemd/network/50-andaru-wan.link", data: []byte(wanLink + "GenericReceiveOffload=no\nGenericReceiveOffloadHardware=yes\nReceiveVLANCTAGHardwareAcceleration=yes\n")}

	r.applyOffloads(context.Background(), []placed{link})
	// GRO-HW is fixed and VLAN acceleration already runs as declared: GRO changes, and LRO, never on unless declared.
	if want := [][]string{{"wan", "generic-receive-offload", "off", "large-receive-offload", "off"}}; !reflect.DeepEqual(h.set, want) {
		t.Fatalf("set %v, want %v", h.set, want)
	}

	// The defaults were recorded before anything changed, so a set without the .link file restores them.
	r.applyOffloads(context.Background(), nil)
	if want := []string{"wan", "generic-receive-offload", "on"}; len(h.set) != 2 || !reflect.DeepEqual(h.set[1], want) {
		t.Fatalf("set %v, want a second call %v", h.set, want)
	}
	data, err := os.ReadFile(r.Root + offloadDefaultsPath)
	if err != nil {
		t.Fatal(err)
	}
	// Requested, not active; never LRO, which the kernel turns off on a forwarding device.
	if want := `{"52:54:00:aa:00:01":{"generic-receive-offload":true,"rx-vlan-offload":true,"tx-tcp-segmentation":true}}`; strings.TrimSpace(string(data)) != want {
		t.Fatalf("recorded defaults: %s, want %s", data, want)
	}

	// LRO runs only while declared.
	r.applyOffloads(context.Background(), []placed{{path: link.path, data: []byte(wanLink + "LargeReceiveOffload=yes\n")}})
	r.applyOffloads(context.Background(), nil)
	if want := [][]string{{"wan", "large-receive-offload", "on"}, {"wan", "large-receive-offload", "off"}}; !reflect.DeepEqual(h.set[2:], want) {
		t.Fatalf("set %v, want LRO on then off", h.set)
	}
}

func TestOffloadsOfADeviceSeenUnderALinkFileAreNotItsDefaults(t *testing.T) {
	h := newOffloadHost()
	r := Realizer{Root: t.TempDir(), Run: h.run}
	// The device appeared while this file was in place: udev turned GRO off.
	writeFile(t, filepath.Join(r.Root, "run/systemd/network/50-andaru-wan.link"), wanLink+"GenericReceiveOffload=no\n")
	h.requested["generic-receive-offload"], h.requested["large-receive-offload"] = false, false
	r.noteOffloadDefaults(context.Background())
	r.applyOffloads(context.Background(), nil)
	if len(h.set) != 0 {
		t.Fatalf("set %v: GRO off came from the .link file, not the driver", h.set)
	}
}

func TestADamagedOffloadRecordStillAppliesTheDeclaredOffloads(t *testing.T) {
	h := newOffloadHost()
	r := Realizer{Root: t.TempDir(), Run: h.run}
	writeFile(t, r.Root+offloadDefaultsPath, "{")
	r.applyOffloads(context.Background(), []placed{{path: "/run/systemd/network/50-andaru-wan.link", data: []byte(wanLink + "GenericReceiveOffload=no\n")}})
	if want := [][]string{{"wan", "generic-receive-offload", "off", "large-receive-offload", "off"}}; !reflect.DeepEqual(h.set, want) {
		t.Fatalf("set %v, want %v", h.set, want)
	}
	// A fresh record replaced the damaged one.
	var record map[string]map[string]bool
	if _, err := readJSON(r.Root+offloadDefaultsPath, &record); err != nil || record["52:54:00:aa:00:01"] == nil {
		t.Fatalf("record %v, %v", record, err)
	}
}

const eth0Link = "50-andaru-eth0.link"

func TestRollbackReturnsOffloadsToTheirDefaults(t *testing.T) {
	b := newBench(t)
	b.net.offloads = map[string]bool{"generic-receive-offload": true}
	b.live(one)
	next := maps.Clone(two)
	next[eth0Link] = "[Match]\nPermanentMACAddress=02:00:00:00:00:01\n\n[Link]\nName=eth0\nGenericReceiveOffload=no\n"
	id := b.gen(next)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if want := []string{"eth0 generic-receive-offload off"}; !slices.Equal(b.net.offloadSets, want) {
		t.Fatalf("after activate %q, want %q", b.net.offloadSets, want)
	}
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	if want := []string{"eth0 generic-receive-offload off", "eth0 generic-receive-offload on"}; !slices.Equal(b.net.offloadSets, want) {
		t.Fatalf("after rollback %q, want %q", b.net.offloadSets, want)
	}
}

func TestBootRecordsOffloadsBeforeTheLinkFiles(t *testing.T) {
	b := newBench(t)
	b.net.offloads = map[string]bool{"generic-receive-offload": true}
	id := b.gen(map[string]string{"50-andaru-eth0.network": uplinkDHCP, eth0Link: "[Match]\nPermanentMACAddress=02:00:00:00:00:01\n\n[Link]\nName=eth0\nGenericReceiveOffload=no\n"})
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.offloads["generic-receive-offload"] = true // the driver's default again
	recorded := false
	b.net.onRun = func(command string) {
		if strings.HasPrefix(command, "ethtool --json") && !recorded {
			recorded = true
			if b.exists(netDir + eth0Link) {
				t.Error("the offloads were first read with the .link file in place")
			}
		}
	}
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(b.net.root, offloadDefaultsPath))
	if err != nil || !strings.Contains(string(data), `"generic-receive-offload":true`) {
		t.Fatalf("recorded %s, %v", data, err)
	}
}

// udev applies a .link file's offloads when a device appears, which the fake does not: this does it at the trigger.
func (b *bench) udevAppliesOffloads() {
	b.net.onRun = func(command string) {
		if command != "udevadm trigger --settle --action=add --subsystem-match=net" {
			return
		}
		for feature, on := range declaredOffloads([]placed{{path: eth0Link, data: []byte(b.net.netFiles()[eth0Link])}})["02:00:00:00:00:01"] {
			b.net.offloads[feature] = on
		}
	}
}

func TestBootRestoresTheOffloadsOfAnUnusableCandidate(t *testing.T) {
	b := newBench(t)
	b.net.offloads = map[string]bool{"generic-receive-offload": true}
	id := b.gen(filtered(map[string]string{"50-andaru-eth0.network": uplinkDHCP, eth0Link: "[Match]\nPermanentMACAddress=02:00:00:00:00:01\n\n[Link]\nName=eth0\nGenericReceiveOffload=no\n"}, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.offloads["generic-receive-offload"] = true
	b.net.offloadSets = nil
	b.udevAppliesOffloads()
	b.net.fail = "nft -f"
	if err := b.r.Boot(t.Context()); err == nil {
		t.Fatal("boot reported success with nothing usable")
	}
	if !b.net.offloads["generic-receive-offload"] {
		t.Fatalf("GRO stayed as the dropped candidate set it: %q", b.net.offloadSets)
	}
}

// Rollback and reset record a device not seen yet before they remove the files udev applied to it.
func TestVerbsRecordBeforeTheyReplaceTheLinkFiles(t *testing.T) {
	for _, verb := range []string{"rollback", "reset"} {
		t.Run(verb, func(t *testing.T) {
			b := newBench(t)
			b.net.offloads = map[string]bool{"generic-receive-offload": true}
			b.live(one)
			next := maps.Clone(two)
			next[eth0Link] = "[Match]\nPermanentMACAddress=02:00:00:00:00:01\n\n[Link]\nName=eth0\nGenericReceiveOffload=no\n"
			id := b.gen(next)
			if err := b.r.Activate(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			// The device is plugged in under the new files: udev turned GRO off, and nothing recorded it yet.
			if err := os.Remove(filepath.Join(b.net.root, offloadDefaultsPath)); err != nil {
				t.Fatal(err)
			}
			b.net.offloads["generic-receive-offload"] = false
			var err error
			if verb == "rollback" {
				err = b.timerFires(id)
			} else {
				err = b.r.Teardown(t.Context())
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(b.net.root, offloadDefaultsPath))
			if err != nil || strings.Contains(string(data), "generic-receive-offload") {
				t.Fatalf("recorded %s, %v: GRO off came from the .link file", data, err)
			}
		})
	}
}

func TestFillOffloadDefaults(t *testing.T) {
	root := t.TempDir()
	// Recorded device: GRO defaulted on (even though currently off), tx-checksum-ip-generic defaulted on,
	// tcpSegmentation was pre-declared so absent from the record.
	writeFile(t, root+offloadDefaultsPath, `{"52:54:00:aa:00:01":{"generic-receive-offload":true,"tx-checksum-ip-generic":true,"ntuple-filters":false}}`)
	// Unrecorded device has a .link file declaring GRO, so only undeclared features fall back to live state.
	writeFile(t, filepath.Join(root, "run/systemd/network/50-andaru-lan.link"), "[Match]\nPermanentMACAddress=52:54:00:AA:00:02\n\n[Link]\nName=lan\nGenericReceiveOffload=no\n")

	devices := []host.DeviceOffloads{
		{
			Interface:        "wan",
			PermanentAddress: "52:54:00:AA:00:01",
			Offloads: map[string]host.OffloadState{
				"genericReceive":         {Active: false, Fixed: false},
				"transmitChecksum":       {Active: false, Fixed: false},
				"ntupleFilter":           {Active: true, Fixed: false},
				"tcpSegmentation":        {Active: false, Fixed: false},
				"genericReceiveHardware": {Active: false, Fixed: true},
				"receiveChecksum":        {Active: true, Fixed: true},
				"largeReceive":           {Active: true, Fixed: false},
			},
		},
		{
			Interface:        "lan",
			PermanentAddress: "52:54:00:AA:00:02",
			Offloads: map[string]host.OffloadState{
				"genericReceive":  {Active: false, Fixed: false},
				"tcpSegmentation": {Active: true, Fixed: false},
				"largeReceive":    {Active: true, Fixed: true},
			},
		},
	}

	FillOffloadDefaults(root, devices)

	check := func(dev host.DeviceOffloads, key string, want *bool) {
		t.Helper()
		got := dev.Offloads[key].Default
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%s.%s default = %v, want %v", dev.Interface, key, got, want)
		}
	}
	on, off := true, false
	check(devices[0], "genericReceive", &on)
	check(devices[0], "transmitChecksum", &on)
	check(devices[0], "ntupleFilter", &off)
	check(devices[0], "tcpSegmentation", nil)
	check(devices[0], "genericReceiveHardware", &off)
	check(devices[0], "receiveChecksum", &on)
	check(devices[0], "largeReceive", &off)

	check(devices[1], "genericReceive", nil)
	check(devices[1], "tcpSegmentation", &on)
	check(devices[1], "largeReceive", &on)

	// A damaged defaults file falls back cleanly without panicking or dropping undeclared defaults.
	damaged := t.TempDir()
	writeFile(t, damaged+offloadDefaultsPath, "{")
	fallback := []host.DeviceOffloads{{
		Interface:        "wan",
		PermanentAddress: "52:54:00:aa:00:01",
		Offloads:         map[string]host.OffloadState{"genericReceive": {Active: true}},
	}}
	FillOffloadDefaults(damaged, fallback)
	check(fallback[0], "genericReceive", &on)
}

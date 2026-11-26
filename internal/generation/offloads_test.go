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
	// GRO-HW is fixed and VLAN acceleration already runs as declared: only GRO changes.
	if want := [][]string{{"wan", "generic-receive-offload", "off"}}; !reflect.DeepEqual(h.set, want) {
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

	// The kernel turned LRO off once the device forwarded: no default turns it back on.
	h.requested["large-receive-offload"] = false
	r.applyOffloads(context.Background(), nil)
	if len(h.set) != 2 {
		t.Fatalf("set %v, want no further call", h.set)
	}
}

func TestOffloadsOfADeviceSeenUnderALinkFileAreNotItsDefaults(t *testing.T) {
	h := newOffloadHost()
	r := Realizer{Root: t.TempDir(), Run: h.run}
	// The device appeared while this file was in place: udev turned GRO off.
	writeFile(t, filepath.Join(r.Root, "run/systemd/network/50-andaru-wan.link"), wanLink+"GenericReceiveOffload=no\n")
	h.requested["generic-receive-offload"] = false
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
	if want := [][]string{{"wan", "generic-receive-offload", "off"}}; !reflect.DeepEqual(h.set, want) {
		t.Fatalf("set %v, want %v", h.set, want)
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

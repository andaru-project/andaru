package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// offloadHost fakes ip and ethtool for one Ethernet device whose features change as ethtool -K asks.
type offloadHost struct {
	requested map[string]bool
	fixed     map[string]bool
	set       [][]string
}

func (h *offloadHost) run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch {
	case name == "/usr/bin/ip":
		return []byte(`[{"ifindex":2,"ifname":"wan","link_type":"ether","mtu":1500,"operstate":"UP","flags":[],"address":"52:54:00:00:00:01","permaddr":"52:54:00:AA:00:01","addr_info":[]}]`), nil
	case strings.HasSuffix(name, "ethtool") && args[0] == "--json":
		entry := map[string]any{"ifname": args[2], "tx-checksumming": map[string]any{"active": true, "fixed": nil, "requested": nil}}
		for f, on := range h.requested {
			entry[f] = map[string]any{"active": on, "fixed": h.fixed[f], "requested": on}
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

func TestOffloadsFollowTheLinkFileAndReturnToTheDefaults(t *testing.T) {
	h := &offloadHost{
		requested: map[string]bool{"generic-receive-offload": true, "rx-vlan-offload": true, "large-receive-offload": false},
		fixed:     map[string]bool{"large-receive-offload": true},
	}
	r := Realizer{Root: t.TempDir(), Run: h.run}
	link := placed{path: "/run/systemd/network/50-andaru-wan.link", data: []byte(
		"[Match]\nPermanentMACAddress=52:54:00:aa:00:01\n\n[Link]\nName=wan\nGenericReceiveOffload=no\nLargeReceiveOffload=yes\nReceiveVLANCTAGHardwareAcceleration=yes\n")}

	r.applyOffloads(context.Background(), []placed{link})
	// LRO is fixed and VLAN acceleration already runs as declared: only GRO changes.
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
	if !strings.Contains(string(data), `"52:54:00:aa:00:01":{"generic-receive-offload":true,"rx-vlan-offload":true}`) {
		t.Fatalf("recorded defaults: %s", data)
	}

	// Nothing to change, nothing run.
	r.applyOffloads(context.Background(), nil)
	if len(h.set) != 2 {
		t.Fatalf("set %v, want no further call", h.set)
	}
}

package host

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestManagedOffloadsFoldsFeatures(t *testing.T) {
	data, err := os.ReadFile("testdata/ethtool-json-k.json")
	if err != nil {
		t.Fatal(err)
	}
	features, err := ParseFeatures(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := features["tx-checksumming"]; ok {
		t.Fatal("a group entry must not read as a feature")
	}
	got := ManagedOffloads(features)
	want := map[string]OffloadState{
		"receiveChecksum":  {Active: true},
		"transmitChecksum": {Active: true},
		// fcoe-crc and sctp are fixed off on a veth, ip-generic is not: the group is not fixed.
		"genericReceive":          {Active: true},
		"genericReceiveHardware":  {Fixed: true},
		"largeReceive":            {Fixed: true},
		"vlanReceiveAcceleration": {},
	}
	for key, state := range want {
		if got[key] != state {
			t.Errorf("%s = %+v, want %+v", key, got[key], state)
		}
	}
	if len(got) != len(Offloads) {
		t.Errorf("a veth reports every managed offload, got %d of %d", len(got), len(Offloads))
	}
}

func TestParseFeaturesRejectsBadInput(t *testing.T) {
	for _, body := range []string{`{}`, `[]`, `[{},{}]`, `nope`} {
		if _, err := ParseFeatures([]byte(body)); err == nil {
			t.Errorf("%s: expected an error", body)
		}
	}
}

func TestReadOffloadsOnlyEthernetDevices(t *testing.T) {
	inventory := []byte(`[
		{"ifindex":1,"ifname":"lo","link_type":"loopback","mtu":65536,"operstate":"UNKNOWN","flags":[],"address":"00:00:00:00:00:00","addr_info":[]},
		{"ifindex":2,"ifname":"wan","link_type":"ether","mtu":1500,"operstate":"UP","flags":[],"address":"52:54:00:00:00:01","addr_info":[]},
		{"ifindex":3,"ifname":"wan.10","link_type":"ether","mtu":1500,"operstate":"UP","flags":[],"address":"52:54:00:00:00:01","linkinfo":{"info_kind":"vlan"},"addr_info":[]}
	]`)
	features, err := os.ReadFile("testdata/ethtool-json-k.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wan"}
	var asked []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == Ethtool {
			asked = append(asked, args[len(args)-1])
			return features, nil
		}
		return inventory, nil
	}
	got, err := readOffloads(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(asked, want) || len(got) != len(want) || got[0].Interface != want[0] || got[0].PermanentAddress == "" {
		t.Fatalf("asked %v, got %+v; want %v", asked, got, want)
	}

	two := []byte(strings.Replace(string(inventory), `"ifname":"wan.10","link_type":"ether","mtu":1500,"operstate":"UP","flags":[],"address":"52:54:00:00:00:01","linkinfo":{"info_kind":"vlan"}`, `"ifname":"lan","link_type":"ether","mtu":1500,"operstate":"UP","flags":[],"address":"52:54:00:00:00:02"`, 1))
	gone := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == Ethtool {
			if args[len(args)-1] == "wan" {
				return nil, errors.New("no such device")
			}
			return features, nil
		}
		return two, nil
	}
	// A device that went away is left out; the others still show.
	if got, err := readOffloads(context.Background(), gone); err != nil || len(got) != 1 || got[0].Interface != "lan" {
		t.Fatalf("got %+v, %v; want lan alone", got, err)
	}
	failing := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == Ethtool {
			return nil, errors.New("no ethtool")
		}
		return inventory, nil
	}
	if _, err := readOffloads(context.Background(), failing); err == nil {
		t.Fatal("no device read must fail the read")
	}
}

package host

import (
	"slices"
	"testing"
)

func TestParseLLDPNeighbors(t *testing.T) {
	// Empty neighbor table returns an empty non-nil slice.
	empty, err := ParseLLDPNeighbors([]byte(`{"Neighbors":[]}`))
	if err != nil {
		t.Fatalf("empty table: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", empty)
	}

	// Multiple interfaces and neighbors, capability bitmask decoding, raw MAC fallback, and deterministic sorting.
	sample := []byte(`{
		"Neighbors": [
			{
				"InterfaceIndex": 3,
				"InterfaceName": "enp2s0",
				"Neighbors": [
					{
						"ChassisID": "",
						"RawChassisID": [4, 170, 187, 204, 17, 34, 51],
						"PortID": "",
						"RawPortID": [3, 170, 187, 204, 17, 34, 52],
						"PortDescription": "Uplink SFP+",
						"SystemName": "core-sw-1",
						"SystemDescription": "Managed Switch",
						"EnabledCapabilities": 20
					}
				]
			},
			{
				"InterfaceIndex": 2,
				"InterfaceName": "enp1s0",
				"Neighbors": [
					{
						"ChassisID": "52:54:00:aa:bb:10",
						"PortID": "eth0/1",
						"PortDescription": "Port 1",
						"SystemName": "access-ap-2",
						"EnabledCapabilities": 12
					},
					{
						"ChassisID": "52:54:00:aa:bb:05",
						"PortID": "gi1/0/24",
						"SystemName": "access-ap-1"
					}
				]
			}
		]
	}`)
	got, err := ParseLLDPNeighbors(sample)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d neighbors, want 3: %+v", len(got), got)
	}
	if got[0].IfIndex != 2 || got[0].Interface != "enp1s0" || got[0].SystemName != "access-ap-1" || got[0].Capabilities == nil || len(got[0].Capabilities) != 0 {
		t.Fatalf("neighbor 0 mismatch: %+v", got[0])
	}
	if got[1].SystemName != "access-ap-2" || !slices.Equal(got[1].Capabilities, []string{"bridge", "wlan-ap"}) {
		t.Fatalf("neighbor 1 mismatch: %+v", got[1])
	}
	if got[2].IfIndex != 3 || got[2].ChassisID != "aa:bb:cc:11:22:33" || got[2].PortID != "aa:bb:cc:11:22:34" || !slices.Equal(got[2].Capabilities, []string{"bridge", "router"}) {
		t.Fatalf("neighbor 2 mismatch: %+v", got[2])
	}
}

func TestParseLLDPNeighborsRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"invalid utf8", []byte{0xff, 0xfe}},
		{"invalid json", []byte(`{bad}`)},
		{"missing Neighbors", []byte(`{}`)},
		{"zero ifindex", []byte(`{"Neighbors":[{"InterfaceIndex":0,"InterfaceName":"eth0","Neighbors":[]}]}`)},
		{"empty name", []byte(`{"Neighbors":[{"InterfaceIndex":2,"InterfaceName":"","Neighbors":[]}]}`)},
		{"bad raw byte", []byte(`{"Neighbors":[{"InterfaceIndex":2,"InterfaceName":"eth0","Neighbors":[{"RawChassisID":[256]}]}]}`)},
		{"bad capabilities", []byte(`{"Neighbors":[{"InterfaceIndex":2,"InterfaceName":"eth0","Neighbors":[{"EnabledCapabilities":70000}]}]}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLLDPNeighbors(tc.raw); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

package host

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

func TestParseLLDPNeighborsEmptyAndNull(t *testing.T) {
	for _, input := range []string{`{"Neighbors":[]}`, `{"Neighbors":null}`} {
		got, err := ParseLLDPNeighbors([]byte(input))
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("%s: want non-nil empty slice, got %#v", input, got)
		}
	}
}

func TestParseLLDPNeighborsPopulatedSanitizedAndFallbacks(t *testing.T) {
	input := `{
  "Neighbors": [
    {
      "InterfaceIndex": 3,
      "InterfaceName": "enp2s0",
      "Neighbors": [
        {
          "ChassisID": "00:11:22:33:44:55",
          "PortID": "Ethernet1/24",
          "PortDescription": "Uplink\tto\nCore\u001b[31m\u009b2J\u202e",
          "SystemName": "  core-sw-01\u007f  ",
          "SystemDescription": "SwitchOS\r\n24.1",
          "EnabledCapabilities": 20
        },
        {
          "ChassisID": "   \t\n ",
          "RawChassisID": [4, 170, 187, 204, 221, 238, 255],
          "PortID": "",
          "RawPortID": [3, 1, 2, 3, 4, 5, 6],
          "SystemName": "ap-office",
          "EnabledCapabilities": 8
        },
        {
          "ChassisID": "",
          "RawChassisID": [5, 1, 192, 0, 2, 10],
          "PortID": "",
          "RawPortID": [7, 170, 187, 204, 221, 238],
          "SystemName": "mgmt-router",
          "EnabledCapabilities": 0
        },
        {
          "ChassisID": "   ",
          "RawChassisID": [4],
          "PortID": "Gi0/1",
          "SystemName": "incomplete-ignored"
        }
      ]
    },
    {
      "InterfaceIndex": 2,
      "InterfaceName": "enp1s0",
      "Neighbors": [
        {
          "ChassisID": "",
          "RawChassisID": [5, 2, 32, 1, 13, 184, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1],
          "PortID": "eth0",
          "SystemName": "edge-gw"
        }
      ]
    }
  ]
}`
	got, err := ParseLLDPNeighbors([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 neighbors (incomplete skipped), got %d: %+v", len(got), got)
	}
	// Sorted by IfIndex first: enp1s0 (2) before enp2s0 (3); IPv6 network address raw fallback.
	if got[0].IfIndex != 2 || got[0].Interface != "enp1s0" || got[0].ChassisID != "2001:db8::1" || got[0].Capabilities == nil || len(got[0].Capabilities) != 0 {
		t.Fatalf("unexpected neighbor 0: %+v", got[0])
	}
	// On enp2s0, ap-office sorts before core-sw-01 before mgmt-router.
	if got[1].SystemName != "ap-office" || got[1].ChassisID != "aa:bb:cc:dd:ee:ff" || got[1].PortID != "01:02:03:04:05:06" || !slices.Equal(got[1].Capabilities, []string{"wlan-ap"}) {
		t.Fatalf("unexpected neighbor 1: %+v", got[1])
	}
	if got[2].SystemName != "core-sw-01" || got[2].PortDescription != "Uplink to Core[31m2J" || got[2].SystemDescription != "SwitchOS 24.1" || !slices.Equal(got[2].Capabilities, []string{"bridge", "router"}) {
		t.Fatalf("unexpected neighbor 2: %+v", got[2])
	}
	if got[3].SystemName != "mgmt-router" || got[3].ChassisID != "192.0.2.10" || got[3].PortID != "subtype-7:aa:bb:cc:dd:ee" || got[3].Capabilities == nil || len(got[3].Capabilities) != 0 {
		t.Fatalf("unexpected neighbor 3: %+v", got[3])
	}
}

func TestReadLLDPNeighborsWhenNetworkdStopped(t *testing.T) {
	inactiveErr := exec.Command("sh", "-c", "exit 3").Run()
	got, err := readLLDPNeighbors(context.Background(), func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "/usr/bin/systemctl" {
			return nil, inactiveErr
		}
		t.Fatalf("networkctl must not run when networkd is stopped, got %s", name)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", got)
	}

	// Operational systemctl failures (exit 1) propagate as errors instead of masking as empty.
	opErr := exec.Command("sh", "-c", "exit 1").Run()
	if _, err := readLLDPNeighbors(context.Background(), func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, opErr
	}); err == nil {
		t.Fatal("expected operational systemctl exit 1 to propagate error")
	}
}

func TestParseLLDPNeighborsRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"invalid utf8", []byte{0xff, 0xfe}},
		{"invalid json", []byte("{")},
		{"missing neighbors", []byte(`{}`)},
		{"bad interface metadata", []byte(`{"Neighbors":[{"InterfaceIndex":0,"InterfaceName":"eth0","Neighbors":[]}]}`)},
		{"bad raw byte", []byte(`{"Neighbors":[{"InterfaceIndex":2,"InterfaceName":"eth0","Neighbors":[{"RawChassisID":[256],"PortID":"1"}]}]}`)},
		{"bad capabilities", []byte(`{"Neighbors":[{"InterfaceIndex":2,"InterfaceName":"eth0","Neighbors":[{"ChassisID":"a","PortID":"1","EnabledCapabilities":70000}]}]}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLLDPNeighbors(tc.body); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

package host

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// LLDPNeighbor is one neighbor discovered by systemd-networkd's LLDP listener on a host interface.
type LLDPNeighbor struct {
	IfIndex           int      `json:"ifIndex"`
	Interface         string   `json:"interface"`
	ChassisID         string   `json:"chassisId"`
	PortID            string   `json:"portId"`
	PortDescription   string   `json:"portDescription,omitempty"`
	SystemName        string   `json:"systemName,omitempty"`
	SystemDescription string   `json:"systemDescription,omitempty"`
	Capabilities      []string `json:"capabilities"`
}

var lldpArgs = []string{"lldp", "--json=short"}

// ReadLLDPNeighbors queries systemd-networkd's LLDP neighbor table via networkctl.
func ReadLLDPNeighbors(ctx context.Context) ([]LLDPNeighbor, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	data, err := readCommand(ctx, "/usr/bin/networkctl", lldpArgs...)
	if err != nil {
		return nil, err
	}
	return ParseLLDPNeighbors(data)
}

type toolLLDPNeighbor struct {
	ChassisID           string `json:"ChassisID"`
	RawChassisID        []int  `json:"RawChassisID"`
	PortID              string `json:"PortID"`
	RawPortID           []int  `json:"RawPortID"`
	PortDescription     string `json:"PortDescription"`
	SystemName          string `json:"SystemName"`
	SystemDescription   string `json:"SystemDescription"`
	EnabledCapabilities *int   `json:"EnabledCapabilities"`
}

type toolLLDPInterface struct {
	InterfaceIndex int                `json:"InterfaceIndex"`
	InterfaceName  string             `json:"InterfaceName"`
	Neighbors      []toolLLDPNeighbor `json:"Neighbors"`
}

type toolLLDPOutput struct {
	Neighbors []toolLLDPInterface `json:"Neighbors"`
}

// IEEE 802.1AB System Capabilities TLV bit order (bits 0..10).
var lldpCapabilityNames = []string{
	"other",
	"repeater",
	"bridge",
	"wlan-ap",
	"router",
	"telephone",
	"docsis",
	"station",
	"customer-vlan",
	"service-vlan",
	"tpmr",
}

func decodeLLDPCapabilities(mask int) []string {
	out := []string{}
	for bit, name := range lldpCapabilityNames {
		if mask&(1<<bit) != 0 {
			out = append(out, name)
		}
	}
	return out
}

// formatRawLLDPID renders raw TLV bytes when networkctl omits the textual ChassisID or PortID.
// When the TLV subtype byte matches a 6-byte MAC address (chassis subtype 4, port subtype 3),
// it formats the trailing 6 bytes as colon-separated lowercase hex.
func formatRawLLDPID(raw []int, macSubtype int) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	bytes := make([]byte, len(raw))
	for i, v := range raw {
		if v < 0 || v > 255 {
			return "", fmt.Errorf("invalid raw LLDP identifier byte %d", v)
		}
		bytes[i] = byte(v)
	}
	if len(bytes) == 7 && int(bytes[0]) == macSubtype {
		return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6]), nil
	}
	parts := make([]string, len(bytes))
	for i, b := range bytes {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":"), nil
}

// ParseLLDPNeighbors decodes `networkctl lldp --json=short`.
func ParseLLDPNeighbors(data []byte) ([]LLDPNeighbor, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("LLDP neighbor output contains invalid UTF-8")
	}
	var raw toolLLDPOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode LLDP neighbors: %w", err)
	}
	if raw.Neighbors == nil {
		return nil, errors.New("LLDP neighbor output missing Neighbors array")
	}
	out := []LLDPNeighbor{}
	for i, iface := range raw.Neighbors {
		if iface.InterfaceIndex <= 0 || iface.InterfaceName == "" {
			return nil, fmt.Errorf("LLDP interface entry %d has invalid metadata", i)
		}
		for j, n := range iface.Neighbors {
			chassis := n.ChassisID
			if chassis == "" {
				var err error
				chassis, err = formatRawLLDPID(n.RawChassisID, 4)
				if err != nil {
					return nil, fmt.Errorf("LLDP neighbor %d on %s: %w", j, iface.InterfaceName, err)
				}
			}
			port := n.PortID
			if port == "" {
				var err error
				port, err = formatRawLLDPID(n.RawPortID, 3)
				if err != nil {
					return nil, fmt.Errorf("LLDP neighbor %d on %s: %w", j, iface.InterfaceName, err)
				}
			}
			caps := []string{}
			if n.EnabledCapabilities != nil {
				if *n.EnabledCapabilities < 0 || *n.EnabledCapabilities > 0xffff {
					return nil, fmt.Errorf("LLDP neighbor %d on %s has invalid capabilities %d", j, iface.InterfaceName, *n.EnabledCapabilities)
				}
				caps = decodeLLDPCapabilities(*n.EnabledCapabilities)
			}
			out = append(out, LLDPNeighbor{
				IfIndex:           iface.InterfaceIndex,
				Interface:         iface.InterfaceName,
				ChassisID:         chassis,
				PortID:            port,
				PortDescription:   n.PortDescription,
				SystemName:        n.SystemName,
				SystemDescription: n.SystemDescription,
				Capabilities:      caps,
			})
		}
	}
	slices.SortFunc(out, func(a, b LLDPNeighbor) int {
		return cmp.Or(
			cmp.Compare(a.IfIndex, b.IfIndex),
			cmp.Compare(a.Interface, b.Interface),
			cmp.Compare(a.SystemName, b.SystemName),
			cmp.Compare(a.ChassisID, b.ChassisID),
			cmp.Compare(a.PortID, b.PortID),
			cmp.Compare(a.PortDescription, b.PortDescription),
		)
	})
	return out, nil
}

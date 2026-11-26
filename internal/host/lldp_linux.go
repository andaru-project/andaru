package host

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"unicode"
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

// ReadLLDPNeighbors queries systemd-networkd's LLDP neighbor table via networkctl;
// returns an empty slice when systemd-networkd is stopped.
func ReadLLDPNeighbors(ctx context.Context) ([]LLDPNeighbor, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return readLLDPNeighbors(ctx, readCommand)
}

func readLLDPNeighbors(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error)) ([]LLDPNeighbor, error) {
	// networkctl fails outright while networkd is stopped.
	if _, err := run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "systemd-networkd.service"); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return []LLDPNeighbor{}, nil
		}
		return nil, err
	}
	data, err := run(ctx, "/usr/bin/networkctl", lldpArgs...)
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

// sanitizeLLDPText strips terminal/control and Unicode bidi/format characters from untrusted L2 peer TLVs
// and collapses line breaks and repeated whitespace to single spaces.
func sanitizeLLDPText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func formatColonHex(raw []int) string {
	parts := make([]string, len(raw))
	for i, v := range raw {
		parts[i] = fmt.Sprintf("%02x", v)
	}
	return strings.Join(parts, ":")
}

// formatRawLLDPID decodes raw IEEE 802.1AB ChassisID / PortID TLV bytes (leading subtype byte + payload)
// when networkctl omits the textual ID.
func formatRawLLDPID(raw []int, macSubtype, addrSubtype int) (string, error) {
	for _, v := range raw {
		if v < 0 || v > 255 {
			return "", fmt.Errorf("invalid raw LLDP identifier byte %d", v)
		}
	}
	if len(raw) < 2 {
		return "", nil
	}
	subtype, payload := raw[0], raw[1:]
	if subtype == macSubtype && len(payload) == 6 {
		return formatColonHex(payload), nil
	}
	if subtype == addrSubtype && len(payload) >= 2 {
		family, addrRaw := payload[0], payload[1:]
		if (family == 1 && len(addrRaw) == 4) || (family == 2 && len(addrRaw) == 16) {
			addrBytes := make([]byte, len(addrRaw))
			for i, v := range addrRaw {
				addrBytes[i] = byte(v)
			}
			if addr, ok := netip.AddrFromSlice(addrBytes); ok {
				return addr.String(), nil
			}
		}
	}
	return fmt.Sprintf("subtype-%d:%s", subtype, formatColonHex(payload)), nil
}

// ParseLLDPNeighbors decodes `networkctl lldp --json=short`.
func ParseLLDPNeighbors(data []byte) ([]LLDPNeighbor, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("LLDP neighbor output contains invalid UTF-8")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("decode LLDP neighbors: %w", err)
	}
	rawNeighbors, ok := top["Neighbors"]
	if !ok {
		return nil, errors.New("LLDP neighbor output missing Neighbors array")
	}
	out := []LLDPNeighbor{}
	if bytes.Equal(bytes.TrimSpace(rawNeighbors), []byte("null")) {
		return out, nil
	}
	var ifaces []toolLLDPInterface
	if err := json.Unmarshal(rawNeighbors, &ifaces); err != nil {
		return nil, fmt.Errorf("decode LLDP neighbors: %w", err)
	}
	for i, iface := range ifaces {
		if iface.InterfaceIndex <= 0 || iface.InterfaceName == "" {
			return nil, fmt.Errorf("LLDP interface entry %d has invalid metadata", i)
		}
		for j, n := range iface.Neighbors {
			chassis := sanitizeLLDPText(n.ChassisID)
			if chassis == "" {
				var err error
				chassis, err = formatRawLLDPID(n.RawChassisID, 4, 5)
				if err != nil {
					return nil, fmt.Errorf("LLDP neighbor %d on %s: %w", j, iface.InterfaceName, err)
				}
			}
			port := sanitizeLLDPText(n.PortID)
			if port == "" {
				var err error
				port, err = formatRawLLDPID(n.RawPortID, 3, 4)
				if err != nil {
					return nil, fmt.Errorf("LLDP neighbor %d on %s: %w", j, iface.InterfaceName, err)
				}
			}
			if chassis == "" || port == "" {
				continue
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
				PortDescription:   sanitizeLLDPText(n.PortDescription),
				SystemName:        sanitizeLLDPText(n.SystemName),
				SystemDescription: sanitizeLLDPText(n.SystemDescription),
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
			cmp.Compare(a.SystemDescription, b.SystemDescription),
			slices.Compare(a.Capabilities, b.Capabilities),
		)
	})
	return out, nil
}

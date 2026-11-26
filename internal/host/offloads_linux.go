package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Ethtool is the tool that reads and sets a device's features.
const Ethtool = "/usr/sbin/ethtool"

// Offload is one feature Andaru manages: its saved key, its systemd.link [Link] key and the kernel features it sets.
type Offload struct {
	Key, Networkd string
	Features      []string
}

// Offloads is every offload a saved Ethernet link may turn on or off, in rendering order.
var Offloads = []Offload{
	{"receiveChecksum", "ReceiveChecksumOffload", []string{"rx-checksumming"}},
	// systemd sets every transmit checksum feature; ethtool reports them one by one.
	{"transmitChecksum", "TransmitChecksumOffload", []string{"tx-checksum-ipv4", "tx-checksum-ip-generic", "tx-checksum-ipv6", "tx-checksum-fcoe-crc", "tx-checksum-sctp"}},
	{"tcpSegmentation", "TCPSegmentationOffload", []string{"tx-tcp-segmentation"}},
	{"tcp6Segmentation", "TCP6SegmentationOffload", []string{"tx-tcp6-segmentation"}},
	{"genericSegmentation", "GenericSegmentationOffload", []string{"generic-segmentation-offload"}},
	{"genericReceive", "GenericReceiveOffload", []string{"generic-receive-offload"}},
	{"genericReceiveHardware", "GenericReceiveOffloadHardware", []string{"rx-gro-hw"}},
	{"largeReceive", "LargeReceiveOffload", []string{"large-receive-offload"}},
	{"vlanReceiveAcceleration", "ReceiveVLANCTAGHardwareAcceleration", []string{"rx-vlan-offload"}},
	{"vlanTransmitAcceleration", "TransmitVLANCTAGHardwareAcceleration", []string{"tx-vlan-offload"}},
	{"vlanReceiveFilter", "ReceiveVLANCTAGFilter", []string{"rx-vlan-filter"}},
	{"ntupleFilter", "NTupleFilter", []string{"ntuple-filters"}},
}

// Feature is one kernel feature as ethtool reports it. Requested is what was asked for; the kernel derives Active
// from it and the features it depends on, so Requested is what restores a device.
type Feature struct {
	Active, Requested, Fixed bool
}

// ParseFeatures decodes `ethtool --json -k <device>`. Group entries, whose fields are null, are left out.
func ParseFeatures(data []byte) (map[string]Feature, error) {
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode device features: %w", err)
	}
	if len(raw) != 1 {
		return nil, errors.New("device features must describe one device")
	}
	out := map[string]Feature{}
	for name, value := range raw[0] {
		var f struct {
			Active, Requested, Fixed *bool
		}
		if name == "ifname" || json.Unmarshal(value, &f) != nil || f.Active == nil || f.Requested == nil || f.Fixed == nil {
			continue
		}
		out[name] = Feature{Active: *f.Active, Requested: *f.Requested, Fixed: *f.Fixed}
	}
	return out, nil
}

// OffloadState is one managed offload as a device runs it. Fixed: the device cannot change it.
type OffloadState struct {
	Active bool `json:"active"`
	Fixed  bool `json:"fixed"`
}

// DeviceOffloads is the live offload state of one Ethernet device.
type DeviceOffloads struct {
	Interface        string                  `json:"interface"`
	PermanentAddress string                  `json:"permanentAddress"`
	Offloads         map[string]OffloadState `json:"offloads"`
}

// Physical reports whether a link is an Ethernet device rather than one the kernel or Andaru made.
func Physical(l LinkStatus) bool {
	return l.LinkType == "ether" && l.Kind == "" && l.PermanentAddress != ""
}

// ManagedOffloads folds a device's features into the managed offloads it reports: active when any of its features is,
// fixed when all are. An offload the device does not report at all is left out.
func ManagedOffloads(features map[string]Feature) map[string]OffloadState {
	out := map[string]OffloadState{}
	for _, o := range Offloads {
		state, seen := OffloadState{Fixed: true}, false
		for _, name := range o.Features {
			if f, ok := features[name]; ok {
				seen = true
				state.Active = state.Active || f.Active
				state.Fixed = state.Fixed && f.Fixed
			}
		}
		if seen {
			out[o.Key] = state
		}
	}
	return out
}

// ReadOffloads reads the managed offloads of every Ethernet device.
func ReadOffloads(ctx context.Context) ([]DeviceOffloads, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return readOffloads(ctx, readCommand)
}

func readOffloads(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error)) ([]DeviceOffloads, error) {
	data, err := run(ctx, "/usr/bin/ip", linkInventoryArgs...)
	if err != nil {
		return nil, err
	}
	links, err := ParseLinks(data)
	if err != nil {
		return nil, err
	}
	out := []DeviceOffloads{}
	for _, l := range slices.DeleteFunc(links, func(l LinkStatus) bool { return !Physical(l) }) {
		data, err := run(ctx, Ethtool, "--json", "-k", l.Name)
		if err != nil {
			return nil, fmt.Errorf("read the features of %s: %w", l.Name, err)
		}
		features, err := ParseFeatures(data)
		if err != nil {
			return nil, err
		}
		out = append(out, DeviceOffloads{Interface: l.Name, PermanentAddress: l.PermanentAddress, Offloads: ManagedOffloads(features)})
	}
	return out, nil
}

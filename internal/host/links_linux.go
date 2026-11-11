package host

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"slices"
	"time"
	"unicode/utf8"
)

const (
	commandTimeout = 2 * time.Second
	stdoutLimit    = 4 * 1024 * 1024
	stderrLimit    = 4 * 1024
)

var errOutputLimit = errors.New("interface inventory exceeds output limit")

// AddressStatus is an observed host address, not a network-masked prefix.
type AddressStatus struct {
	Family       string `json:"family"`
	Address      string `json:"address"`
	PrefixLength int    `json:"prefixLength"`
	Scope        string `json:"scope"`
}

// LinkStatus contains runtime observations, not persistent configuration identity.
type LinkStatus struct {
	IfIndex     int             `json:"ifIndex"`
	Name        string          `json:"name"`
	LinkType    string          `json:"linkType"`
	MTU         int             `json:"mtu"`
	OperState   string          `json:"operState"`
	Flags       []string        `json:"flags"`
	LinkAddress string          `json:"linkAddress,omitempty"`
	Addresses   []AddressStatus `json:"addresses"`
	// Kind is the kernel link kind (vlan, bridge, ...); empty for physical devices.
	Kind string `json:"-"`
	// PermanentAddress is the hardware address a device cannot change; virtual links inherit theirs.
	PermanentAddress string `json:"-"`
}

// -d adds linkinfo: without it a VLAN is indistinguishable from the NIC whose MAC it inherits.
var linkInventoryArgs = []string{"-d", "-j", "address", "show"}

// ReadLinks reads the host namespace using Debian's read-only iproute2 interface.
func ReadLinks(ctx context.Context) ([]LinkStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	data, err := readCommand(ctx, "/usr/bin/ip", linkInventoryArgs...)
	if err != nil {
		return nil, err
	}
	return ParseLinks(data)
}

type toolAddress struct {
	Family       string `json:"family"`
	Local        string `json:"local"`
	PrefixLength *int   `json:"prefixlen"`
	Scope        string `json:"scope"`
}

type toolLink struct {
	IfIndex     int      `json:"ifindex"`
	Name        string   `json:"ifname"`
	LinkType    string   `json:"link_type"`
	MTU         *int     `json:"mtu"`
	OperState   string   `json:"operstate"`
	Flags       []string `json:"flags"`
	LinkAddress string   `json:"address"`
	Permanent   string   `json:"permaddr"`
	LinkInfo    struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
	Addresses []toolAddress `json:"addr_info"`
}

// ParseLinks decodes `ip -d -j address show`.
func ParseLinks(data []byte) ([]LinkStatus, error) {
	// encoding/json otherwise replaces invalid UTF-8, inventing a different name.
	if !utf8.Valid(data) {
		return nil, errors.New("interface inventory contains invalid UTF-8")
	}
	var raw []toolLink
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode interface inventory: %w", err)
	}
	if raw == nil {
		return nil, errors.New("interface inventory must be an array")
	}
	links := make([]LinkStatus, len(raw))
	indexes := make(map[int]struct{}, len(raw))
	names := make(map[string]struct{}, len(raw))
	for i, value := range raw {
		if value.IfIndex <= 0 || value.Name == "" || value.LinkType == "" || value.OperState == "" || value.MTU == nil || *value.MTU < 0 {
			return nil, fmt.Errorf("interface inventory entry %d has invalid link metadata", i)
		}
		if _, found := indexes[value.IfIndex]; found {
			return nil, errors.New("interface inventory contains duplicate indexes")
		}
		if _, found := names[value.Name]; found {
			return nil, errors.New("interface inventory contains duplicate names")
		}
		indexes[value.IfIndex] = struct{}{}
		names[value.Name] = struct{}{}
		if value.Flags == nil {
			value.Flags = []string{}
		}
		link := LinkStatus{
			IfIndex: value.IfIndex, Name: value.Name, LinkType: value.LinkType,
			MTU: *value.MTU, OperState: value.OperState, Flags: value.Flags,
			LinkAddress: value.LinkAddress, Addresses: make([]AddressStatus, len(value.Addresses)),
			Kind: value.LinkInfo.Kind, PermanentAddress: cmp.Or(value.Permanent, value.LinkAddress),
		}
		for j, record := range value.Addresses {
			address, err := netip.ParseAddr(record.Local)
			if err != nil || record.PrefixLength == nil || record.Scope == "" {
				return nil, fmt.Errorf("interface inventory entry %d has an invalid address", i)
			}
			family, bits := "", 0
			switch record.Family {
			case "inet":
				if !address.Is4() {
					return nil, fmt.Errorf("interface inventory entry %d has a mismatched IPv4 address", i)
				}
				family, bits = "ipv4", 32
			case "inet6":
				if !address.Is6() {
					return nil, fmt.Errorf("interface inventory entry %d has a mismatched IPv6 address", i)
				}
				family, bits = "ipv6", 128
			default:
				return nil, fmt.Errorf("interface inventory entry %d has an unsupported address family", i)
			}
			if *record.PrefixLength < 0 || *record.PrefixLength > bits {
				return nil, fmt.Errorf("interface inventory entry %d has an invalid prefix length", i)
			}
			link.Addresses[j] = AddressStatus{Family: family, Address: address.String(), PrefixLength: *record.PrefixLength, Scope: record.Scope}
		}
		links[i] = link
	}
	slices.SortFunc(links, func(a, b LinkStatus) int { return cmp.Compare(a.IfIndex, b.IfIndex) })
	return links, nil
}

type boundedStderr struct{ data []byte }

func (b *boundedStderr) Write(p []byte) (int, error) {
	if remaining := stderrLimit - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return len(p), nil
}

func readCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	var stderr boundedStderr
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open interface inventory output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("start interface inventory: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, stdoutLimit+1))
	if readErr != nil || len(data) > stdoutLimit {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if len(data) > stdoutLimit {
		return nil, errOutputLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read interface inventory: %w", err)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read interface inventory output: %w", readErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("interface inventory command: %w: %s", waitErr, stderr.data)
	}
	if len(stderr.data) != 0 {
		return nil, fmt.Errorf("interface inventory command warning: %s", stderr.data)
	}
	return data, nil
}

// Package delegation places subnets and pools inside a prefix the WAN's DHCPv6 client receives, and holds what
// the root prefix sync saw of it.
package delegation

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"time"
)

// Place returns the prefix of length at position id under base: false when it is shorter than base, longer
// than 64, or id needs more than the length − base.Bits() bits between them.
func Place(base netip.Prefix, id uint64, length int) (netip.Prefix, bool) {
	free := length - base.Bits()
	if free < 0 || length > 64 || (free < 64 && id>>free != 0) {
		return netip.Prefix{}, false
	}
	a := base.Masked().Addr().As16()
	high := uint64(a[0])<<56 | uint64(a[1])<<48 | uint64(a[2])<<40 | uint64(a[3])<<32 | uint64(a[4])<<24 | uint64(a[5])<<16 | uint64(a[6])<<8 | uint64(a[7])
	if free > 0 {
		high |= id << (64 - length)
	}
	for i := range 8 {
		a[i] = byte(high >> (56 - 8*i))
	}
	return netip.PrefixFrom(netip.AddrFrom16(a), length), true
}

// Join puts the upper 64 bits of p in front of the lower 64 bits of suffix.
func Join(p netip.Prefix, suffix netip.Addr) netip.Addr {
	a, s := p.Masked().Addr().As16(), suffix.As16()
	copy(a[8:], s[8:])
	return netip.AddrFrom16(a)
}

// Held is a delegated prefix and when it stops being preferred and valid.
type Held struct {
	Prefix    netip.Prefix `json:"prefix"`
	Preferred time.Time    `json:"preferred"`
	Valid     time.Time    `json:"valid"`
}

// WAN is what the sync saw on one WAN interface: the length it requested, its current prefix, and the prefixes
// it replaced that are still valid, whose leases Kea holds until their routers renew.
type WAN struct {
	Requested int    `json:"requested"`
	Current   *Held  `json:"current,omitempty"`
	Replaced  []Held `json:"replaced,omitempty"`
}

// State is /var/lib/andaru/delegated-prefixes.json, keyed by WAN interface name.
type State struct {
	WANs map[string]WAN `json:"wans"`
}

// ReadState reads the state file; a missing file is an empty state.
func ReadState(path string) (State, error) {
	s := State{WANs: map[string]WAN{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{WANs: map[string]WAN{}}, err
	}
	if s.WANs == nil {
		s.WANs = map[string]WAN{}
	}
	return s, nil
}

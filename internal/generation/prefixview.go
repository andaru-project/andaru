package generation

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/andaru-project/andaru/internal/delegation"
)

// PrefixView is what each DHCPv6 WAN holds and where the live generation's delegated segments and pools sit in it.
type PrefixView struct {
	WANs []WANView `json:"wans"`
}

type WANView struct {
	Interface string      `json:"interface"`
	Requested int         `json:"requested"`
	Current   *HeldView   `json:"current"`
	SubnetIDs IDRange     `json:"subnetIds"`
	Segments  []PlaceView `json:"segments"`
	Pools     []PlaceView `json:"pools"`
	// NotFitting names what the current prefix has no room for.
	NotFitting []string `json:"notFitting"`
}

type HeldView struct {
	Prefix         string    `json:"prefix"`
	PreferredUntil time.Time `json:"preferredUntil"`
	ValidUntil     time.Time `json:"validUntil"`
}

type IDRange struct {
	First string `json:"first"`
	Last  string `json:"last"`
}

type PlaceView struct {
	Interface string  `json:"interface"`
	SubnetID  string  `json:"subnetId"`
	Prefix    *string `json:"prefix"`
}

// ReadPrefixView reads the saved prefixes and the live generation's files under root; it needs no network access.
func ReadPrefixView(root string) (PrefixView, error) {
	r := Realizer{Root: root}
	links, hints, _ := r.readDelegatedLinks()
	state, err := delegation.ReadState(root + delegatedState)
	if err != nil {
		return PrefixView{}, err
	}
	var tpl delegation.Template
	if raw, err := os.ReadFile(root + kea6Template); err == nil {
		_ = json.Unmarshal(raw, &tpl)
	}
	uplinks := maps.Clone(hints)
	for _, l := range links {
		uplinks[l.Uplink] = hints[l.Uplink]
	}
	view := PrefixView{WANs: []WANView{}}
	for _, up := range slices.Sorted(maps.Keys(uplinks)) {
		w := WANView{Interface: up, Requested: hints[up], Segments: []PlaceView{}, Pools: []PlaceView{}, NotFitting: []string{}}
		length := cmp.Or(hints[up], 64)
		var base netip.Prefix
		if c := state.WANs[up].Current; c != nil {
			w.Current = &HeldView{c.Prefix.String(), c.Preferred, c.Valid}
			base, length = c.Prefix, c.Prefix.Bits()
		}
		bits := min(64-length, 16)
		w.SubnetIDs = IDRange{"0", strconv.FormatUint(uint64(1)<<bits-1, 16)}
		place := func(iface, id string, at int, what string) PlaceView {
			v := PlaceView{Interface: iface, SubnetID: id}
			if !base.IsValid() {
				return v
			}
			n, _ := strconv.ParseUint(id, 16, 16)
			if p, ok := delegation.Place(base, n, at); ok {
				s := p.String()
				v.Prefix = &s
			} else {
				w.NotFitting = append(w.NotFitting, fmt.Sprintf("the %s of %s", what, iface))
			}
			return v
		}
		for _, l := range links {
			if l.Uplink == up {
				w.Segments = append(w.Segments, place(l.Interface, strconv.FormatUint(l.SubnetID, 16), 64, "subnet"))
			}
		}
		for _, s := range tpl.Subnets {
			if s.Uplink == up && s.Pool != nil {
				w.Pools = append(w.Pools, place(s.Interface, s.Pool.SubnetID, s.Pool.Length, "delegation pool"))
			}
		}
		view.WANs = append(view.WANs, w)
	}
	return view, nil
}

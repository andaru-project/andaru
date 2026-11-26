package generation

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"
)

const leasesViewFile = leasesDir + "/leases.json"

// LeaseView is the snapshot of DHCPv4 and DHCPv6 leases Kea last reported.
type LeaseView struct {
	V4 []Lease4View `json:"v4"`
	V6 []Lease6View `json:"v6"`
}

type Lease4View struct {
	Segment   string     `json:"segment"`
	Address   string     `json:"address"`
	MAC       string     `json:"mac"`
	ClientID  string     `json:"clientId,omitempty"`
	Hostname  string     `json:"hostname"`
	State     string     `json:"state"`
	Cltt      *time.Time `json:"cltt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type Lease6View struct {
	Segment   string     `json:"segment"`
	Type      string     `json:"type"`
	Address   string     `json:"address"`
	DUID      string     `json:"duid"`
	MAC       string     `json:"mac,omitempty"`
	Hostname  string     `json:"hostname"`
	State     string     `json:"state"`
	Cltt      *time.Time `json:"cltt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

func keaLeaseState(state int) string {
	switch state {
	case 0:
		return "active"
	case 1:
		return "declined"
	case 2:
		return "expired"
	case 3:
		return "released"
	default:
		return "unknown"
	}
}

func leaseTimes(cltt int64, valid int) (*time.Time, *time.Time) {
	if cltt <= 0 {
		return nil, nil
	}
	t := time.Unix(cltt, 0).UTC()
	if valid > 0 && int64(valid) < 0xffffffff {
		exp := time.Unix(cltt+int64(valid), 0).UTC()
		return &t, &exp
	}
	return &t, nil
}

func compareAddrText(a, b string) int {
	pa, ea := netip.ParsePrefix(a)
	pb, eb := netip.ParsePrefix(b)
	if ea == nil && eb == nil {
		if c := pa.Addr().Compare(pb.Addr()); c != 0 {
			return c
		}
		return cmp.Compare(pa.Bits(), pb.Bits())
	}
	aa, ea := netip.ParseAddr(a)
	ab, eb := netip.ParseAddr(b)
	if ea == nil && eb == nil {
		return aa.Compare(ab)
	}
	return cmp.Compare(a, b)
}

func buildLease4Views(cfg keaConfig, leases []keaLease) []Lease4View {
	segments := map[int]string{}
	for _, s := range cfg.Dhcp4.Subnet4 {
		segments[s.ID] = s.UserContext.Andaru.Segment
	}
	out := make([]Lease4View, 0, len(leases))
	for _, l := range leases {
		cltt, exp := leaseTimes(l.Cltt, l.Valid)
		out = append(out, Lease4View{
			Segment:   segments[l.Subnet],
			Address:   l.Address,
			MAC:       strings.ToLower(l.MAC),
			ClientID:  l.ClientID,
			Hostname:  l.Hostname,
			State:     keaLeaseState(l.State),
			Cltt:      cltt,
			ExpiresAt: exp,
		})
	}
	slices.SortFunc(out, func(a, b Lease4View) int {
		return cmp.Or(cmp.Compare(a.Segment, b.Segment), compareAddrText(a.Address, b.Address), cmp.Compare(a.MAC, b.MAC))
	})
	return out
}

func buildLease6Views(cfg kea6Config, leases []keaLease) []Lease6View {
	segments := map[int]string{}
	for _, s := range cfg.Dhcp6.Subnet6 {
		segments[s.ID] = s.UserContext.Andaru.Segment
	}
	out := make([]Lease6View, 0, len(leases))
	for _, l := range leases {
		kind := "address"
		addr := l.Address
		if l.Type == "IA_PD" {
			kind = "prefix"
			if l.PrefixLen > 0 && !strings.Contains(addr, "/") {
				addr = fmt.Sprintf("%s/%d", addr, l.PrefixLen)
			}
		}
		cltt, exp := leaseTimes(l.Cltt, l.Valid)
		out = append(out, Lease6View{
			Segment:   segments[l.Subnet],
			Type:      kind,
			Address:   addr,
			DUID:      strings.ToLower(l.DUID),
			MAC:       strings.ToLower(l.MAC),
			Hostname:  l.Hostname,
			State:     keaLeaseState(l.State),
			Cltt:      cltt,
			ExpiresAt: exp,
		})
	}
	slices.SortFunc(out, func(a, b Lease6View) int {
		return cmp.Or(cmp.Compare(a.Segment, b.Segment), cmp.Compare(a.Type, b.Type), compareAddrText(a.Address, b.Address), cmp.Compare(a.DUID, b.DUID))
	})
	return out
}

func readStoredLeaseView(root string) (LeaseView, error) {
	view := LeaseView{V4: []Lease4View{}, V6: []Lease6View{}}
	raw, err := os.ReadFile(root + leasesViewFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return view, nil
	case err != nil:
		return LeaseView{}, err
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return LeaseView{}, fmt.Errorf("read the saved leases view: %w", err)
	}
	if view.V4 == nil {
		view.V4 = []Lease4View{}
	}
	if view.V6 == nil {
		view.V6 = []Lease6View{}
	}
	return view, nil
}

// ReadLeaseView reads the lease snapshot written by andaru realize leases; it needs no access to Kea's socket.
func ReadLeaseView(root string, now time.Time) (LeaseView, error) {
	view, err := readStoredLeaseView(root)
	if err != nil {
		return LeaseView{}, err
	}
	for i := range view.V4 {
		if view.V4[i].State == "active" && view.V4[i].ExpiresAt != nil && !view.V4[i].ExpiresAt.After(now) {
			view.V4[i].State = "expired"
		}
	}
	for i := range view.V6 {
		if view.V6[i].State == "active" && view.V6[i].ExpiresAt != nil && !view.V6[i].ExpiresAt.After(now) {
			view.V6[i].State = "expired"
		}
	}
	return view, nil
}

func writeLeaseView(root string, v4 []Lease4View, v6 []Lease6View) error {
	if v4 == nil {
		v4 = []Lease4View{}
	}
	if v6 == nil {
		v6 = []Lease6View{}
	}
	body, err := json.Marshal(LeaseView{V4: v4, V6: v6})
	if err != nil {
		return err
	}
	body = append(body, '\n')
	path := root + leasesViewFile
	if old, err := os.ReadFile(path); err == nil && string(old) == string(body) {
		return nil
	}
	if err := os.MkdirAll(root+leasesDir, 0o755); err != nil {
		return err
	}
	return writeAtomic(path, body, 0o644)
}

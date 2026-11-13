package delegation

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"time"
)

// Template is /run/andaru-kea/kea-dhcp6.template.json: the DHCPv6 subnets of delegated segments, written with
// suffixes and positions because their prefix is only known when the WAN receives it.
type Template struct {
	Subnets []TemplateSubnet `json:"subnets"`
}

// TemplateSubnet is one delegated segment's Kea subnet before its prefix is known.
type TemplateSubnet struct {
	ID           uint32                `json:"id"`
	Interface    string                `json:"interface"`
	Uplink       string                `json:"uplink"`
	SubnetID     string                `json:"subnetId"`
	Ranges       []TemplateRange       `json:"ranges"`
	Reservations []TemplateReservation `json:"reservations"`
	Pool         *TemplatePool         `json:"pool"`
	Valid        int                   `json:"valid-lifetime"`
	Preferred    int                   `json:"preferred-lifetime"`
	Options      []map[string]any      `json:"option-data"`
	UserContext  map[string]any        `json:"user-context"`
}

type TemplateRange struct {
	First string `json:"first"`
	Last  string `json:"last"`
}

type TemplateReservation struct {
	DUID     string `json:"duid"`
	Address  string `json:"address,omitempty"`
	PrefixID string `json:"prefixId,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// TemplatePool is a delegation pool at SubnetID of Length under the WAN's prefix.
type TemplatePool struct {
	SubnetID        string `json:"subnetId"`
	Length          int    `json:"length"`
	DelegatedLength int    `json:"delegatedLength"`
}

// FillTemplate turns the template into Kea subnet6 entries under each uplink's prefix. A subnet whose uplink
// has no prefix is left out silently; a subnet or pool the prefix has no room for is left out and named in skipped.
// No lease outlives the delegation it is numbered from.
func FillTemplate(raw []byte, prefixes map[string]Held, now time.Time) (subnets []map[string]any, skipped []string, err error) {
	var tpl Template
	if err := json.Unmarshal(raw, &tpl); err != nil {
		return nil, nil, fmt.Errorf("read the DHCPv6 template: %w", err)
	}
	subnets = []map[string]any{}
	for _, s := range tpl.Subnets {
		held, ok := prefixes[s.Uplink]
		if !ok {
			continue
		}
		base := held.Prefix
		valid := bound(s.Valid, held.Valid.Sub(now))
		preferred := min(bound(s.Preferred, held.Preferred.Sub(now)), valid)
		id, _ := strconv.ParseUint(s.SubnetID, 16, 16)
		lan, ok := Place(base, id, 64)
		if !ok {
			skipped = append(skipped, fmt.Sprintf("the subnet of %s (subnet ID %s does not fit %s)", s.Interface, s.SubnetID, base))
			continue
		}
		join := func(raw string) string {
			a, _ := netip.ParseAddr(raw)
			return Join(lan, a).String()
		}
		pools := []map[string]any{}
		for _, r := range s.Ranges {
			pools = append(pools, map[string]any{"pool": join(r.First) + " - " + join(r.Last)})
		}
		context := map[string]any{}
		for k, v := range s.UserContext {
			context[k] = v
		}
		andaru, _ := context["andaru"].(map[string]any)
		marked := map[string]any{"delegated": true}
		for k, v := range andaru {
			marked[k] = v
		}
		// The lease sync tells these subnets apart: their pool routes and static names are root's to publish.
		context["andaru"] = marked
		subnet := map[string]any{
			"id": s.ID, "subnet": lan.String(), "interface": s.Interface, "pools": pools,
			"valid-lifetime": valid, "preferred-lifetime": preferred, "option-data": s.Options, "user-context": context,
		}
		var pool netip.Prefix
		if p := s.Pool; p != nil {
			n, _ := strconv.ParseUint(p.SubnetID, 16, 16)
			if pool, ok = Place(base, n, p.Length); ok {
				subnet["pd-pools"] = []map[string]any{{"prefix": pool.Addr().String(), "prefix-len": pool.Bits(), "delegated-len": p.DelegatedLength}}
			} else {
				skipped = append(skipped, fmt.Sprintf("the delegation pool of %s (position %s/%d does not fit %s)", s.Interface, p.SubnetID, p.Length, base))
			}
		}
		reservations := []map[string]any{}
		for _, r := range s.Reservations {
			res := map[string]any{"duid": r.DUID}
			if r.Address != "" {
				res["ip-addresses"] = []string{join(r.Address)}
			}
			if r.PrefixID != "" && pool.IsValid() {
				n, _ := strconv.ParseUint(r.PrefixID, 16, 16)
				if fixed, ok := Place(pool, n, s.Pool.DelegatedLength); ok {
					res["prefixes"] = []string{fixed.String()}
				}
			}
			if r.Hostname != "" {
				res["hostname"] = r.Hostname
			}
			if len(res) > 1 && (res["ip-addresses"] != nil || res["prefixes"] != nil) {
				reservations = append(reservations, res)
			}
		}
		subnet["reservations"] = reservations
		subnets = append(subnets, subnet)
	}
	return subnets, skipped, nil
}

// bound is the lease lifetime limited to what remains of the delegation. The remainder is rounded down to ten
// minutes, so Kea's configuration changes at most that often while the delegation's lifetime runs down.
func bound(lease int, remaining time.Duration) int {
	left := int(max(remaining, 0) / time.Second)
	if left >= lease {
		return lease
	}
	if left >= 600 {
		return left - left%600
	}
	return left
}

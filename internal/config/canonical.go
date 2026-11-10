package config

import (
	"encoding/json"
	"encoding/json/jsontext"
	"slices"
	"strings"
)

func canonical(v any) ([]byte, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	value := jsontext.Value(raw)
	if e = value.Canonicalize(jsontext.CanonicalizeRawInts(false)); e != nil {
		return nil, e
	}
	if len(value) >= documentLimit {
		return nil, errTooLarge
	}
	return append(value, '\n'), nil
}
func normalizeDeclaration(d *declaration) {
	slices.SortFunc(d.Links, func(a, b link) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(d.Segments, func(a, b segment) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(d.Zones, func(a, b zone) int { return strings.Compare(a.ID, b.ID) })
	for i := range d.Links {
		if d.Links[i].Members != nil {
			slices.Sort(*d.Links[i].Members)
		}
	}
	for i := range d.Segments {
		slices.Sort(d.Segments[i].Addressing.IPv4.Addresses)
		slices.Sort(d.Segments[i].Addressing.IPv6.Addresses)
	}
}

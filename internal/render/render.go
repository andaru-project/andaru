package render

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
)

// Network renders the declaration for this host. It fails only when the
// declaration cannot be decoded; everything else is reported as a finding.
func Network(in Input) (Result, error) {
	r := &renderer{in: in, index: map[string]int{}, segment: map[string]int{}, master: map[string]string{},
		name: map[string]string{}, rendered: map[string]bool{}, usable: map[string]bool{}, visited: map[string]bool{},
		result: Result{Resolved: []Resolution{}, Files: []File{}, Findings: []Finding{}}}
	if err := json.Unmarshal(in.Declaration, &r.d); err != nil {
		return Result{}, fmt.Errorf("decode saved declaration: %w", err)
	}
	for i, l := range r.d.Links {
		r.index[l.ID] = i
		for _, member := range l.Members {
			r.master[member] = l.ID
		}
	}
	for i, s := range r.d.Segments {
		r.segment[s.Link] = i
	}
	r.assignNames()
	for _, l := range r.d.Links {
		r.visit(l.ID)
	}
	for i, s := range r.d.Segments {
		if !r.usable[s.Link] {
			r.find("warning", "DEPENDENCY_UNAVAILABLE", fmt.Sprintf("/segments/%d/link", i), "Not rendered: the segment's link is unavailable.")
		}
	}
	for i, l := range r.d.Links {
		if !r.rendered[l.ID] {
			continue
		}
		if l.Kind == "pppoe" {
			r.pppoe(i)
		} else {
			r.networkd(i)
		}
	}
	for _, path := range in.Facts.ForeignFiles {
		r.find("error", "FOREIGN_CONFIGURATION", "", "A network configuration file not written by Andaru would override or conflict with it: "+path)
	}
	slices.SortFunc(r.result.Files, func(a, b File) int { return cmp.Compare(a.Path, b.Path) })
	slices.SortFunc(r.result.Resolved, func(a, b Resolution) int { return cmp.Compare(a.Link, b.Link) })
	slices.SortFunc(r.result.Findings, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Code, b.Code), cmp.Compare(a.Message, b.Message))
	})
	r.result.Realizable = !slices.ContainsFunc(r.result.Findings, func(f Finding) bool { return f.Severity == "error" })
	return r.result, nil
}

// PPPoE is rendered by a later change; until then such links are left out.
func (r *renderer) visitPPPoE(i int, named bool) {}
func (r *renderer) pppoe(i int)                  {}

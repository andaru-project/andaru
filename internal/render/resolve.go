package render

import (
	"fmt"
	"slices"
	"strings"
)

type renderer struct {
	in       Input
	d        declaration
	index    map[string]int    // link ID -> position in d.Links
	segment  map[string]int    // link ID -> position in d.Segments
	master   map[string]string // member link ID -> bridge or bond ID
	name     map[string]string // link ID -> kernel or derived name, when known
	rendered map[string]bool   // link gets files
	usable   map[string]bool   // other objects may build on the link
	visited  map[string]bool
	result   Result
}

func (r *renderer) find(severity, code, path, message string) {
	r.result.Findings = append(r.result.Findings, Finding{severity, code, path, message})
}
func linkPath(i int, field string) string {
	if field == "" {
		return fmt.Sprintf("/links/%d", i)
	}
	return fmt.Sprintf("/links/%d/%s", i, field)
}

var kernelKind = map[string]string{"vlan": "vlan", "bridge": "bridge", "bond": "bond", "pppoe": "ppp"}
var namePrefix = map[string]string{"vlan": "vl", "bridge": "br", "bond": "bo", "pppoe": "pp"}

// derivedName is at most 14 bytes: user names may be 128 code points, kernel names 15 bytes.
func derivedName(l link) string {
	hex := strings.ReplaceAll(l.ID[strings.IndexByte(l.ID, '_')+1:], "-", "")
	return namePrefix[l.Kind] + hex[:min(12, len(hex))]
}

func (r *renderer) assignNames() {
	derived := map[string][]int{}
	for i, l := range r.d.Links {
		if l.Kind != "ethernet" {
			name := derivedName(l)
			derived[name] = append(derived[name], i)
			continue
		}
		var matches []string
		for _, observed := range r.in.Links {
			// A VLAN or bridge on a NIC reports the NIC's own MAC; only kindless Ethernet devices are hardware.
			if observed.LinkType == "ether" && observed.Kind == "" && strings.EqualFold(observed.PermanentAddress, l.PermanentMac) {
				matches = append(matches, observed.Name)
			}
		}
		switch len(matches) {
		case 0:
			r.find("error", "HARDWARE_MISSING", linkPath(i, "permanentMac"), "No interface with this permanent MAC is present.")
		case 1:
			r.name[l.ID] = matches[0]
			r.result.Resolved = append(r.result.Resolved, Resolution{l.ID, matches[0]})
		default:
			slices.Sort(matches)
			r.find("error", "HARDWARE_AMBIGUOUS", linkPath(i, "permanentMac"), "Several interfaces share this permanent MAC: "+strings.Join(matches, ", ")+".")
		}
	}
	for name, owners := range derived {
		collision := len(owners) > 1
		for _, observed := range r.in.Links {
			// An observed link of the same kind is Andaru's own from an earlier apply.
			if observed.Name == name && observed.Kind != kernelKind[r.d.Links[owners[0]].Kind] {
				collision = true
			}
		}
		for _, i := range owners {
			if collision {
				r.find("error", "NAME_COLLISION", linkPath(i, ""), "The interface name "+name+" derived for this link is already taken.")
			} else {
				r.name[r.d.Links[i].ID] = name
			}
		}
	}
}

// visit decides, parents first, whether a link is rendered and whether others may build on it.
func (r *renderer) visit(id string) bool {
	if r.visited[id] {
		return r.usable[id]
	}
	r.visited[id] = true
	i := r.index[id]
	l := r.d.Links[i]
	named := r.name[id] != ""
	switch l.Kind {
	case "ethernet":
		r.rendered[id] = named
		r.usable[id] = named && l.Enabled
	case "vlan":
		parent := r.visit(l.Parent)
		if l.Enabled && named && !parent {
			r.find("warning", "DEPENDENCY_UNAVAILABLE", linkPath(i, "parent"), "Not rendered: the parent link is unavailable.")
		}
		r.rendered[id] = l.Enabled && named && parent
		r.usable[id] = r.rendered[id]
	case "bridge", "bond":
		r.rendered[id] = l.Enabled && named
		r.usable[id] = r.rendered[id]
		for m, member := range l.Members {
			// One dead port must not take the rest of a bridge or bond down.
			if !r.visit(member) && r.rendered[id] {
				r.find("warning", "DEPENDENCY_UNAVAILABLE", fmt.Sprintf("/links/%d/members/%d", i, m), "Left out: this member link is unavailable.")
			}
		}
	case "pppoe":
		r.visitPPPoE(i, named)
	}
	return r.usable[id]
}

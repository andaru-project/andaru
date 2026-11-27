package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/addon"
)

var (
	addonIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}(\.[a-z][a-z0-9-]{0,31})+$`)
	addonIfNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,14}$`)
	zoneIDPattern      = regexp.MustCompile(`^zon_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

func addonSecretScope(id string) string { return "addon:" + id }

func validSecretScope(scope string) bool {
	if scope == "core" {
		return true
	}
	id, ok := strings.CutPrefix(scope, "addon:")
	return ok && addonIDPattern.MatchString(id)
}

// addonSecretRef extracts a secretReference if v is an object containing a string "secret" key.
func addonSecretRef(v any) (*secretReference, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	name, ok := m["secret"].(string)
	if !ok {
		return nil, false
	}
	ref := &secretReference{Secret: name}
	if u, ok := m["unresolved"].(bool); ok {
		ref.Unresolved = u
	}
	return ref, true
}

func validateAddons(d *declaration) []FieldError {
	if len(d.Addons) == 0 {
		return nil
	}
	zones := make(map[string]bool, len(d.Zones))
	for _, z := range d.Zones {
		zones[z.ID] = true
	}
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{Path: path, Message: msg})
		}
	}
	seenIDs := map[string]bool{}
	seenIfaces := map[string]bool{}
	for i := range d.Addons {
		a := &d.Addons[i]
		p := fmt.Sprintf("/addons/%d", i)
		if !addonIDPattern.MatchString(a.ID) {
			bad(p+"/id", "Invalid add-on identity.")
		} else if seenIDs[a.ID] {
			bad(p+"/id", "Duplicate add-on identity.")
		}
		seenIDs[a.ID] = true
		for j, ifc := range a.Interfaces {
			ip := fmt.Sprintf("%s/interfaces/%d", p, j)
			if !addonIfNamePattern.MatchString(ifc.Name) {
				bad(ip+"/name", "Invalid add-on interface name.")
			} else if seenIfaces[ifc.Name] {
				bad(ip+"/name", "Add-on interface is already declared.")
			}
			seenIfaces[ifc.Name] = true
			if ifc.Zone != "" && !zones[ifc.Zone] {
				bad(ip+"/zone", "Unknown zone.")
			}
		}
		// Reject dangling zone references in settings for both enabled and disabled add-ons.
		for k, v := range a.Settings {
			if s, ok := v.(string); ok && zoneIDPattern.MatchString(s) && !zones[s] {
				bad(p+"/settings/"+k, "Unknown zone.")
			}
			if v == nil {
				bad(p+"/settings/"+k, "Null setting values are not allowed.")
			}
		}
		for topic, state := range a.BusGrants.Publish {
			if state != "granted" && state != "denied" {
				bad(p+"/busGrants/publish/"+topic, "Invalid bus grant state.")
			}
		}
		for topic, state := range a.BusGrants.Subscribe {
			if state != "granted" && state != "denied" {
				bad(p+"/busGrants/subscribe/"+topic, "Invalid bus grant state.")
			}
		}
	}
	return out
}

// validateAddonsAgainstCatalog validates every written add-on entry against its catalog manifest and
// materializes declared setting defaults onto d.Addons.
func validateAddonsAgainstCatalog(cat *addon.Catalog, d *declaration, written map[objectKey]bool) []FieldError {
	if cat == nil || len(d.Addons) == 0 {
		return nil
	}
	zones := make(map[string]bool, len(d.Zones))
	for _, z := range d.Zones {
		zones[z.ID] = true
	}
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{Path: path, Message: msg})
		}
	}
	for i := range d.Addons {
		a := &d.Addons[i]
		if written != nil && !written[objectKey{"addons", a.ID}] {
			continue
		}
		p := fmt.Sprintf("/addons/%d", i)
		m, ok := cat.ByID(a.ID)
		if !ok {
			bad(p+"/id", fmt.Sprintf("Add-on %q is not in the catalog.", a.ID))
			continue
		}
		// Validate settings and materialize defaults.
		rawSettings := a.Settings
		if rawSettings == nil {
			rawSettings = map[string]any{}
		}
		materialized, sErrs := addon.ValidateSettings(m, rawSettings, zones)
		for _, fe := range sErrs {
			bad(p+"/settings"+fe.Path, fe.Message)
		}
		if len(sErrs) == 0 {
			if len(materialized) == 0 {
				a.Settings = nil
			} else {
				a.Settings = materialized
			}
		}
		// Validate interface attachments against manifest.Interfaces.
		allowedIfaces := make(map[string]addon.InterfaceSpec, len(m.Interfaces))
		for _, spec := range m.Interfaces {
			allowedIfaces[spec.Name] = spec
		}
		bound := map[string]string{}
		for j, ifc := range a.Interfaces {
			ip := fmt.Sprintf("%s/interfaces/%d", p, j)
			if _, ok := allowedIfaces[ifc.Name]; !ok {
				bad(ip+"/name", fmt.Sprintf("Interface %q is not declared by %s.", ifc.Name, m.ID))
				continue
			}
			bound[ifc.Name] = ifc.Zone
		}
		if a.Enabled {
			for _, spec := range m.Interfaces {
				if spec.ZoneRequired && bound[spec.Name] == "" {
					bad(p+"/interfaces", fmt.Sprintf("Interface %q requires a zone when enabled.", spec.Name))
				}
			}
		}
		// Validate busGrants keys against manifest.Bus.
		pubAllowed := map[string]bool{}
		subAllowed := map[string]bool{}
		if m.Bus != nil {
			for _, t := range m.Bus.Publish {
				pubAllowed[t.Topic] = true
			}
			for _, t := range m.Bus.Subscribe {
				subAllowed[t.Topic] = true
			}
		}
		for topic := range a.BusGrants.Publish {
			if !pubAllowed[topic] {
				bad(p+"/busGrants/publish", fmt.Sprintf("Publish topic %q is not requested by %s.", topic, m.ID))
			}
		}
		for topic := range a.BusGrants.Subscribe {
			if !subAllowed[topic] {
				bad(p+"/busGrants/subscribe", fmt.Sprintf("Subscribe topic %q is not requested by %s.", topic, m.ID))
			}
		}
	}
	return out
}

func sameAddonSecrets(a, b map[string]map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !maps.Equal(va, vb) {
			return false
		}
	}
	return true
}

func addonSettingSecretKeys(settings map[string]any) []string {
	var keys []string
	for k, v := range settings {
		if _, ok := addonSecretRef(v); ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

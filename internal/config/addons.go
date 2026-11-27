package config

import (
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/addon"
)

var (
	zoneIDPattern     = regexp.MustCompile(`^zon_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	secretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

func addonSecretScope(id string) string { return "addon:" + id }

func validSecretScope(scope string) bool {
	if scope == "core" {
		return true
	}
	id, ok := strings.CutPrefix(scope, "addon:")
	return ok && addon.ValidID(id)
}

func escapeJSONPointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
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

// validateAddonSecretWrite validates an add-on setting secretWrite/secretRef object against the
// strict secretWrite contract before secret preconditions or secret-store writes execute.
func validateAddonSecretWrite(pw map[string]any, path string) *Fault {
	for k := range pw {
		switch k {
		case "secret", "set", "expectedVersion", "unresolved":
		default:
			return fault("VALIDATION_FAILED", path+"/"+escapeJSONPointer(k), "Unknown field in secret reference.")
		}
	}
	name, ok := pw["secret"].(string)
	if !ok || !secretNamePattern.MatchString(name) {
		return fault("VALIDATION_FAILED", path+"/secret", "Invalid secret name.")
	}
	rawSet, hasSet := pw["set"]
	if hasSet {
		s, ok := rawSet.(string)
		if !ok || len(s) > 4096 || strings.ContainsRune(s, 0) {
			return fault("VALIDATION_FAILED", path+"/set", "Secret value must be a valid string within size limits.")
		}
	}
	if rawExp, hasExp := pw["expectedVersion"]; hasExp {
		if !hasSet {
			return fault("VALIDATION_FAILED", path+"/expectedVersion", "expectedVersion requires set.")
		}
		if text, ok := rawExp.(string); !ok {
			return fault("INVALID_REQUEST", path+"/expectedVersion", "Invalid secret version.")
		} else if _, err := ParseVersion(text); err != nil {
			return fault("INVALID_REQUEST", path+"/expectedVersion", "Invalid secret version.")
		}
	}
	if rawUnresolved, hasUnresolved := pw["unresolved"]; hasUnresolved {
		if hasSet {
			return fault("VALIDATION_FAILED", path+"/unresolved", "unresolved cannot be combined with set.")
		}
		if _, ok := rawUnresolved.(bool); !ok {
			return fault("VALIDATION_FAILED", path+"/unresolved", "unresolved must be a boolean.")
		}
	}
	return nil
}

func addonPropIsSecretRef(cat *addon.Catalog, addonID, propName string) (knownAddon, isSecret bool) {
	if cat == nil {
		return false, true
	}
	m, ok := cat.ByID(addonID)
	if !ok {
		return false, true
	}
	for i := range m.Properties {
		if m.Properties[i].Name == propName {
			return true, m.Properties[i].Type == "secret-reference"
		}
	}
	return true, false
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
		if !addon.ValidID(a.ID) {
			bad(p+"/id", "Invalid add-on identity.")
		} else if seenIDs[a.ID] {
			bad(p+"/id", "Duplicate add-on identity.")
		}
		seenIDs[a.ID] = true
		for j, ifc := range a.Interfaces {
			ip := fmt.Sprintf("%s/interfaces/%d", p, j)
			if !addon.ValidInterfaceName(ifc.Name) {
				bad(ip+"/name", "Invalid or reserved add-on interface name.")
			} else if seenIfaces[ifc.Name] {
				bad(ip+"/name", "Add-on interface is already declared.")
			}
			seenIfaces[ifc.Name] = true
			if ifc.Zone != "" && !zones[ifc.Zone] {
				bad(ip+"/zone", "Unknown zone.")
			}
		}
		// Reject dangling zone references and null values in settings for both enabled and disabled add-ons.
		settingKeys := make([]string, 0, len(a.Settings))
		for k := range a.Settings {
			settingKeys = append(settingKeys, k)
		}
		slices.Sort(settingKeys)
		for _, k := range settingKeys {
			v := a.Settings[k]
			sp := p + "/settings/" + escapeJSONPointer(k)
			if s, ok := v.(string); ok && zoneIDPattern.MatchString(s) && !zones[s] {
				bad(sp, "Unknown zone.")
			}
			if v == nil {
				bad(sp, "Null setting values are not allowed.")
			}
			if obj, ok := v.(map[string]any); ok {
				objKeys := make([]string, 0, len(obj))
				for fk := range obj {
					objKeys = append(objKeys, fk)
				}
				slices.Sort(objKeys)
				for _, fk := range objKeys {
					fv := obj[fk]
					switch fk {
					case "secret":
						if name, ok := fv.(string); !ok || !secretNamePattern.MatchString(name) {
							bad(sp+"/secret", "Invalid secret name.")
						}
					case "unresolved":
						if _, ok := fv.(bool); !ok {
							bad(sp+"/unresolved", "unresolved must be a boolean.")
						}
					default:
						bad(sp+"/"+escapeJSONPointer(fk), "Unknown field in stored secret reference.")
					}
				}
				if _, hasSecret := obj["secret"]; !hasSecret {
					bad(sp+"/secret", "Stored secret reference requires secret.")
				}
			}
		}
		pubTopics := make([]string, 0, len(a.BusGrants.Publish))
		for topic := range a.BusGrants.Publish {
			pubTopics = append(pubTopics, topic)
		}
		slices.Sort(pubTopics)
		for _, topic := range pubTopics {
			state := a.BusGrants.Publish[topic]
			if state != "granted" && state != "denied" {
				bad(p+"/busGrants/publish/"+escapeJSONPointer(topic), "Invalid bus grant state.")
			}
		}
		subTopics := make([]string, 0, len(a.BusGrants.Subscribe))
		for topic := range a.BusGrants.Subscribe {
			subTopics = append(subTopics, topic)
		}
		slices.Sort(subTopics)
		for _, topic := range subTopics {
			state := a.BusGrants.Subscribe[topic]
			if state != "granted" && state != "denied" {
				bad(p+"/busGrants/subscribe/"+escapeJSONPointer(topic), "Invalid bus grant state.")
			}
		}
	}
	return out
}

// validateAddonsAgainstCatalog validates every written add-on entry against its catalog manifest,
// materializes declared setting defaults onto d.Addons, and checks WebUI origin separation across all
// configured add-ons so later management/DNS changes cannot expose Andaru's session cookie.
func validateAddonsAgainstCatalog(cat *addon.Catalog, d *declaration, written map[objectKey][]byte) []FieldError {
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
		p := fmt.Sprintf("/addons/%d", i)
		m, ok := cat.ByID(a.ID)
		_, wasWritten := written[objectKey{"addons", a.ID}]
		if written == nil || wasWritten {
			if !ok {
				bad(p+"/id", fmt.Sprintf("Add-on %q is not in the catalog.", a.ID))
				continue
			}
			// Validate settings and materialize defaults.
			materialized, sErrs := addon.ValidateSettings(m, a.Settings, zones)
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
			pubKeys := make([]string, 0, len(a.BusGrants.Publish))
			for topic := range a.BusGrants.Publish {
				pubKeys = append(pubKeys, topic)
			}
			slices.Sort(pubKeys)
			for _, topic := range pubKeys {
				if !pubAllowed[topic] {
					bad(p+"/busGrants/publish/"+escapeJSONPointer(topic), fmt.Sprintf("Publish topic %q is not requested by %s.", topic, m.ID))
				}
			}
			subKeys := make([]string, 0, len(a.BusGrants.Subscribe))
			for topic := range a.BusGrants.Subscribe {
				subKeys = append(subKeys, topic)
			}
			slices.Sort(subKeys)
			for _, topic := range subKeys {
				if !subAllowed[topic] {
					bad(p+"/busGrants/subscribe/"+escapeJSONPointer(topic), fmt.Sprintf("Subscribe topic %q is not requested by %s.", topic, m.ID))
				}
			}
		}
	}
	for _, fe := range validateAddonsWebUISeparation(cat, d) {
		bad(fe.Path, fe.Message)
	}
	return out
}

// validateAddonsWebUISeparation verifies every cataloged add-on's WebUI hostnameSetting does not match
// Andaru's management hostname, local management FQDN, or management segment IP addresses.
func validateAddonsWebUISeparation(cat *addon.Catalog, d *declaration) []FieldError {
	if cat == nil || len(d.Addons) == 0 {
		return nil
	}
	var out []FieldError
	for i := range d.Addons {
		a := &d.Addons[i]
		m, ok := cat.ByID(a.ID)
		if !ok || m.WebUI == nil || m.WebUI.HostnameSetting == "" {
			continue
		}
		propName := strings.TrimPrefix(m.WebUI.HostnameSetting, "/")
		if rawHost, exists := a.Settings[propName].(string); exists && rawHost != "" {
			if matchesManagementHost(d, rawHost) {
				if len(out) < 100 {
					out = append(out, FieldError{
						Path:    fmt.Sprintf("/addons/%d/settings/%s", i, escapeJSONPointer(propName)),
						Message: "Add-on WebUI hostname must not match Andaru's management hostname or IP address.",
					})
				}
			}
		}
	}
	return out
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

func matchesManagementHost(d *declaration, candidate string) bool {
	norm := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".")
	if norm == "" {
		return false
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		short, _, _ := strings.Cut(host, ".")
		if norm == host || norm == short {
			return true
		}
		if d.DNS != nil && d.DNS.LocalDomain != "" {
			dom := strings.Trim(strings.ToLower(strings.TrimSpace(d.DNS.LocalDomain)), ".")
			if dom != "" && (norm == host+"."+dom || norm == short+"."+dom) {
				return true
			}
		}
	}
	candAddr, isIP := parseHostAddr(norm)
	if !isIP || d.Management == nil || d.Management.Segment == "" {
		return false
	}
	for _, seg := range d.Segments {
		if seg.ID != d.Management.Segment {
			continue
		}
		for _, cidr := range append(slices.Clone(seg.Addressing.IPv4.Addresses), seg.Addressing.IPv6.Addresses...) {
			if pfx, err := netip.ParsePrefix(cidr); err == nil && pfx.Addr() == candAddr {
				return true
			}
		}
	}
	return false
}

func parseHostAddr(s string) (netip.Addr, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	a, err := netip.ParseAddr(trimmed)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

package addon

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"unicode/utf8"
)

// ValidateSettings validates raw add-on settings against m.Properties, rejects unknown keys and explicit null
// values, materializes declared defaults onto the returned map, preserves explicit false/0/"", and checks
// zone references against validZones (when non-nil).
func ValidateSettings(m *Manifest, raw map[string]any, validZones map[string]bool) (map[string]any, []FieldError) {
	var errs []FieldError
	propsByName := make(map[string]*Property, len(m.Properties))
	for i := range m.Properties {
		p := &m.Properties[i]
		propsByName[p.Name] = p
	}
	for k, v := range raw {
		p := "/" + escapePointer(k)
		if _, ok := propsByName[k]; !ok {
			errs = append(errs, FieldError{Path: p, Message: fmt.Sprintf("unknown setting %q", k)})
			continue
		}
		if v == nil {
			errs = append(errs, FieldError{Path: p, Message: "null setting values are not allowed; omit the key to unset"})
		}
	}
	out := make(map[string]any, len(m.Properties))
	for i := range m.Properties {
		prop := &m.Properties[i]
		p := "/" + escapePointer(prop.Name)
		val, present := raw[prop.Name]
		if !present {
			if prop.Default != nil {
				val = cloneDefault(prop.Default)
				present = true
			}
		} else if val == nil {
			// Already recorded above.
			continue
		}
		if !present {
			if prop.Required {
				errs = append(errs, FieldError{Path: p, Message: "required setting is missing"})
			}
			continue
		}
		if verrs := validateValue(prop, val, p, validZones); len(verrs) > 0 {
			errs = append(errs, verrs...)
			continue
		}
		out[prop.Name] = val
	}
	return out, errs
}

func cloneDefault(v any) any {
	switch x := v.(type) {
	case []any:
		cp := make([]any, len(x))
		for i, item := range x {
			cp[i] = cloneDefault(item)
		}
		return cp
	case map[string]any:
		cp := make(map[string]any, len(x))
		for k, val := range x {
			cp[k] = cloneDefault(val)
		}
		return cp
	default:
		return v
	}
}

func validateValue(prop *Property, val any, p string, validZones map[string]bool) []FieldError {
	if val == nil {
		return []FieldError{{Path: p, Message: "null values are not allowed"}}
	}
	switch prop.Type {
	case "string":
		s, ok := val.(string)
		if !ok {
			return []FieldError{{Path: p, Message: "expected string"}}
		}
		n := utf8.RuneCountInString(s)
		if prop.MinLength != nil && n < *prop.MinLength {
			return []FieldError{{Path: p, Message: fmt.Sprintf("length must be at least %d", *prop.MinLength)}}
		}
		if prop.MaxLength != nil && n > *prop.MaxLength {
			return []FieldError{{Path: p, Message: fmt.Sprintf("length must be at most %d", *prop.MaxLength)}}
		}
		if msg := validateFormat(prop.Format, s); msg != "" {
			return []FieldError{{Path: p, Message: msg}}
		}
		if prop.Pattern != "" {
			if re, err := regexp.Compile(prop.Pattern); err == nil && !re.MatchString(s) {
				return []FieldError{{Path: p, Message: "value does not match required pattern"}}
			}
		}
	case "string-list":
		items, ok := toStringSlice(val)
		if !ok {
			return []FieldError{{Path: p, Message: "expected array of strings"}}
		}
		var errs []FieldError
		if prop.MinItems != nil && len(items) < *prop.MinItems {
			errs = append(errs, FieldError{Path: p, Message: fmt.Sprintf("must contain at least %d items", *prop.MinItems)})
		}
		if prop.MaxItems != nil && len(items) > *prop.MaxItems {
			errs = append(errs, FieldError{Path: p, Message: fmt.Sprintf("must contain at most %d items", *prop.MaxItems)})
		}
		seen := map[string]bool{}
		sep := ""
		if prop.Binding != nil && prop.Binding.Encoding != nil && prop.Binding.Encoding.Mode == "join" {
			sep = prop.Binding.Encoding.Separator
		}
		for i, item := range items {
			ip := fmt.Sprintf("%s/%d", p, i)
			if seen[item] {
				errs = append(errs, FieldError{Path: ip, Message: "duplicate item"})
			}
			seen[item] = true
			if msg := validateFormat(prop.ItemFormat, item); msg != "" {
				errs = append(errs, FieldError{Path: ip, Message: msg})
			}
			if sep != "" && containsString(item, sep) {
				errs = append(errs, FieldError{Path: ip, Message: fmt.Sprintf("item must not contain separator %q", sep)})
			}
		}
		return errs
	case "boolean":
		if _, ok := val.(bool); !ok {
			return []FieldError{{Path: p, Message: "expected boolean"}}
		}
	case "integer":
		f, ok := toFloat64(val)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f {
			return []FieldError{{Path: p, Message: "expected integer"}}
		}
		if prop.Minimum != nil && f < *prop.Minimum {
			return []FieldError{{Path: p, Message: fmt.Sprintf("value must be >= %v", *prop.Minimum)}}
		}
		if prop.Maximum != nil && f > *prop.Maximum {
			return []FieldError{{Path: p, Message: fmt.Sprintf("value must be <= %v", *prop.Maximum)}}
		}
	case "number":
		f, ok := toFloat64(val)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return []FieldError{{Path: p, Message: "expected number"}}
		}
		if prop.Minimum != nil && f < *prop.Minimum {
			return []FieldError{{Path: p, Message: fmt.Sprintf("value must be >= %v", *prop.Minimum)}}
		}
		if prop.Maximum != nil && f > *prop.Maximum {
			return []FieldError{{Path: p, Message: fmt.Sprintf("value must be <= %v", *prop.Maximum)}}
		}
	case "enum":
		items, ok := toStringSlice(val)
		if !ok {
			return []FieldError{{Path: p, Message: "enum setting must be an array of option strings"}}
		}
		var errs []FieldError
		if prop.MinItems != nil && len(items) < *prop.MinItems {
			errs = append(errs, FieldError{Path: p, Message: fmt.Sprintf("must select at least %d option(s)", *prop.MinItems)})
		}
		if prop.MaxItems != nil && len(items) > *prop.MaxItems {
			errs = append(errs, FieldError{Path: p, Message: fmt.Sprintf("must select at most %d option(s)", *prop.MaxItems)})
		}
		allowed := make([]string, len(prop.Options))
		for i, opt := range prop.Options {
			allowed[i] = opt.Value
		}
		seen := map[string]bool{}
		for i, item := range items {
			ip := fmt.Sprintf("%s/%d", p, i)
			if !slices.Contains(allowed, item) {
				errs = append(errs, FieldError{Path: ip, Message: fmt.Sprintf("unknown enum option %q", item)})
			} else if seen[item] {
				errs = append(errs, FieldError{Path: ip, Message: "duplicate enum selection"})
			}
			seen[item] = true
		}
		return errs
	case "secret-reference":
		m, ok := val.(map[string]any)
		if !ok {
			return []FieldError{{Path: p, Message: `expected secret reference object {"secret": "..."}`}}
		}
		for k := range m {
			if k != "secret" && k != "unresolved" {
				return []FieldError{{Path: p + "/" + escapePointer(k), Message: fmt.Sprintf("unknown secret reference field %q", k)}}
			}
		}
		name, ok := m["secret"].(string)
		if !ok || !secretNamePattern.MatchString(name) {
			return []FieldError{{Path: p + "/secret", Message: "invalid secret name"}}
		}
		if u, exists := m["unresolved"]; exists {
			if _, ok := u.(bool); !ok {
				return []FieldError{{Path: p + "/unresolved", Message: "unresolved must be a boolean"}}
			}
		}
	case "zone-reference":
		id, ok := val.(string)
		if !ok || id == "" {
			return []FieldError{{Path: p, Message: "expected zone id string"}}
		}
		if validZones != nil && !validZones[id] {
			return []FieldError{{Path: p, Message: fmt.Sprintf("unknown zone %q", id)}}
		}
	}
	return nil
}

func toStringSlice(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, len(x))
		for i, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

func containsString(s, sub string) bool {
	if sub == "" {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

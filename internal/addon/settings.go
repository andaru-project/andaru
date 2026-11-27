package addon

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
	rawKeys := make([]string, 0, len(raw))
	for k := range raw {
		rawKeys = append(rawKeys, k)
	}
	slices.Sort(rawKeys)
	for _, k := range rawKeys {
		v := raw[k]
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
		if present {
			if val == nil {
				continue // Already recorded above.
			}
		} else if prop.Default != nil {
			val = cloneDefault(prop.Default)
		} else {
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
	envBound := prop.Binding != nil && prop.Binding.Type == "environment"
	switch prop.Type {
	case "string":
		s, ok := val.(string)
		if !ok {
			return []FieldError{{Path: p, Message: "expected string"}}
		}
		if strings.ContainsRune(s, 0) {
			return []FieldError{{Path: p, Message: "NUL bytes are not allowed"}}
		}
		if envBound && strings.ContainsAny(s, "\r\n") {
			return []FieldError{{Path: p, Message: "newlines are not allowed in environment-bound settings"}}
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
			if strings.ContainsRune(item, 0) {
				errs = append(errs, FieldError{Path: ip, Message: "NUL bytes are not allowed"})
			}
			if envBound && strings.ContainsAny(item, "\r\n") {
				errs = append(errs, FieldError{Path: ip, Message: "newlines are not allowed in environment-bound settings"})
			}
			if seen[item] {
				errs = append(errs, FieldError{Path: ip, Message: "duplicate item"})
			}
			seen[item] = true
			if msg := validateFormat(prop.ItemFormat, item); msg != "" {
				errs = append(errs, FieldError{Path: ip, Message: msg})
			}
			if sep != "" && strings.Contains(item, sep) {
				errs = append(errs, FieldError{Path: ip, Message: fmt.Sprintf("item must not contain separator %q", sep)})
			}
		}
		return errs
	case "boolean":
		if _, ok := val.(bool); !ok {
			return []FieldError{{Path: p, Message: "expected boolean"}}
		}
	case "integer":
		iv, ok := toExactInt64(val)
		if !ok {
			return []FieldError{{Path: p, Message: "expected integer"}}
		}
		f := float64(iv)
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
		seen := map[string]bool{}
		for i, item := range items {
			ip := fmt.Sprintf("%s/%d", p, i)
			if !slices.ContainsFunc(prop.Options, func(o EnumOption) bool { return o.Value == item }) {
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

const maxSafeJSONInt = 1<<53 - 1

func toExactInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		n, err := strconv.ParseInt(x.String(), 10, 64)
		return n, err == nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x || x < -maxSafeJSONInt || x > maxSafeJSONInt {
			return 0, false
		}
		return int64(x), true
	default:
		return 0, false
	}
}

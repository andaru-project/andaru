// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

// StringSetting returns the non-empty string setting key, or def.
func (c TargetConfig) StringSetting(key, def string) string {
	if v, ok := c.Settings[key].(string); ok && v != "" {
		return v
	}
	return def
}

// BoolSetting returns the bool setting key, or def when absent or not a bool.
func (c TargetConfig) BoolSetting(key string, def bool) bool {
	if v, ok := c.Settings[key].(bool); ok {
		return v
	}
	return def
}

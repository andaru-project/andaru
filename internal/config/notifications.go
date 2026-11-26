package config

import (
	"fmt"
	"net/url"
)

// webhookURL reports whether s is an http or https URL with a host.
func webhookURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// validateChannels checks the notification channels' names and sources. The schema checks their kinds and
// severities, and the save their secrets.
func validateChannels(d *declaration, bad func(string, string)) {
	seen := map[string]bool{}
	for i, c := range d.Notifications.Channels {
		p := fmt.Sprintf("/notifications/channels/%d", i)
		if seen[c.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		if invalidText(c.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if seen["name:"+c.Name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		seen[c.ID], seen["name:"+c.Name] = true, true
		for j, source := range c.Filter.Sources {
			if invalidText(source, true) {
				bad(fmt.Sprintf("%s/filter/sources/%d", p, j), "Control or format characters are not allowed.")
			}
		}
	}
}

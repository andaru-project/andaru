package config

import (
	"context"
	"fmt"
	"net/url"
)

// NotificationChannel is a saved channel with its secrets' values, for sending.
type NotificationChannel struct {
	ID          string
	Name        string
	Enabled     bool
	Kind        string
	MinSeverity string
	Sources     []string
	// URL and Authorization are the webhook's; Authorization is empty when the channel sends none.
	URL, Authorization string
	// Unresolved says a secret it references has no value, as after a restore without secrets: it cannot send.
	Unresolved bool
}

// NotificationChannels returns the channels of the saved head, enabled or not.
func (s *Store) NotificationChannels(ctx context.Context) (result []NotificationChannel, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		value := func(ref *secretReference) (string, bool) {
			if ref == nil {
				return "", true
			}
			entry, ok := state.Secrets.Scopes["core"][ref.Secret]
			return entry.Value, ok && !ref.Unresolved
		}
		for _, c := range state.Config.Notifications.Channels {
			n := NotificationChannel{ID: c.ID, Name: c.Name, Enabled: c.Enabled == nil || *c.Enabled, Kind: c.Kind,
				MinSeverity: c.Filter.MinSeverity, Sources: c.Filter.Sources}
			if c.Webhook != nil {
				var urlOK, authOK bool
				n.URL, urlOK = value(&c.Webhook.URL)
				n.Authorization, authOK = value(c.Webhook.Authorization)
				n.Unresolved = !urlOK || !authOK
			}
			result = append(result, n)
		}
		return nil
	})
	return
}

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

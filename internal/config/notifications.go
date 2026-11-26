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
	// Server and Topic are ntfy's; Token is ntfy's (empty when it sends none), Pushover's application token or
	// Telegram's bot token; User is Pushover's user key and ChatID Telegram's chat.
	Server, Topic, Token, User, ChatID string
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
			values := map[string]string{}
			for _, s := range c.secrets() {
				v, ok := value(s.ref)
				values[s.path], n.Unresolved = v, n.Unresolved || !ok
			}
			switch {
			case c.Webhook != nil:
				n.URL, n.Authorization = values["/webhook/url"], values["/webhook/authorization"]
			case c.Ntfy != nil:
				n.Server, n.Topic, n.Token = c.Ntfy.Server, c.Ntfy.Topic, values["/ntfy/token"]
			case c.Pushover != nil:
				n.Token, n.User = values["/pushover/token"], values["/pushover/user"]
			case c.Telegram != nil:
				n.Token, n.ChatID = values["/telegram/token"], c.Telegram.ChatID
			}
			result = append(result, n)
		}
		return nil
	})
	return
}

// webhookURL reports whether s is an http or https URL with a host, as a webhook's URL and an ntfy server must be.
func webhookURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// validateChannels checks the notification channels' names, sources and ntfy servers. The schema checks their kinds and
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
		if c.Ntfy != nil && !ntfyServer(c.Ntfy.Server) {
			bad(p+"/ntfy/server", "Expected an http or https URL of a server, with no query, fragment or credentials.")
		}
		for j, source := range c.Filter.Sources {
			if invalidText(source, true) {
				bad(fmt.Sprintf("%s/filter/sources/%d", p, j), "Control or format characters are not allowed.")
			}
		}
	}
}

// ntfyServer reports whether s is an ntfy server's base URL, which may sit under a path behind a proxy. It is not a
// secret, so it may not hold credentials, and the publish path is added to it, so it has no query or fragment.
func ntfyServer(s string) bool {
	u, e := url.Parse(s)
	return e == nil && webhookURL(s) && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

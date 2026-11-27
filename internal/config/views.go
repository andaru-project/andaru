package config

import (
	"encoding/json"
	"slices"

	"github.com/andaru-project/andaru/internal/authz"
)

type ObjectView struct {
	Value   json.RawMessage `json:"value"`
	Version Version         `json:"version"`
}
type CollectionView struct {
	State        string       `json:"state"`
	SavedVersion Version      `json:"savedVersion"`
	Items        []ObjectView `json:"items"`
	// Version is a rule list's own version, which guards its order; empty for other collections.
	Version Version `json:"version,omitempty"`
}
type SnapshotView struct {
	State         string          `json:"state"`
	SavedVersion  Version         `json:"savedVersion"`
	Value         json.RawMessage `json:"value"`
	UnusedSecrets []string        `json:"unusedSecrets"`
}
type ChangedView struct {
	Collection string          `json:"collection"`
	ID         string          `json:"id"`
	Deleted    bool            `json:"deleted,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
	Version    Version         `json:"version,omitempty"`
}
type SaveResult struct {
	State        string            `json:"state"`
	SavedVersion Version           `json:"savedVersion"`
	Items        []ChangedView     `json:"items"`
	Created      map[string]string `json:"created"`
}
type VersionInfo struct {
	ID                 Version  `json:"id"`
	ParentID           *Version `json:"parentId,omitempty"`
	CreatedAt          string   `json:"createdAt"`
	Actor              string   `json:"actor"`
	ChangedObjectCount int      `json:"changedObjectCount"`
	// WrittenBy is the Andaru version that saved this version; absent when the Andaru that saved it did not record it.
	WrittenBy string `json:"writtenBy,omitempty"`
}
type HistoryView struct {
	State      string        `json:"state"`
	Items      []VersionInfo `json:"items"`
	NextBefore *Version      `json:"nextBefore,omitempty"`
}
type VersionView struct {
	State string `json:"state"`
	VersionInfo
	Value   json.RawMessage `json:"value"`
	Changes []objectChange  `json:"changes"`
	// Questions are the decisions this version needs to read in the current schema; it reads with the provisional
	// choices until they are answered.
	Questions []Question `json:"questions,omitempty"`
}
type secretView struct {
	Secret     string  `json:"secret"`
	IsSet      bool    `json:"isSet"`
	UpdatedAt  string  `json:"updatedAt,omitempty"`
	Version    Version `json:"version"`
	Unresolved bool    `json:"unresolved,omitempty"`
}
type linkView struct {
	link
	Password *secretView `json:"password,omitempty"`
}
type channelView struct {
	channel
	Webhook  *webhookView  `json:"webhook,omitempty"`
	Ntfy     *ntfyView     `json:"ntfy,omitempty"`
	Pushover *pushoverView `json:"pushover,omitempty"`
	Telegram *telegramView `json:"telegram,omitempty"`
	Email    *emailView    `json:"email,omitempty"`
}
type webhookView struct {
	URL           *secretView `json:"url"`
	Authorization *secretView `json:"authorization,omitempty"`
}
type ntfyView struct {
	Server string      `json:"server"`
	Topic  string      `json:"topic"`
	Token  *secretView `json:"token,omitempty"`
}
type pushoverView struct {
	Token *secretView `json:"token"`
	User  *secretView `json:"user"`
}
type telegramView struct {
	Token  *secretView `json:"token"`
	ChatID string      `json:"chatId"`
}
type emailView struct {
	email
	Password *secretView `json:"password,omitempty"`
}

// viewSecret is a reference's view: whether it is set, and its version, never its value.
func viewSecret(ref *secretReference, secrets secretDocument) (*secretView, error) {
	return viewSecretInScope(ref, "core", secrets)
}

func viewSecretInScope(ref *secretReference, scope string, secrets secretDocument) (*secretView, error) {
	if ref == nil {
		return nil, nil
	}
	meta, ok := secrets.Scopes[scope][ref.Secret]
	if !ok && !ref.Unresolved {
		return nil, fault("STORE_UNAVAILABLE", "", "Secret reference is unavailable.")
	}
	if !ok {
		meta.Version = "0"
	}
	return &secretView{Secret: ref.Secret, IsSet: !ref.Unresolved, UpdatedAt: meta.UpdatedAt, Version: meta.Version, Unresolved: ref.Unresolved}, nil
}
func viewObject(v any, version Version, secrets secretDocument) (ObjectView, error) {
	var e error
	switch x := v.(type) {
	case *link:
		view := linkView{link: *x}
		if view.Password, e = viewSecret(x.Password, secrets); e != nil {
			return ObjectView{}, e
		}
		v = view
	case *channel:
		view := channelView{channel: *x}
		if x.Webhook != nil {
			view.Webhook = &webhookView{}
			if view.Webhook.URL, e = viewSecret(&x.Webhook.URL, secrets); e != nil {
				return ObjectView{}, e
			}
			if view.Webhook.Authorization, e = viewSecret(x.Webhook.Authorization, secrets); e != nil {
				return ObjectView{}, e
			}
		}
		if x.Ntfy != nil {
			view.Ntfy = &ntfyView{Server: x.Ntfy.Server, Topic: x.Ntfy.Topic}
			if view.Ntfy.Token, e = viewSecret(x.Ntfy.Token, secrets); e != nil {
				return ObjectView{}, e
			}
		}
		if x.Pushover != nil {
			view.Pushover = &pushoverView{}
			if view.Pushover.Token, e = viewSecret(&x.Pushover.Token, secrets); e != nil {
				return ObjectView{}, e
			}
			if view.Pushover.User, e = viewSecret(&x.Pushover.User, secrets); e != nil {
				return ObjectView{}, e
			}
		}
		if x.Telegram != nil {
			view.Telegram = &telegramView{ChatID: x.Telegram.ChatID}
			if view.Telegram.Token, e = viewSecret(&x.Telegram.Token, secrets); e != nil {
				return ObjectView{}, e
			}
		}
		if x.Email != nil {
			view.Email = &emailView{email: *x.Email}
			if view.Email.Password, e = viewSecret(x.Email.Password, secrets); e != nil {
				return ObjectView{}, e
			}
		}
		v = view
	case *addonConfig:
		view := *x
		if len(x.Settings) > 0 {
			cp := make(map[string]any, len(x.Settings))
			scope := addonSecretScope(x.ID)
			for k, val := range x.Settings {
				if ref, ok := addonSecretRef(val); ok {
					sv, err := viewSecretInScope(ref, scope, secrets)
					if err != nil {
						return ObjectView{}, err
					}
					cp[k] = sv
				} else {
					cp[k] = val
				}
			}
			view.Settings = cp
		}
		v = view
	}
	if v == nil {
		return ObjectView{}, fault("NOT_FOUND", "", "Object does not exist.")
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return ObjectView{}, e
	}
	return ObjectView{Value: raw, Version: version}, nil
}
func viewSlice[T any](items []T, col string, id func(*T) string, s savedState) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(items))
	for i := range items {
		item := &items[i]
		v, e := viewObject(item, s.Versions[objectKey{col, id(item)}].Version, s.Secrets)
		if e != nil {
			return nil, e
		}
		out = append(out, v.Value)
	}
	return out, nil
}

func publicSnapshot(s savedState) (SnapshotView, error) {
	links, err := viewSlice(s.Config.Links, "links", func(l *link) string { return l.ID }, s)
	if err != nil {
		return SnapshotView{}, err
	}
	channels, err := viewSlice(s.Config.Notifications.Channels, "notifications.channels", func(c *channel) string { return c.ID }, s)
	if err != nil {
		return SnapshotView{}, err
	}
	addons, err := viewSlice(s.Config.Addons, "addons", func(a *addonConfig) string { return a.ID }, s)
	if err != nil {
		return SnapshotView{}, err
	}
	used := map[string]bool{}
	for _, h := range heldSecrets(&s.Config) {
		if h.scope == "core" && !h.ref.Unresolved {
			used[h.ref.Secret] = true
		}
	}
	// The whole declaration, every section included, with each link, channel, and add-on as its secret-free view.
	var d map[string]json.RawMessage
	whole, e := json.Marshal(s.Config)
	if e == nil {
		e = json.Unmarshal(whole, &d)
	}
	if e != nil {
		return SnapshotView{}, e
	}
	if d["links"], e = json.Marshal(links); e != nil {
		return SnapshotView{}, e
	}
	if len(channels) > 0 {
		if d["notifications"], e = json.Marshal(map[string]any{"channels": channels}); e != nil {
			return SnapshotView{}, e
		}
	}
	if len(addons) > 0 {
		if d["addons"], e = json.Marshal(addons); e != nil {
			return SnapshotView{}, e
		}
	}
	roles := append(authz.BuiltIns(), s.Config.Roles...)
	if d["roles"], e = json.Marshal(roles); e != nil {
		return SnapshotView{}, e
	}
	raw, e := json.Marshal(d)
	if e != nil {
		return SnapshotView{}, e
	}
	unused := []string{}
	for name := range s.Secrets.Scopes["core"] {
		if !used[name] {
			unused = append(unused, name)
		}
	}
	slices.Sort(unused)
	return SnapshotView{State: "saved", SavedVersion: s.Version, Value: raw, UnusedSecrets: unused}, nil
}
func resultView(p preparedChange) (SaveResult, error) {
	result := SaveResult{State: "saved", SavedVersion: p.State.Version, Items: []ChangedView{}, Created: p.Created}
	if len(p.Results) == 0 {
		return result, nil
	}
	index := objects(&p.State.Config)
	for _, key := range p.Results {
		meta := p.State.Versions[key]
		item := ChangedView{Collection: key.Collection, ID: key.ID, Deleted: meta.Deleted}
		if key == listKey(key.Collection) && slices.Contains(orderedCollections, key.Collection) {
			// A list's order has a version but no value of its own.
			item.Version = meta.Version
		} else if !meta.Deleted {
			v, e := viewObject(index[key], meta.Version, p.State.Secrets)
			if e != nil {
				return result, e
			}
			item.Value = v.Value
			item.Version = v.Version
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

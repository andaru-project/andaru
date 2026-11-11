package config

import (
	"encoding/json"
	"slices"
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
}
type secretView struct {
	Secret    string  `json:"secret"`
	IsSet     bool    `json:"isSet"`
	UpdatedAt string  `json:"updatedAt"`
	Version   Version `json:"version"`
}
type linkView struct {
	link
	Password *secretView `json:"password,omitempty"`
}

func viewObject(v any, version Version, secrets secretDocument) (ObjectView, error) {
	if l, ok := v.(*link); ok {
		view := linkView{link: *l}
		if l.Password != nil {
			meta, ok := secrets.Scopes["core"][l.Password.Secret]
			if !ok {
				return ObjectView{}, fault("STORE_UNAVAILABLE", "", "Secret reference is unavailable.")
			}
			view.Password = &secretView{Secret: l.Password.Secret, IsSet: true, UpdatedAt: meta.UpdatedAt, Version: meta.Version}
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
func publicSnapshot(s savedState) (SnapshotView, error) {
	links := make([]json.RawMessage, 0, len(s.Config.Links))
	used := map[string]bool{}
	for i := range s.Config.Links {
		l := &s.Config.Links[i]
		v, e := viewObject(l, s.Versions[objectKey{"links", l.ID}].Version, s.Secrets)
		if e != nil {
			return SnapshotView{}, e
		}
		links = append(links, v.Value)
		if l.Password != nil {
			used[l.Password.Secret] = true
		}
	}
	d := struct {
		SchemaVersion int               `json:"schemaVersion"`
		Links         []json.RawMessage `json:"links"`
		Segments      []segment         `json:"segments"`
		Zones         []zone            `json:"zones"`
	}{s.Config.SchemaVersion, links, s.Config.Segments, s.Config.Zones}
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

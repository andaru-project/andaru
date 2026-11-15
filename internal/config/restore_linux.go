package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type RestoreDocuments struct {
	Configuration, ConfigurationSecrets json.RawMessage
}

type RestoreCounts struct{ Created, Updated, Deleted int }

type RestorePreview struct {
	ExpectedSavedVersion                Version
	ConfigurationChanges, SecretChanges RestoreCounts
	UnresolvedSecrets, BlockingSecrets  []string
	Configuration                       json.RawMessage `json:"-"`
}

type RestoreResult struct {
	SavedVersion      Version
	UnresolvedSecrets []string
}

func restoreRequests(decoded decodedBackup) []objectKey {
	if decoded.configuration == nil {
		return nil
	}
	index := objects(decoded.configuration)
	requested := make([]objectKey, 0, len(index))
	for key := range index {
		requested = append(requested, key)
		// Both policy lists share one creation-identity index, including deleted IDs.
		if key.Collection == "policy.input" {
			requested = append(requested, objectKey{"policy.forward", key.ID})
		}
		if key.Collection == "policy.forward" {
			requested = append(requested, objectKey{"policy.input", key.ID})
		}
	}
	return requested
}

func (s *Store) PreviewRestore(ctx context.Context, input RestoreDocuments) (result RestorePreview, err error) {
	decoded, err := decodeBackupDocuments(input)
	if err != nil {
		return result, err
	}
	err = s.read(ctx, "", restoreRequests(decoded), func(current savedState, _ VersionInfo) error {
		n, e := strconvVersion(current.Version)
		if e != nil || n == int64(^uint64(0)>>1) {
			return unavailable()
		}
		_, result, e = prepareRestoration(&current, decoded, Version(fmt.Sprint(n+1)), time.Now().UTC())
		return e
	})
	if err != nil {
		return RestorePreview{}, err
	}
	return result, nil
}

func (s *Store) prepareRestore(ctx context.Context, actor string, expected Version, decoded decodedBackup) (*pendingSave, RestorePreview, error) {
	var empty RestorePreview
	if expected == "" {
		return nil, empty, fault("PRECONDITION_REQUIRED", "/expectedSavedVersion", "Expected saved version is required.")
	}
	if _, err := ParseVersion(string(expected)); err != nil {
		return nil, empty, fault("INVALID_REQUEST", "/expectedSavedVersion", "Invalid saved version.")
	}
	p, current, next, err := s.startSave(ctx, actor, restoreRequests(decoded))
	if err != nil {
		return nil, empty, err
	}
	success := false
	defer func() {
		if !success {
			p.Close()
		}
	}()
	if current.Version != expected {
		return nil, empty, fault("PRECONDITION_FAILED", "/expectedSavedVersion", "The saved configuration changed since restore was previewed.")
	}
	prepared, preview, err := prepareRestoration(&current, decoded, next, time.Now().UTC())
	if err != nil {
		return nil, empty, err
	}
	if len(preview.BlockingSecrets) != 0 {
		return nil, empty, fault("VALIDATION_FAILED", "/configurationSecrets", "Restoration would remove secrets required by bound references; edit the references or select a paired restore.")
	}
	if ctx.Err() != nil {
		return nil, empty, unavailable()
	}
	p.prepared = prepared
	p.deadline = time.Now().Add(2 * time.Second)
	success = true
	return p, preview, nil
}

func (s *Store) Restore(ctx context.Context, actor string, expected Version, input RestoreDocuments) (result RestoreResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	decoded, err := decodeBackupDocuments(input)
	if err != nil {
		return result, err
	}
	p, preview, err := s.prepareRestore(ctx, actor, expected, decoded)
	if err != nil {
		return result, err
	}
	defer func() {
		if e := p.Close(); e != nil {
			result = RestoreResult{}
			err = e
		}
	}()
	saved, err := p.publish(ctx)
	if err != nil {
		return result, err
	}
	return RestoreResult{SavedVersion: saved.SavedVersion, UnresolvedSecrets: preview.UnresolvedSecrets}, nil
}

func prepareRestoration(current *savedState, decoded decodedBackup, next Version, now time.Time) (preparedChange, RestorePreview, error) {
	var empty preparedChange
	preview := RestorePreview{ExpectedSavedVersion: current.Version, UnresolvedSecrets: []string{}, BlockingSecrets: []string{}}
	candidate := current.Config
	if decoded.configuration != nil {
		candidate = *decoded.configuration
		candidate.SchemaVersion = 2
	}
	secrets := current.Secrets
	if decoded.secrets != nil {
		secrets = emptySecrets()
		core := decoded.secrets.Scopes["core"]
		for name, entry := range core {
			secrets.Scopes["core"][name] = secretEntry{Kind: entry.Kind, Value: entry.Value, Version: next, UpdatedAt: now.Format(time.RFC3339Nano)}
			old, exists := current.Secrets.Scopes["core"][name]
			if !exists {
				preview.SecretChanges.Created++
			} else if old.Kind != entry.Kind || old.Value != entry.Value {
				preview.SecretChanges.Updated++
			}
		}
		for name := range current.Secrets.Scopes["core"] {
			if _, ok := core[name]; !ok {
				preview.SecretChanges.Deleted++
			}
		}
	}
	unresolved, blocked := map[string]bool{}, map[string]bool{}
	for i := range candidate.Links {
		reference := candidate.Links[i].Password
		if reference == nil {
			continue
		}
		_, exists := secrets.Scopes["core"][reference.Secret]
		if decoded.configuration != nil && (decoded.secrets == nil || !exists) {
			reference.Unresolved = true
		}
		if reference.Unresolved {
			unresolved[reference.Secret] = true
		} else if !exists {
			blocked[reference.Secret] = true
		}
	}
	for name := range unresolved {
		preview.UnresolvedSecrets = append(preview.UnresolvedSecrets, name)
	}
	for name := range blocked {
		preview.BlockingSecrets = append(preview.BlockingSecrets, name)
	}
	slices.Sort(preview.UnresolvedSecrets)
	slices.Sort(preview.BlockingSecrets)
	if err := validateSecrets(secrets, next); err != nil {
		return empty, preview, boundaryFault(err)
	}
	configBody := current.configBody
	if decoded.configuration != nil {
		var err error
		configBody, err = canonical(candidate)
		if err != nil {
			return empty, preview, boundaryFault(err)
		}
	}
	secretBody := current.secretBody
	if decoded.secrets != nil {
		var err error
		secretBody, err = canonical(secrets)
		if err != nil {
			return empty, preview, boundaryFault(err)
		}
	}
	preview.Configuration = configBody
	state := *current
	state.Version, state.Config, state.Secrets = next, candidate, secrets
	state.configBody, state.secretBody = configBody, secretBody
	prepared := preparedChange{State: state, ConfigBody: configBody, SecretBody: secretBody, Changed: []objectChange{}}
	before, after := objects(&current.Config), objects(&candidate)
	changes := map[objectKey]objectChange{}
	if decoded.configuration != nil {
		for key := range before {
			if _, exists := after[key]; !exists {
				changes[key] = objectChange{Collection: key.Collection, ID: key.ID, Deleted: true}
				preview.ConfigurationChanges.Deleted++
			}
		}
		for key, value := range after {
			if key.Collection == "policy.input" || key.Collection == "policy.forward" {
				other := "policy.input"
				if key.Collection == other {
					other = "policy.forward"
				}
				if _, exists := current.Versions[objectKey{other, key.ID}]; exists {
					return empty, preview, fault("VALIDATION_FAILED", "/configuration", "An imported rule identity belongs to another collection in destination history.")
				}
			}
			old, exists := before[key]
			if !exists {
				_, known := current.Versions[key]
				changes[key] = objectChange{Collection: key.Collection, ID: key.ID, Created: !known && key != listKey(key.Collection)}
				preview.ConfigurationChanges.Created++
			} else {
				oldBody, err := canonical(old)
				if err != nil {
					return empty, preview, err
				}
				newBody, err := canonical(value)
				if err != nil {
					return empty, preview, err
				}
				if !bytes.Equal(oldBody, newBody) {
					changes[key] = objectChange{Collection: key.Collection, ID: key.ID}
					preview.ConfigurationChanges.Updated++
				}
			}
		}
		for _, collection := range orderedCollections {
			if !slices.Equal(orderedIDs(&current.Config, collection), orderedIDs(&candidate, collection)) {
				key := listKey(collection)
				changes[key] = objectChange{Collection: collection, ID: collection}
			}
		}
	}
	if decoded.secrets != nil {
		for _, l := range candidate.Links {
			if l.Password != nil && !l.Password.Unresolved {
				key := objectKey{"links", l.ID}
				if _, changed := changes[key]; !changed {
					changes[key] = objectChange{Collection: key.Collection, ID: key.ID}
				}
			}
		}
	}
	for key, change := range changes {
		prepared.State.Versions[key] = objectVersion{Version: next, Deleted: change.Deleted}
		prepared.Changed = append(prepared.Changed, change)
	}
	slices.SortFunc(prepared.Changed, func(a, b objectChange) int {
		return compareKeys(objectKey{a.Collection, a.ID}, objectKey{b.Collection, b.ID})
	})
	return prepared, preview, nil
}

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/authz"
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

// PreparedRestore keeps the existing configuration prepare/publication transaction open
// while the engine journals a coordinated config+identity restore.
type PreparedRestore struct {
	p       *pendingSave
	preview RestorePreview
	target  RestoreDocuments
	closed  bool
}

func (p *PreparedRestore) Preview() RestorePreview { return p.preview }
func (p *PreparedRestore) TargetVersion() Version  { return p.p.prepared.State.Version }

func (p *PreparedRestore) TargetDocuments() RestoreDocuments {
	return RestoreDocuments{Configuration: slices.Clone(p.target.Configuration), ConfigurationSecrets: slices.Clone(p.target.ConfigurationSecrets)}
}

func (p *PreparedRestore) Publish(ctx context.Context) (RestoreResult, error) {
	if p == nil || p.closed {
		return RestoreResult{}, unavailable()
	}
	saved, err := p.p.publish(ctx)
	if err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{SavedVersion: saved.SavedVersion, UnresolvedSecrets: slices.Clone(p.preview.UnresolvedSecrets)}, nil
}

func (p *PreparedRestore) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	return p.p.Close()
}

func restoreRequests(decoded decodedBackup) []objectKey {
	if decoded.configuration == nil {
		return nil
	}
	return slices.Collect(maps.Keys(objects(decoded.configuration)))
}

// heldRestore refuses a restore that would save provisional choices as the user's (settings safe mode): a backup
// whose configuration needs decisions, or, while the head needs them, a restore without a configuration, which saves
// the head's own again.
func heldRestore(current *savedState, decoded decodedBackup) error {
	if decoded.needsDecisions {
		return fault("VALIDATION_FAILED", "/configuration", "This backup needs decisions to read in this Andaru, and restoring such a backup is not offered yet.")
	}
	if decoded.configuration == nil && len(current.questions) > 0 {
		return ErrUpgradePending
	}
	return nil
}

func (s *Store) PreviewRestore(ctx context.Context, input RestoreDocuments) (result RestorePreview, err error) {
	decoded, err := decodeBackupDocuments(input)
	if err != nil {
		return result, err
	}
	err = s.read(ctx, "", restoreRequests(decoded), func(current savedState, _ VersionInfo) error {
		if e := heldRestore(&current, decoded); e != nil {
			return e
		}
		n, e := strconvVersion(current.Version)
		if e != nil || n == int64(^uint64(0)>>1) {
			return unavailable()
		}
		_, result, e = prepareRestoration(&current, decoded, Version(fmt.Sprint(n+1)), time.Now().UTC(), s.catalog, s.managementHosts)
		return e
	})
	if err != nil {
		return RestorePreview{}, err
	}
	return result, nil
}

func (s *Store) prepareRestore(ctx context.Context, actor authz.Actor, expected Version, decoded decodedBackup) (*pendingSave, RestorePreview, error) {
	var empty RestorePreview
	if expected == "" {
		return nil, empty, fault("PRECONDITION_REQUIRED", "/expectedSavedVersion", "Expected saved version is required.")
	}
	if _, err := ParseVersion(string(expected)); err != nil {
		return nil, empty, fault("INVALID_REQUEST", "/expectedSavedVersion", "Invalid saved version.")
	}
	if decoded.needsDecisions {
		return nil, empty, heldRestore(&savedState{}, decoded) // said before a stale version is
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
	if err = heldRestore(&current, decoded); err != nil {
		return nil, empty, err
	}
	prepared, preview, err := prepareRestoration(&current, decoded, next, time.Now().UTC(), s.catalog, s.managementHosts)
	if err != nil {
		return nil, empty, err
	}
	if len(preview.BlockingSecrets) != 0 {
		return nil, empty, fault("VALIDATION_FAILED", "/configurationSecrets", "Restoration would leave bound references without their secret, or holding a secret of another kind (a link's password, a channel's notification secret); edit or rename the references, or select a paired restore.")
	}
	if ctx.Err() != nil {
		return nil, empty, unavailable()
	}
	p.prepared = prepared
	p.deadline = time.Now().Add(2 * time.Second)
	success = true
	return p, preview, nil
}

func (s *Store) PrepareRestore(ctx context.Context, actor authz.Actor, expected Version, input RestoreDocuments) (*PreparedRestore, error) {
	decoded, err := decodeBackupDocuments(input)
	if err != nil {
		return nil, err
	}
	pending, preview, err := s.prepareRestore(ctx, actor, expected, decoded)
	if err != nil {
		return nil, err
	}
	return &PreparedRestore{
		p:       pending,
		preview: preview,
		target:  RestoreDocuments{Configuration: slices.Clone(pending.prepared.ConfigBody), ConfigurationSecrets: slices.Clone(pending.prepared.SecretBody)},
	}, nil
}

func (s *Store) Restore(ctx context.Context, actor authz.Actor, expected Version, input RestoreDocuments) (result RestoreResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	prepared, err := s.PrepareRestore(ctx, actor, expected, input)
	if err != nil {
		return result, err
	}
	defer func() {
		if e := prepared.Close(); e != nil {
			result = RestoreResult{}
			err = e
		}
	}()
	return prepared.Publish(ctx)
}

func (s *Store) RestoreMatches(ctx context.Context, version Version, target RestoreDocuments) (bool, error) {
	var savedVersion Version
	var configBody, secretBody []byte
	err := s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		savedVersion = state.Version
		// The stored bytes: a journal written by the engine before names a document of the form before.
		configBody = state.storedBody
		secretBody = state.secretBody
		return nil
	})
	if err != nil {
		return false, err
	}
	same := bytes.Equal(bytes.TrimRight(configBody, "\n"), bytes.TrimRight(target.Configuration, "\n"))
	if !same && savedVersion == version {
		// This engine finished a restore the engine before had journalled: the store holds the document as it reads now.
		if d, _, _, e := decodeSaved(target.Configuration); e == nil {
			now, e := canonical(d)
			same = e == nil && bytes.Equal(now, configBody)
		}
	}
	return savedVersion == version && same && sameSecrets(secretBody, target.ConfigurationSecrets), nil
}

// sameSecrets compares two secret documents of the store's own form by what each secret is: its kind, value and
// version. A restore prepared again from its journal saves the same secrets at another time.
func sameSecrets(a, b []byte) bool {
	if bytes.Equal(bytes.TrimRight(a, "\n"), bytes.TrimRight(b, "\n")) {
		return true
	}
	var x, y secretDocument
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return maps.EqualFunc(x.Scopes, y.Scopes, func(xs, ys map[string]secretEntry) bool {
		return maps.EqualFunc(xs, ys, func(s, t secretEntry) bool {
			return s.Kind == t.Kind && s.Value == t.Value && s.Version == t.Version
		})
	})
}

func prepareRestoration(current *savedState, decoded decodedBackup, next Version, now time.Time, cat *addon.Catalog, managementHosts []string) (preparedChange, RestorePreview, error) {
	var empty preparedChange
	preview := RestorePreview{ExpectedSavedVersion: current.Version, UnresolvedSecrets: []string{}, BlockingSecrets: []string{}}
	candidate := current.Config
	if decoded.configuration != nil {
		candidate = *decoded.configuration
		candidate.Addons = slices.Clone(candidate.Addons)
		if !decoded.ownSecrets {
			// Backup restore: validate every cataloged enabled add-on (plus WebUI separation) while
			// allowing retired/disabled add-ons from cross-version backups to restore cleanly.
			// Schema upgrade (ownSecrets=true) skips catalog/WebUI checks so it never deadlocks under
			// ErrUpgradePending; buildWebUI suppresses conflicting links and render.Network gates apply.
			if errs := validateRestoredAddonsAgainstCatalog(cat, &candidate, managementHosts); len(errs) > 0 {
				return empty, preview, boundaryFault(validationError(errs))
			}
		}
	}
	secrets := current.Secrets
	secretsPruned := false
	if decoded.secrets == nil && decoded.configuration != nil {
		// Prune add-on secret scopes whose add-ons are absent from the restored configuration so orphaned
		// scopes do not linger invisibly or conflict with future reinstalls.
		presentAddons := make(map[string]bool, len(candidate.Addons))
		for _, a := range candidate.Addons {
			presentAddons[addonSecretScope(a.ID)] = true
		}
		pruned := emptySecrets()
		for scope, entries := range current.Secrets.Scopes {
			if scope != "core" && !presentAddons[scope] {
				preview.SecretChanges.Deleted += len(entries)
				secretsPruned = true
				continue
			}
			pruned.Scopes[scope] = maps.Clone(entries)
		}
		if secretsPruned {
			secrets = pruned
		}
	}
	if decoded.secrets != nil {
		secrets = emptySecrets()
		for scope, entries := range decoded.secrets.Scopes {
			if scope != "core" && len(entries) == 0 {
				continue
			}
			if secrets.Scopes[scope] == nil {
				secrets.Scopes[scope] = map[string]secretEntry{}
			}
			oldScope := current.Secrets.Scopes[scope]
			for name, entry := range entries {
				secrets.Scopes[scope][name] = secretEntry{Kind: entry.Kind, Value: entry.Value, Version: next, UpdatedAt: now.Format(time.RFC3339Nano)}
				old, exists := oldScope[name]
				if !exists {
					preview.SecretChanges.Created++
				} else if old.Kind != entry.Kind || old.Value != entry.Value {
					preview.SecretChanges.Updated++
				}
			}
		}
		for scope, oldEntries := range current.Secrets.Scopes {
			newEntries := decoded.secrets.Scopes[scope]
			for name := range oldEntries {
				if _, ok := newEntries[name]; !ok {
					preview.SecretChanges.Deleted++
				}
			}
		}
	}
	unresolved, blocked := map[string]bool{}, map[string]bool{}
	for _, h := range heldSecrets(&candidate) {
		reference := h.ref
		scoped := secrets.Scopes[h.scope]
		entry, exists := scoped[reference.Secret]
		if decoded.configuration != nil && !decoded.ownSecrets && (decoded.secrets == nil || !exists) {
			h.setUnresolved()
		}
		label := reference.Secret
		if h.scope != "core" {
			label = h.scope + "/" + reference.Secret
		}
		switch {
		case exists && entry.Kind != secretKind(h.key.Collection):
			blocked[label] = true // a restore keeps the binding a save keeps: a channel never holds a password
		case reference.Unresolved:
			unresolved[label] = true
		case !exists:
			blocked[label] = true
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
	if decoded.secrets != nil && !decoded.ownSecrets {
		if errs := slices.Concat(checkAllPKIKeys(&candidate, secrets), checkAllWireGuardKeys(&candidate, secrets), checkIPsecSecrets(&candidate, secrets, nil)); len(errs) > 0 {
			return empty, preview, &Fault{Code: "VALIDATION_FAILED", Details: errs}
		}
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
	if decoded.secrets != nil || secretsPruned {
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
		for _, h := range heldSecrets(&candidate) {
			if !h.ref.Unresolved {
				if _, changed := changes[h.key]; !changed {
					changes[h.key] = objectChange{Collection: h.key.Collection, ID: h.key.ID}
				}
			}
		}
	}
	carryLegacy(&prepared.State, after, changes)
	for key, change := range changes {
		prepared.State.Versions[key] = objectVersion{Version: next, Deleted: change.Deleted}
		prepared.Changed = append(prepared.Changed, change)
	}
	slices.SortFunc(prepared.Changed, func(a, b objectChange) int {
		return compareKeys(objectKey{a.Collection, a.ID}, objectKey{b.Collection, b.ID})
	})
	return prepared, preview, nil
}

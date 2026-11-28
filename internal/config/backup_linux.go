package config

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
)

//go:embed backup-secret-schema.json
var backupSecretSchema []byte

type BackupDocuments struct {
	SavedVersion                        Version
	Configuration, ConfigurationSecrets json.RawMessage
}

type backupSecretEntry struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type backupSecretDocument struct {
	SchemaVersion int                                     `json:"schemaVersion"`
	Scopes        map[string]map[string]backupSecretEntry `json:"secrets"`
}

type decodedBackup struct {
	configuration *declaration
	secrets       *backupSecretDocument
	// ownSecrets says the configuration is read against the store's own secrets, as an upgrade's is, so its
	// references stay resolved.
	ownSecrets bool
	// needsDecisions says the configuration reads with provisional choices; restoring it would save them as the
	// user's, and answering them on restore is not offered yet.
	needsDecisions bool
}

// Export captures logical documents from one saved pair, never public views or SQL rows.
func (s *Store) Export(ctx context.Context, includeSecrets bool) (result BackupDocuments, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		result.SavedVersion = state.Version
		result.Configuration = state.configBody
		// A head that waits for decisions is exported as saved, so a restore of the backup still asks them.
		if len(state.questions) > 0 {
			result.Configuration = state.storedBody
		}
		if includeSecrets {
			result.ConfigurationSecrets, err = exportBackupSecrets(state.Secrets)
			return err
		}
		return nil
	})
	if err != nil {
		return BackupDocuments{}, err
	}
	return result, nil
}

// Portable turns the documents a prepared restore records (PreparedRestore.TargetDocuments), with target its saved
// version, back into a backup's, for a restore that is prepared again. A restore of secrets gives every secret its
// target version; one of the configuration alone records the store's secrets as they were, which then stay out.
func (input RestoreDocuments) Portable(target Version) (RestoreDocuments, error) {
	if input.ConfigurationSecrets == nil {
		return input, nil
	}
	var stored secretDocument
	if err := json.Unmarshal(input.ConfigurationSecrets, &stored); err != nil {
		return input, boundaryFault(err)
	}
	total := 0
	matched := false
	for _, entries := range stored.Scopes {
		total += len(entries)
		for _, entry := range entries {
			if entry.Version == target {
				matched = true
			}
		}
	}
	restored := total == 0 || matched
	if !restored {
		return RestoreDocuments{Configuration: input.Configuration}, nil
	}
	secrets, err := exportBackupSecrets(stored)
	return RestoreDocuments{Configuration: input.Configuration, ConfigurationSecrets: secrets}, err
}

func exportBackupSecrets(doc secretDocument) ([]byte, error) {
	scopes := make(map[string]map[string]backupSecretEntry, len(doc.Scopes))
	for scope, entries := range doc.Scopes {
		if scope != "core" && len(entries) == 0 {
			continue
		}
		copied := make(map[string]backupSecretEntry, len(entries))
		for name, entry := range entries {
			copied[name] = backupSecretEntry{Kind: entry.Kind, Value: entry.Value}
		}
		scopes[scope] = copied
	}
	if scopes["core"] == nil {
		scopes["core"] = map[string]backupSecretEntry{}
	}
	return canonical(backupSecretDocument{SchemaVersion: 1, Scopes: scopes})
}

// RestorableHere reports whether a backup's documents restore in this Andaru: they read, and without provisional
// choices (settings safe mode). A failure that is not about the documents is returned.
func RestorableHere(input RestoreDocuments) (bool, error) {
	decoded, err := decodeBackupDocuments(input)
	var f *Fault
	if errors.As(err, &f) && (f.Code == "VALIDATION_FAILED" || f.Code == "INVALID_REQUEST") {
		return false, nil
	}
	return err == nil && !decoded.needsDecisions, err
}

func ValidateBackupDocuments(input RestoreDocuments) error {
	_, err := decodeBackupDocuments(input)
	return err
}

func decodeBackupDocuments(input RestoreDocuments) (decodedBackup, error) {
	var decoded decodedBackup
	if input.Configuration == nil && input.ConfigurationSecrets == nil {
		return decoded, fault("INVALID_REQUEST", "", "Select at least one backup store.")
	}
	if input.Configuration != nil {
		// A backup taken before schema 4 restores as the configuration it is now.
		d, _, _, questions, err := decodeSavedWith(input.Configuration, nil)
		if err != nil {
			return decoded, boundaryFault(err)
		}
		decoded.configuration, decoded.needsDecisions = &d, len(questions) > 0
	}
	if input.ConfigurationSecrets != nil {
		instance, err := decodeStrictLimit(input.ConfigurationSecrets, documentLimit)
		if err != nil {
			return decoded, boundaryFault(err)
		}
		schema, err := schemas()
		if err != nil {
			return decoded, unavailable()
		}
		if err = schema.backupSecret.Validate(instance); err != nil {
			return decoded, fault("VALIDATION_FAILED", "/configurationSecrets", "Invalid configuration-secret backup document.")
		}
		var secrets backupSecretDocument
		if err = json.Unmarshal(input.ConfigurationSecrets, &secrets); err != nil {
			return decoded, fault("INVALID_REQUEST", "/configurationSecrets", "Invalid configuration-secret backup document.")
		}
		for _, entries := range secrets.Scopes {
			for _, entry := range entries {
				if len(entry.Value) > maxSecretBytes || strings.ContainsRune(entry.Value, 0) {
					return decoded, fault("VALIDATION_FAILED", "/configurationSecrets", "Invalid configuration-secret value.")
				}
			}
		}
		decoded.secrets = &secrets
	}
	return decoded, nil
}

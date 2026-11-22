package config

import (
	"context"
	_ "embed"
	"encoding/json"
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
}

// Export captures logical documents from one saved pair, never public views or SQL rows.
func (s *Store) Export(ctx context.Context, includeSecrets bool) (result BackupDocuments, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		result.SavedVersion = state.Version
		result.Configuration = state.configBody
		if includeSecrets {
			core := make(map[string]backupSecretEntry, len(state.Secrets.Scopes["core"]))
			for name, entry := range state.Secrets.Scopes["core"] {
				core[name] = backupSecretEntry{Kind: entry.Kind, Value: entry.Value}
			}
			result.ConfigurationSecrets, err = canonical(backupSecretDocument{SchemaVersion: 1, Scopes: map[string]map[string]backupSecretEntry{"core": core}})
			return err
		}
		return nil
	})
	if err != nil {
		return BackupDocuments{}, err
	}
	return result, nil
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
		// Restoring it would save its provisional choices as the user's; answering them on restore is not offered yet.
		if len(questions) > 0 {
			return decoded, fault("VALIDATION_FAILED", "/configuration", "This backup needs decisions to read in this Andaru, and restoring such a backup is not offered yet.")
		}
		decoded.configuration = &d
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
		for _, entry := range secrets.Scopes["core"] {
			if len(entry.Value) > 4096 || strings.ContainsRune(entry.Value, 0) {
				return decoded, fault("VALIDATION_FAILED", "/configurationSecrets", "Invalid configuration-secret value.")
			}
		}
		decoded.secrets = &secrets
	}
	return decoded, nil
}

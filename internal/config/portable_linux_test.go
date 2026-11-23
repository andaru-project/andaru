package config

import (
	"strings"
	"testing"
)

// A journal records secrets in the store's own form. Those a restore of secrets wrote carry its target version and
// return as a backup's; those a restore of the configuration alone recorded as the store held them stay out, so a
// restore prepared again leaves them as they are.
func TestPortableKeepsOnlyRestoredSecrets(t *testing.T) {
	stored := func(version string) []byte {
		return []byte(`{"schemaVersion":1,"secrets":{"core":{"WAN_PASSWORD":{"kind":"password","value":"secret","updatedAt":"2026-11-22T00:00:00Z","version":"` + version + `"}}}}`)
	}
	restored, err := RestoreDocuments{Configuration: []byte("{}"), ConfigurationSecrets: stored("5")}.Portable("5")
	if err != nil || !strings.Contains(string(restored.ConfigurationSecrets), `"WAN_PASSWORD":{"kind":"password","value":"secret"}`) {
		t.Fatalf("restored secrets: %s %v", restored.ConfigurationSecrets, err)
	}
	if err = ValidateBackupDocuments(RestoreDocuments{ConfigurationSecrets: restored.ConfigurationSecrets}); err != nil {
		t.Fatalf("not a backup's secrets: %v", err)
	}
	kept, err := RestoreDocuments{Configuration: []byte("{}"), ConfigurationSecrets: stored("3")}.Portable("5")
	if err != nil || kept.ConfigurationSecrets != nil || string(kept.Configuration) != "{}" {
		t.Fatalf("secrets the restore did not write: %+v %v", kept, err)
	}
}

package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/backup"
	"slices"
	"testing"
)

func TestBackupCommandParsingIsExact(t *testing.T) {
	valid := []struct {
		args   []string
		verb   string
		stores []string
	}{
		{[]string{"create", "--output", "/safe/backup.zip"}, "create", nil},
		{[]string{"create", "--include-secrets", "--output", "/safe/backup.zip.age"}, "create", nil},
		{[]string{"create", "--include-credentials", "--output", "/safe/credentials.age"}, "create", nil},
		{[]string{"inspect", "--input", "/safe/backup.zip"}, "inspect", nil},
		{[]string{"restore", "--input", "/safe/backup.zip", "--stores", "configuration,configurationSecrets,users,userCredentials", "--operation-id", "restore-1"}, "restore", []string{"configuration", "configurationSecrets", "users", "userCredentials"}},
	}
	for _, tc := range valid {
		command, err := parseBackupCommand(tc.args)
		if err != nil || command.verb != tc.verb || !slices.Equal(command.stores, tc.stores) {
			t.Fatalf("parse %q: %+v, %v", tc.args, command, err)
		}
	}
	invalid := [][]string{
		nil,
		{"create"},
		{"create", "--output", "a", "--output", "b"},
		{"create", "--output", "a", "extra"},
		{"inspect", "--input", "a", "--include-secrets"},
		{"restore", "--input", "a"},
		{"restore", "--input", "a", "--stores", "configuration,configuration"},
		{"restore", "--input", "a", "--stores", "configurationSecrets,unknown"},
		{"restore", "--input", "a", "--stores", "users", "--operation-id", "../escape"},
		{"restore", "--input", "a", "--stores", ""},
	}
	for _, args := range invalid {
		if command, err := parseBackupCommand(args); err == nil {
			t.Fatalf("accepted %q as %+v", args, command)
		}
	}
}

func TestBackupErrorEnvelopeMustBeStructured(t *testing.T) {
	valid, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "INVALID_ARCHIVE", "message": "The archive is invalid.", "details": []any{}}})
	if !validAPIError(valid) {
		t.Fatal("rejected contract error envelope")
	}
	for _, raw := range [][]byte{
		[]byte(`{"error":{"code":"INVALID_ARCHIVE","message":"bad"}}`),
		[]byte(`{"error":{"code":"INVALID_ARCHIVE","message":"bad","details":[],"secret":"value"}}`),
		[]byte(`{"error":{"code":"","message":"bad","details":[]}}`),
		[]byte(`not-json`),
	} {
		if validAPIError(raw) {
			t.Fatalf("accepted invalid error envelope: %s", raw)
		}
	}
}

// The CLI reads manifest formats 1 to 3: format 3 holds what format 2 does, users second (#233).
func TestValidManifestFormats(t *testing.T) {
	digest := strings.Repeat("a", 64)
	payload := backup.Payload{SchemaVersion: 1, Size: 10, SHA256: digest}
	live := 3
	manifest := func(format int, scopes ...string) backup.Manifest {
		payloads := map[string]backup.Payload{}
		for _, s := range scopes {
			payloads[map[string]string{"configuration": "config.json", "users": "users.json"}[s]] = payload
		}
		return backup.Manifest{FormatVersion: format, BackupID: strings.Repeat("1", 32), CreatedAt: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC),
			AndaruVersion: "1.0.0", SavedVersion: "7", Scopes: scopes, Payloads: payloads,
			Network: backup.NetworkObservation{Live: &live, LiveSavedVersion: "6"}, SavedButUnappliedAreas: []string{}}
	}
	for _, c := range []struct {
		m    backup.Manifest
		want bool
	}{
		{manifest(1, "configuration"), true},
		{manifest(1, "configuration", "users"), false},
		{manifest(2, "configuration", "users"), true},
		{manifest(2, "configuration"), false},
		{manifest(3, "configuration", "users"), true},
		{manifest(3, "configuration"), false},
		{manifest(4, "configuration", "users"), false},
	} {
		if got := validManifest(c.m); got != c.want {
			t.Errorf("format %d, scopes %q: %v", c.m.FormatVersion, c.m.Scopes, got)
		}
	}
}

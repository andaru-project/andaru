package main

import (
	"encoding/json"
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

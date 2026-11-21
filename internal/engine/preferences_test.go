package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreferencesAreOneBoundedJSONObjectPerAccount(t *testing.T) {
	stateDir := t.TempDir()
	const one, other = "0657cce4-d4f9-4d1e-9cae-53a99855bb10", "15de97d7-8f96-433a-a417-b4e939acc832"
	read := func(accountID string) string {
		t.Helper()
		document, err := readPreferences(stateDir, accountID)
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}

	if got := read(one); got != "{}" {
		t.Fatalf("an account without preferences reads %q", got)
	}
	// Anything JSON.stringify writes is the client's to keep: null, fractions and nesting included.
	const kept = `{"dashboard":{"order":["interfaces"],"hidden":[],"ratio":0.35,"selected":null}}`
	for _, document := range []string{`{"dashboard":{"order":["changes"]}}`, kept} {
		if err := writePreferences(stateDir, one, []byte(document)); err != nil {
			t.Fatalf("%s: %v", document, err)
		}
	}
	if got := read(one); got != kept {
		t.Fatalf("the second save did not replace the first: %q", got)
	}
	// One account's document is not another's.
	if err := writePreferences(stateDir, other, []byte(`{"theirs":true}`)); err != nil {
		t.Fatal(err)
	}
	if read(one) != kept || read(other) != `{"theirs":true}` {
		t.Fatalf("accounts share a document: %q %q", read(one), read(other))
	}

	atLimit := `{"a":"` + strings.Repeat("x", maxPreferencesBytes-8) + `"}`
	if err := writePreferences(stateDir, other, []byte(atLimit)); err != nil || len(atLimit) != maxPreferencesBytes {
		t.Fatalf("a document of exactly the limit (%d bytes) was refused: %v", len(atLimit), err)
	}
	for name, document := range map[string]string{
		"an array":            "[]",
		"a string":            `"x"`,
		"null":                "null",
		"nothing":             "",
		"two values":          "{}{}",
		"broken JSON":         `{"a":`,
		"a duplicate name":    `{"a":1,"a":2}`,
		"invalid UTF-8":       "{\"a\":\"\xff\"}",
		"a form feed":         "\f{}",
		"one byte over limit": atLimit[:len(atLimit)-2] + `x"}`,
	} {
		if err := writePreferences(stateDir, one, []byte(document)); !errors.Is(err, errInvalidPreferences) {
			t.Errorf("%s was not refused: %v", name, err)
		}
	}
	if got := read(one); got != kept {
		t.Fatalf("a refused save changed the document: %q", got)
	}

	// An account ID is a file name here, so it must be nothing else.
	for _, accountID := range []string{"", ".", "..", "../identity", "a/b", ".hidden"} {
		if err := writePreferences(stateDir, accountID, []byte("{}")); err == nil || errors.Is(err, errInvalidPreferences) {
			t.Errorf("account ID %q was not refused as a name: %v", accountID, err)
		}
		if _, err := readPreferences(stateDir, accountID); err == nil {
			t.Errorf("account ID %q was read", accountID)
		}
	}

	// Nothing but the two documents is left: no temporary file, nothing outside the directory, nothing readable by others.
	entries, err := os.ReadDir(filepath.Join(stateDir, "preferences"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("preferences directory holds %v, %v", entries, err)
	}
	for _, entry := range entries {
		if info, err := entry.Info(); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s is not a private file: %v %v", entry.Name(), info, err)
		}
	}
	if top, err := os.ReadDir(stateDir); err != nil || len(top) != 1 {
		t.Fatalf("something was written outside the preferences directory: %v %v", top, err)
	}

	removePreferences(stateDir, one)
	if read(one) != "{}" || read(other) != atLimit {
		t.Fatal("removing one account's preferences did not remove exactly those")
	}
}

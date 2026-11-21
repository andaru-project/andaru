package engine

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// maxPreferencesBytes bounds one account's preference document.
const maxPreferencesBytes = 16 << 10

var errInvalidPreferences = errors.New("preferences are not one JSON object within the limit")

// Preferences are what a client keeps for an account, such as a dashboard layout: one JSON object per account.
// Nothing in Andaru acts on their content. They are files beside the identity database, not rows in it: a new
// table changes that database's layout, and the previous image, booted again after a rollback, refuses a layout
// it does not know and would leave nobody able to sign in.
func preferencesPath(stateDir, accountID string) (string, error) {
	// The ID becomes a file name, so it must be what every account ID is and nothing else.
	if uuid.Validate(accountID) != nil || len(accountID) != 36 {
		return "", errors.New("account ID is not a UUID")
	}
	return filepath.Join(stateDir, "preferences", accountID+".json"), nil
}

// readPreferences returns the account's document, an empty object when it has none.
func readPreferences(stateDir, accountID string) ([]byte, error) {
	path, err := preferencesPath(stateDir, accountID)
	if err != nil {
		return nil, err
	}
	document, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte("{}"), nil
	}
	return document, err
}

// writePreferences replaces the account's document, leaving the old one or the whole new one. The directory is
// not synced: after a power loss a layout may be the one before, which is not worth a second sync per save. A
// crash between the two steps leaves a temporary file of at most 16 KiB that nothing reads.
func writePreferences(stateDir, accountID string, document []byte) (err error) {
	if len(document) > maxPreferencesBytes || !jsonObject(document) {
		return errInvalidPreferences
	}
	path, err := preferencesPath(stateDir, accountID)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(document); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// removePreferences drops a deleted account's document. A failure leaves at most 16 KiB behind, so it is not reported.
func removePreferences(stateDir, accountID string) {
	if path, err := preferencesPath(stateDir, accountID); err == nil {
		_ = os.Remove(path)
	}
}

// jsonObject reports whether raw is exactly one JSON object: any JSON a client may write, with unique names and
// valid UTF-8. The configuration's stricter token profile does not apply to a document that is the client's own.
func jsonObject(raw []byte) bool {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw), jsontext.AllowDuplicateNames(false), jsontext.AllowInvalidUTF8(false))
	value, err := decoder.ReadValue()
	if err != nil || value.Kind() != '{' {
		return false
	}
	_, err = decoder.ReadToken()
	return err == io.EOF
}

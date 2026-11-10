package config

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type secretEntry struct {
	Kind      string  `json:"kind"`
	Value     string  `json:"value"`
	UpdatedAt string  `json:"updatedAt"`
	Version   Version `json:"version"`
}
type secretDocument struct {
	SchemaVersion int                               `json:"schemaVersion"`
	Scopes        map[string]map[string]secretEntry `json:"secrets"`
}

func emptySecrets() secretDocument {
	return secretDocument{SchemaVersion: 1, Scopes: map[string]map[string]secretEntry{"core": {}}}
}
func validSecretName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func secretPrecondition(doc secretDocument, pw map[string]any, path string) *Fault {
	name, ok := pw["secret"].(string)
	if !ok || !validSecretName(name) {
		return nil
	}
	raw, exists := pw["expectedVersion"]
	if !exists {
		return fault("PRECONDITION_REQUIRED", path, "Expected secret version is required.")
	}
	text, ok := raw.(string)
	if !ok {
		return fault("INVALID_REQUEST", path, "Invalid secret version.")
	}
	v, e := ParseVersion(text)
	if e != nil {
		return fault("INVALID_REQUEST", path, "Invalid secret version.")
	}
	actual := Version("0")
	if old, ok := doc.Scopes["core"][name]; ok {
		actual = old.Version
	}
	if v != actual {
		f := fault("PRECONDITION_FAILED", "", "")
		f.Current = &Current{Kind: "secret", Name: name, Version: actual}
		return f
	}
	return nil
}
func validateSecrets(doc secretDocument, head Version) error {
	raw, e := json.Marshal(doc)
	if e != nil {
		return e
	}
	if len(raw) > documentLimit {
		return errTooLarge
	}
	instance, e := decodeStrictLimit(raw, documentLimit)
	if e != nil {
		return e
	}
	s, e := schemas()
	if e != nil {
		return e
	}
	if e = s.secret.Validate(instance); e != nil {
		return validationError{{Path: "", Message: "Invalid secret document."}}
	}
	h, e := strconv.ParseInt(string(head), 10, 64)
	if e != nil {
		return e
	}
	for name, v := range doc.Scopes["core"] {
		n, e := strconv.ParseInt(string(v.Version), 10, 64)
		_, te := time.Parse(time.RFC3339Nano, v.UpdatedAt)
		if !validSecretName(name) || e != nil || n <= 0 || n > h || te != nil || len(v.Value) > 4096 || strings.ContainsRune(v.Value, 0) {
			return validationError{{Path: "", Message: "Invalid secret value or version."}}
		}
	}
	return nil
}

package config

import (
	"encoding/json"
	"fmt"
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

// checkResolvedReferences gates an apply, so it covers links alone: channels take effect without an apply. A channel
// a restore left unresolved may be saved with that reference unchanged, and sends nothing until the secret is set.
func checkResolvedReferences(d *declaration) error {
	var details []FieldError
	for i, l := range d.Links {
		if l.Password != nil && l.Password.Unresolved {
			details = append(details, FieldError{Path: "/links/" + strconv.Itoa(i) + "/password", Message: "Secret " + l.Password.Secret + " is unresolved; explicitly bind or set it before applying."})
		}
	}
	if len(details) != 0 {
		return &Fault{Code: "VALIDATION_FAILED", Details: details}
	}
	return nil
}

// heldSecret is a secret reference of a declaration, with the object that holds it and its path.
type heldSecret struct {
	key  objectKey
	path string
	ref  *secretReference
}

// heldSecrets are every secret reference of d.
func heldSecrets(d *declaration) []heldSecret {
	var out []heldSecret
	for i := range d.Links {
		if l := &d.Links[i]; l.Password != nil {
			out = append(out, heldSecret{objectKey{"links", l.ID}, fmt.Sprintf("/links/%d/password", i), l.Password})
		}
	}
	for i := range d.Notifications.Channels {
		c := &d.Notifications.Channels[i]
		for _, s := range c.secrets() {
			if s.ref != nil {
				out = append(out, heldSecret{objectKey{"notifications.channels", c.ID}, fmt.Sprintf("/notifications/channels/%d%s", i, s.path), s.ref})
			}
		}
	}
	return out
}

// secretField is a place in a written object that may hold a secret reference: parent[name], at path below the object.
type secretField struct {
	path   string
	parent map[string]any
	name   string
}

// secretKind is the kind of secret a collection holds. A secret is bound to its kind: a link's password and a channel's
// secrets are changed with different capabilities, so neither may reach the other's.
func secretKind(collection string) string {
	if collection == "notifications.channels" {
		return "notification"
	}
	return "password"
}

func secretFields(collection string, value map[string]any) []secretField {
	switch collection {
	case "links":
		return []secretField{{"/password", value, "password"}}
	case "notifications.channels":
		var out []secretField
		for _, kind := range []struct {
			name   string
			fields []string
		}{{"webhook", []string{"url", "authorization"}}, {"ntfy", []string{"token"}}, {"pushover", []string{"token", "user"}}, {"telegram", []string{"token"}}, {"email", []string{"password"}}} {
			if w, ok := value[kind.name].(map[string]any); ok {
				for _, f := range kind.fields {
					out = append(out, secretField{"/" + kind.name + "/" + f, w, f})
				}
			}
		}
		return out
	}
	return nil
}

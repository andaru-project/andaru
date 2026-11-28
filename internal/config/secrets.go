package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxSecretBytes bounds one secret's value: an RSA-4096 PKCS #8 private key in PEM fits with room to spare.
const maxSecretBytes = 8192

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
func secretPrecondition(doc secretDocument, scope string, pw map[string]any, path string) *Fault {
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
	if old, ok := doc.Scopes[scope][name]; ok {
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
	for scope, entries := range doc.Scopes {
		if !validSecretScope(scope) {
			return validationError{{Path: "", Message: "Invalid secret scope."}}
		}
		for name, v := range entries {
			n, e := strconv.ParseInt(string(v.Version), 10, 64)
			_, te := time.Parse(time.RFC3339Nano, v.UpdatedAt)
			if !validSecretName(name) || e != nil || n <= 0 || n > h || te != nil || len(v.Value) > maxSecretBytes || strings.ContainsRune(v.Value, 0) {
				return validationError{{Path: "", Message: "Invalid secret value or version."}}
			}
			if scope == "core" && v.Kind == "addon" || scope != "core" && v.Kind != "addon" {
				return validationError{{Path: "", Message: "Invalid secret kind for scope."}}
			}
		}
	}
	return nil
}

// checkResolvedReferences gates an apply, so it covers links, enabled add-ons and the certificates management and
// enabled add-ons serve: channels take effect without an apply, and a CA without its key still verifies peers. A
// channel, disabled add-on or PKI object a restore left unresolved may be saved with that reference unchanged.
func checkResolvedReferences(d *declaration) error {
	var details []FieldError
	certs := certificateIndex(d)
	unresolvedCert := func(path, id string) {
		if c, ok := certs[id]; ok && c.KeyRef.Unresolved {
			details = append(details, FieldError{Path: path, Message: "The private key of certificate " + id + " is unresolved; set it before applying."})
		}
	}
	if d.Management != nil && d.Management.Certificate != "" {
		unresolvedCert("/management/certificate", d.Management.Certificate)
	}
	for i := range d.Addons {
		if d.Addons[i].Enabled {
			for _, id := range addonCertificateRefs(&d.Addons[i]) {
				unresolvedCert(fmt.Sprintf("/addons/%d/settings", i), id)
			}
		}
	}
	for i, l := range d.Links {
		if l.Password != nil && l.Password.Unresolved {
			details = append(details, FieldError{Path: "/links/" + strconv.Itoa(i) + "/password", Message: "Secret " + l.Password.Secret + " is unresolved; explicitly bind or set it before applying."})
		}
	}
	for i := range d.Addons {
		a := &d.Addons[i]
		if !a.Enabled {
			continue
		}
		for _, k := range addonSettingSecretKeys(a.Settings) {
			if ref, ok := addonSecretRef(a.Settings[k]); ok && ref.Unresolved {
				details = append(details, FieldError{Path: fmt.Sprintf("/addons/%d/settings/%s", i, escapeJSONPointer(k)), Message: "Secret " + ref.Secret + " is unresolved; explicitly bind or set it before applying."})
			}
		}
	}
	if len(details) != 0 {
		return &Fault{Code: "VALIDATION_FAILED", Details: details}
	}
	return nil
}

// heldSecret is a secret reference of a declaration, with the object and scope that hold it and its path.
type heldSecret struct {
	key            objectKey
	scope          string
	path           string
	ref            *secretReference
	markUnresolved func()
}

func (h heldSecret) setUnresolved() {
	h.ref.Unresolved = true
	if h.markUnresolved != nil {
		h.markUnresolved()
	}
}

// heldSecrets are every secret reference of d.
func heldSecrets(d *declaration) []heldSecret {
	var out []heldSecret
	for i := range d.Links {
		if l := &d.Links[i]; l.Password != nil {
			out = append(out, heldSecret{key: objectKey{"links", l.ID}, scope: "core", path: fmt.Sprintf("/links/%d/password", i), ref: l.Password})
		}
	}
	for i := range d.Notifications.Channels {
		c := &d.Notifications.Channels[i]
		for _, s := range c.secrets() {
			if s.ref != nil {
				out = append(out, heldSecret{key: objectKey{"notifications.channels", c.ID}, scope: "core", path: fmt.Sprintf("/notifications/channels/%d%s", i, s.path), ref: s.ref})
			}
		}
	}
	pkiKey := func(collection, id, kind string, i int, ref *secretReference) {
		if ref != nil {
			out = append(out, heldSecret{key: objectKey{collection, id}, scope: "core", path: fmt.Sprintf("/pki/%s/%d/keyRef", kind, i), ref: ref})
		}
	}
	for i := range d.PKI.CAs {
		pkiKey("pki.cas", d.PKI.CAs[i].ID, "cas", i, d.PKI.CAs[i].KeyRef)
	}
	for i := range d.PKI.Certificates {
		pkiKey("pki.certificates", d.PKI.Certificates[i].ID, "certificates", i, &d.PKI.Certificates[i].KeyRef)
	}
	for i := range d.PKI.TLSCryptKeys {
		pkiKey("pki.tlsCryptKeys", d.PKI.TLSCryptKeys[i].ID, "tlsCryptKeys", i, &d.PKI.TLSCryptKeys[i].KeyRef)
	}
	for i := range d.Addons {
		a := &d.Addons[i]
		scope := addonSecretScope(a.ID)
		for _, k := range addonSettingSecretKeys(a.Settings) {
			ref, ok := addonSecretRef(a.Settings[k])
			if !ok {
				continue
			}
			m := a.Settings[k].(map[string]any)
			out = append(out, heldSecret{
				key:   objectKey{"addons", a.ID},
				scope: scope,
				path:  fmt.Sprintf("/addons/%d/settings/%s", i, escapeJSONPointer(k)),
				ref:   ref,
				markUnresolved: func() {
					m["unresolved"] = true
				},
			})
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

// secretKind is the kind of secret a collection holds. A secret is bound to its kind: a link's password, a channel's
// secrets, and an add-on's secrets are isolated so none may reach another's.
func secretKind(collection string) string {
	switch collection {
	case "notifications.channels":
		return "notification"
	case "addons":
		return "addon"
	case "pki.cas", "pki.certificates", "pki.tlsCryptKeys":
		return "pki"
	default:
		return "password"
	}
}

func secretScopeForOp(collection, id string) string {
	if collection == "addons" {
		return addonSecretScope(id)
	}
	return "core"
}

func secretFields(collection string, value map[string]any) []secretField {
	switch collection {
	case "links":
		return []secretField{{"/password", value, "password"}}
	case "pki.cas", "pki.certificates", "pki.tlsCryptKeys":
		return []secretField{{"/keyRef", value, "keyRef"}}
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
	case "addons":
		settings, ok := value["settings"].(map[string]any)
		if !ok {
			return nil
		}
		// Every JSON object in add-on settings is a secretWrite/secretRef (no other v1 property type is an object),
		// so routing all object entries through secretFields ensures malformed secret objects never bypass validation.
		var keys []string
		for k, v := range settings {
			if _, isObj := v.(map[string]any); isObj {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		var out []secretField
		for _, k := range keys {
			out = append(out, secretField{"/settings/" + escapeJSONPointer(k), settings, k})
		}
		return out
	}
	return nil
}

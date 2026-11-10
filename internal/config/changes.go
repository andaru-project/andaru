package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Operation struct {
	Op              string          `json:"op"`
	Collection      string          `json:"collection"`
	ID              string          `json:"id,omitempty"`
	LocalID         string          `json:"localId,omitempty"`
	ExpectedVersion *Version        `json:"expectedVersion,omitempty"`
	Value           json.RawMessage `json:"value,omitempty"`
}
type Changes struct {
	Operations []Operation `json:"operations"`
	single     bool
	wire       any
}
type Current struct {
	Kind       string          `json:"kind"`
	Collection string          `json:"collection,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Version    Version         `json:"version,omitempty"`
	Deleted    bool            `json:"deleted,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
}
type Fault struct {
	Code    string
	Details []FieldError
	Current *Current
}

func (f *Fault) Error() string { return f.Code }
func fault(code string, path, msg string) *Fault {
	f := &Fault{Code: code, Details: []FieldError{}}
	if msg != "" {
		f.Details = append(f.Details, FieldError{path, msg})
	}
	return f
}
func boundaryFault(e error) *Fault {
	if errors.Is(e, errTooLarge) {
		return fault("REQUEST_TOO_LARGE", "", "Input exceeds its size limit.")
	}
	var v validationError
	if errors.As(e, &v) {
		return &Fault{Code: "VALIDATION_FAILED", Details: []FieldError(v)}
	}
	return fault("INVALID_REQUEST", "", "Invalid JSON or integer profile.")
}
func DecodeChanges(data []byte) (Changes, error) {
	instance, e := decodeStrict(data)
	if e != nil {
		return Changes{}, boundaryFault(e)
	}
	var c Changes
	if e = json.Unmarshal(data, &c); e != nil {
		return c, fault("INVALID_REQUEST", "", "Invalid operation or version representation.")
	}
	c.wire = instance
	return c, nil
}
func ObjectChange(op, collection, id string, expected *Version, body []byte) (Changes, error) {
	c := Changes{single: true, Operations: []Operation{{Op: op, Collection: collection, ID: id, ExpectedVersion: expected}}}
	if op == "create" {
		c.Operations[0].LocalID = "object"
	}
	if op == "delete" {
		if len(strings.TrimSpace(string(body))) != 0 {
			return c, fault("INVALID_REQUEST", "", "Delete has no body.")
		}
		return c, nil
	}
	v, e := decodeStrict(body)
	if e != nil {
		return c, boundaryFault(e)
	}
	c.Operations[0].Value = append(json.RawMessage(nil), body...)
	c.wire = v
	return c, nil
}
func (c Changes) path(i int, relative string, value bool) string {
	if c.single {
		return relative
	}
	p := fmt.Sprintf("/operations/%d", i)
	if value {
		p += "/value"
	}
	return p + relative
}

type objectKey struct{ Collection, ID string }
type objectVersion struct {
	Version Version
	Deleted bool
}
type objectChange struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Created    bool   `json:"created,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
}
type savedState struct {
	Version                Version
	Config                 declaration
	Secrets                secretDocument
	Versions               map[objectKey]objectVersion
	configBody, secretBody []byte
}
type preparedChange struct {
	State                  savedState
	Changed                []objectChange
	Results                []objectKey
	Created                map[string]string
	Noop                   bool
	ConfigBody, SecretBody []byte
}

func initialState() savedState {
	return savedState{Version: "0", Config: emptyDeclaration(), Secrets: emptySecrets(), Versions: map[objectKey]objectVersion{}}
}
func validCollection(s string) bool { return s == "links" || s == "segments" || s == "zones" }
func newID(collection string) (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	prefix := map[string]string{"links": "lnk", "segments": "seg", "zones": "zon"}[collection]
	return fmt.Sprintf("%s_%x-%x-%x-%x-%x", prefix, b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
func rank(code string) int {
	switch code {
	case "INVALID_REQUEST":
		return 0
	case "PRECONDITION_REQUIRED":
		return 1
	case "NOT_FOUND":
		return 2
	case "PRECONDITION_FAILED":
		return 3
	case "VALIDATION_FAILED":
		return 4
	}
	return 5
}
func objects(d *declaration) map[objectKey]any {
	m := make(map[objectKey]any, len(d.Links)+len(d.Segments)+len(d.Zones))
	for i := range d.Links {
		m[objectKey{"links", d.Links[i].ID}] = &d.Links[i]
	}
	for i := range d.Segments {
		m[objectKey{"segments", d.Segments[i].ID}] = &d.Segments[i]
	}
	for i := range d.Zones {
		m[objectKey{"zones", d.Zones[i].ID}] = &d.Zones[i]
	}
	return m
}

func prepareChanges(current *savedState, input Changes, next Version, now time.Time) (preparedChange, error) {
	var empty preparedChange
	if len(input.Operations) == 0 || len(input.Operations) > 100 {
		return empty, fault("VALIDATION_FAILED", "/operations", "Expected 1 to 100 operations.")
	}
	if current.configBody == nil {
		b, e := canonical(current.Config)
		if e != nil {
			return empty, e
		}
		current.configBody = b
	}
	if current.secretBody == nil {
		b, e := canonical(current.Secrets)
		if e != nil {
			return empty, e
		}
		current.secretBody = b
	}
	before := objects(&current.Config)
	values := make([]map[string]any, len(input.Operations))
	origins := map[objectKey]int{}
	labels := map[string]string{}
	created := map[string]string{}
	keys := make([]objectKey, len(input.Operations))
	duplicates := map[objectKey]bool{}
	var best *Fault
	bestIndex := len(input.Operations)
	pick := func(f *Fault, i int) {
		if f != nil && (best == nil || rank(f.Code) < rank(best.Code) || (rank(f.Code) == rank(best.Code) && i < bestIndex)) {
			best = f
			bestIndex = i
		}
	}
	schema, e := schemas()
	if e != nil {
		return empty, fault("STORE_UNAVAILABLE", "", "Configuration schema unavailable.")
	}
	for i, op := range input.Operations {
		if !validCollection(op.Collection) || (op.Op != "create" && op.Op != "replace" && op.Op != "delete") {
			pick(fault("VALIDATION_FAILED", input.path(i, "", false), "Unknown operation or collection."), i)
			continue
		}
		key := objectKey{op.Collection, op.ID}
		keys[i] = key
		if op.ExpectedVersion != nil {
			if _, e := ParseVersion(string(*op.ExpectedVersion)); e != nil {
				pick(fault("INVALID_REQUEST", input.path(i, "/expectedVersion", false), "Invalid version."), i)
			}
		}
		if op.Op != "create" {
			if op.ID == "" {
				pick(fault("VALIDATION_FAILED", input.path(i, "/id", false), "Object ID is required."), i)
			} else if op.ExpectedVersion == nil {
				pick(fault("PRECONDITION_REQUIRED", input.path(i, "/expectedVersion", false), "Expected version is required."), i)
			} else if v, ok := current.Versions[key]; !ok {
				pick(fault("NOT_FOUND", input.path(i, "/id", false), "Object does not exist."), i)
			} else if v.Deleted || v.Version != *op.ExpectedVersion {
				f := fault("PRECONDITION_FAILED", "", "")
				f.Current = &Current{Kind: "object", Collection: key.Collection, ID: key.ID, Version: v.Version, Deleted: v.Deleted}
				if !v.Deleted {
					if view, e := viewObject(before[key], v.Version, current.Secrets); e == nil {
						f.Current.Value = view.Value
					}
				}
				pick(f, i)
			}
			if duplicates[key] {
				pick(fault("VALIDATION_FAILED", input.path(i, "/id", false), "Duplicate operation target."), i)
			}
			duplicates[key] = true
		}
		if op.Op == "delete" {
			if len(op.Value) > 0 || op.LocalID != "" {
				pick(fault("VALIDATION_FAILED", input.path(i, "", false), "Delete accepts only identity and expected version."), i)
			}
			continue
		}
		instance, e := decodeStrict(op.Value)
		if e != nil {
			pick(boundaryFault(e), i)
			continue
		}
		value, ok := instance.(map[string]any)
		if !ok {
			pick(fault("VALIDATION_FAILED", input.path(i, "", true), "Expected an object."), i)
			continue
		}
		values[i] = value
		if pw, ok := value["password"].(map[string]any); ok {
			if _, set := pw["set"]; set {
				pick(secretPrecondition(current.Secrets, pw, input.path(i, "/password/expectedVersion", true)), i)
			}
		}
	}
	if best != nil && rank(best.Code) < 4 {
		return empty, best
	}
	if !input.single && input.wire != nil {
		if e := schema.changes.Validate(input.wire); e != nil {
			f := boundaryFault(schemaErrors(e))
			pick(f, len(input.Operations))
		}
	}
	for i, op := range input.Operations {
		value := values[i]
		if value == nil {
			continue
		}
		target := schema.objects[op.Collection]
		if target == nil {
			continue
		}
		if e := target.Validate(value); e != nil {
			f := boundaryFault(schemaErrors(e))
			for j := range f.Details {
				f.Details[j].Path = input.path(i, f.Details[j].Path, true)
			}
			pick(f, i)
			continue
		}
		if op.Op == "create" {
			if _, exists := value["id"]; exists {
				pick(fault("VALIDATION_FAILED", input.path(i, "/id", true), "Creation IDs are assigned by the engine."), i)
				continue
			}
			id, e := newID(op.Collection)
			if e != nil {
				return empty, fault("STORE_UNAVAILABLE", "", "Identity allocation failed.")
			}
			keys[i] = objectKey{op.Collection, id}
			value["id"] = id
			if op.LocalID != "" {
				if !localLabel(op.LocalID) || labels[op.LocalID] != "" {
					pick(fault("VALIDATION_FAILED", input.path(i, "/localId", false), "Invalid or duplicate local label."), i)
				} else {
					labels[op.LocalID] = id
					created[op.LocalID] = id
				}
			}
		} else if supplied, ok := value["id"]; ok && supplied != op.ID {
			pick(fault("VALIDATION_FAILED", input.path(i, "/id", true), "Object identity cannot change."), i)
		} else {
			value["id"] = op.ID
		}
	}
	if best != nil {
		return empty, best
	}
	writes := map[objectKey][]byte{}
	deleted := map[objectKey]bool{}
	var createKeys []objectKey
	changedSecrets := map[string]bool{}
	setters := map[string]objectKey{}
	for i, op := range input.Operations {
		key := keys[i]
		origins[key] = i
		if op.Op == "delete" {
			deleted[key] = true
			continue
		}
		value := values[i]
		resolveReferences(value, op.Collection, labels)
		if op.Collection == "links" {
			if pw, ok := value["password"].(map[string]any); ok {
				name, _ := pw["secret"].(string)
				if set, ok := pw["set"].(string); ok {
					if _, dup := setters[name]; dup {
						return empty, fault("VALIDATION_FAILED", input.path(i, "/password", true), "Duplicate secret set command.")
					}
					setters[name] = key
					old, exists := current.Secrets.Scopes["core"][name]
					if !exists || old.Value != set {
						current.Secrets.Scopes["core"][name] = secretEntry{Kind: "password", Value: set, UpdatedAt: now.UTC().Format(time.RFC3339Nano), Version: next}
						changedSecrets[name] = true
					}
				}
				value["password"] = map[string]any{"secret": name}
			} else if value["kind"] == "pppoe" && op.Op == "replace" {
				if old, ok := before[key].(*link); ok && old.Password != nil {
					value["password"] = map[string]any{"secret": old.Password.Secret}
				}
			}
		}
		raw, e := json.Marshal(value)
		if e != nil {
			return empty, e
		}
		writes[key] = raw
		if op.Op == "create" {
			createKeys = append(createKeys, key)
		}
	}
	d := current.Config
	if d.Links, e = rewrite(d.Links, "links", writes, deleted, createKeys, func(x link) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.Segments, e = rewrite(d.Segments, "segments", writes, deleted, createKeys, func(x segment) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.Zones, e = rewrite(d.Zones, "zones", writes, deleted, createKeys, func(x zone) string { return x.ID }); e != nil {
		return empty, e
	}
	raw, e := json.Marshal(d)
	if e != nil {
		return empty, e
	}
	validated, e := decodeDeclaration(raw)
	if e != nil {
		var ve validationError
		if errors.As(e, &ve) {
			return empty, rebaseValidation(ve, &d, origins, input)
		}
		return empty, boundaryFault(e)
	}
	d = validated
	for i, l := range d.Links {
		if l.Password != nil {
			if _, ok := current.Secrets.Scopes["core"][l.Password.Secret]; !ok {
				path := fmt.Sprintf("/links/%d/password", i)
				return empty, rebaseValidation(validationError{{path, "Named secret does not exist."}}, &d, origins, input)
			}
		}
	}
	if e = validateSecrets(current.Secrets, next); e != nil {
		var failure validationError
		if errors.Is(e, errTooLarge) {
			return empty, boundaryFault(e)
		}
		if !errors.As(e, &failure) {
			return empty, e
		}
		return empty, rebaseSecrets(failure, current.Secrets, next, setters, origins, input)
	}
	cb, e := canonical(d)
	if e != nil {
		return empty, boundaryFault(e)
	}
	sb, e := canonical(current.Secrets)
	if e != nil {
		return empty, boundaryFault(e)
	}
	same := string(cb) == string(current.configBody) && string(sb) == string(current.secretBody)
	state := *current
	state.Config = d
	state.configBody = cb
	state.secretBody = sb
	p := preparedChange{State: state, Created: created, Noop: same, ConfigBody: cb, SecretBody: sb, Changed: []objectChange{}, Results: []objectKey{}}
	after := objects(&d)
	resultSet := map[objectKey]bool{}
	changes := map[objectKey]objectChange{}
	for i, op := range input.Operations {
		key := keys[i]
		resultSet[key] = true
		if op.Op == "delete" {
			changes[key] = objectChange{Collection: key.Collection, ID: key.ID, Deleted: true}
			continue
		}
		old, exists := before[key]
		different := !exists
		if exists {
			a, _ := canonical(old)
			b, _ := canonical(after[key])
			different = string(a) != string(b)
		}
		if different {
			changes[key] = objectChange{Collection: key.Collection, ID: key.ID, Created: op.Op == "create"}
		}
	}
	for _, l := range d.Links {
		if l.Password != nil && changedSecrets[l.Password.Secret] {
			key := objectKey{"links", l.ID}
			resultSet[key] = true
			if _, exists := changes[key]; !exists {
				changes[key] = objectChange{Collection: "links", ID: l.ID}
			}
		}
	}
	if !same {
		p.State.Version = next
		for key, ch := range changes {
			p.State.Versions[key] = objectVersion{Version: next, Deleted: ch.Deleted}
			p.Changed = append(p.Changed, ch)
		}
	}
	for key := range resultSet {
		p.Results = append(p.Results, key)
	}
	slices.SortFunc(p.Results, compareKeys)
	slices.SortFunc(p.Changed, func(a, b objectChange) int {
		return compareKeys(objectKey{a.Collection, a.ID}, objectKey{b.Collection, b.ID})
	})
	return p, nil
}
func compareKeys(a, b objectKey) int {
	if n := strings.Compare(a.Collection, b.Collection); n != 0 {
		return n
	}
	return strings.Compare(a.ID, b.ID)
}
func localLabel(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for i, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && (c >= '0' && c <= '9' || c == '_')) {
			return false
		}
	}
	return true
}
func rewrite[T any](old []T, col string, writes map[objectKey][]byte, deleted map[objectKey]bool, created []objectKey, id func(T) string) ([]T, error) {
	out := make([]T, 0, len(old)+len(created))
	appendRaw := func(raw []byte) error {
		var v T
		if e := json.Unmarshal(raw, &v); e != nil {
			return fault("VALIDATION_FAILED", "", "Invalid typed object.")
		}
		out = append(out, v)
		return nil
	}
	for _, v := range old {
		key := objectKey{col, id(v)}
		if deleted[key] {
			continue
		}
		if raw, ok := writes[key]; ok {
			if e := appendRaw(raw); e != nil {
				return nil, e
			}
		} else {
			out = append(out, v)
		}
	}
	for _, key := range created {
		if key.Collection == col {
			if e := appendRaw(writes[key]); e != nil {
				return nil, e
			}
		}
	}
	return out, nil
}
func resolveReferences(v map[string]any, col string, labels map[string]string) {
	resolve := func(m map[string]any, k string) {
		if s, ok := m[k].(string); ok && strings.HasPrefix(s, "@") {
			if id, ok := labels[s[1:]]; ok {
				m[k] = id
			}
		}
	}
	if col == "links" {
		resolve(v, "parent")
		if members, ok := v["members"].([]any); ok {
			for i, s := range members {
				if text, ok := s.(string); ok && strings.HasPrefix(text, "@") {
					if id, ok := labels[text[1:]]; ok {
						members[i] = id
					}
				}
			}
		}
	}
	if col == "segments" {
		resolve(v, "link")
		resolve(v, "zone")
		if a, ok := v["addressing"].(map[string]any); ok {
			if a6, ok := a["ipv6"].(map[string]any); ok {
				resolve(a6, "from")
			}
		}
	}
}
func rebaseValidation(details validationError, d *declaration, origins map[objectKey]int, input Changes) *Fault {
	f := fault("VALIDATION_FAILED", "", "")
	bestOp := len(input.Operations)
	index := objects(d)
	nameOf := func(v any) string {
		switch x := v.(type) {
		case *link:
			return x.Name
		case *segment:
			return x.Name
		case *zone:
			return x.Name
		}
		return ""
	}
	for _, detail := range details {
		parts := strings.SplitN(strings.TrimPrefix(detail.Path, "/"), "/", 3)
		path := detail.Path
		i := -1
		key := objectKey{}
		if len(parts) >= 2 {
			n, e := strconv.Atoi(parts[1])
			if e == nil {
				switch parts[0] {
				case "links":
					if n >= 0 && n < len(d.Links) {
						key = objectKey{"links", d.Links[n].ID}
					}
				case "segments":
					if n >= 0 && n < len(d.Segments) {
						key = objectKey{"segments", d.Segments[n].ID}
					}
				case "zones":
					if n >= 0 && n < len(d.Zones) {
						key = objectKey{"zones", d.Zones[n].ID}
					}
				}
				if op, ok := origins[key]; ok {
					i = op
					tail := ""
					if len(parts) == 3 {
						tail = "/" + parts[2]
					}
					path = input.path(i, tail, true)
				}
			}
		}
		if i < 0 && len(parts) == 3 && parts[2] == "name" {
			name := nameOf(index[key])
			for candidate, op := range origins {
				if candidate.Collection == key.Collection && name != "" && nameOf(index[candidate]) == name && (i < 0 || op < i) {
					i = op
					path = input.path(op, "/name", true)
				}
			}
		}
		if i < 0 {
			for j, op := range input.Operations {
				if op.Op == "delete" || op.Op == "replace" {
					i = j
					path = input.path(j, "/id", false)
					break
				}
			}
			if key.ID != "" {
				detail.Message = "Object " + key.ID + " remains referenced or invalid."
			}
		}
		if i < 0 {
			i = 0
		}
		if i < bestOp {
			f.Details = nil
			bestOp = i
		}
		if i == bestOp && len(f.Details) < 100 {
			f.Details = append(f.Details, FieldError{path, detail.Message})
		}
	}
	return f
}

// rebaseSecrets blames the earliest batch operation whose own secret entry fails validation.
func rebaseSecrets(failure validationError, doc secretDocument, head Version, setters map[string]objectKey, origins map[objectKey]int, input Changes) *Fault {
	best := -1
	for name, key := range setters {
		one := secretDocument{SchemaVersion: doc.SchemaVersion, Scopes: map[string]map[string]secretEntry{"core": {name: doc.Scopes["core"][name]}}}
		if validateSecrets(one, head) != nil && (best < 0 || origins[key] < best) {
			best = origins[key]
		}
	}
	if best < 0 {
		best = 0
	}
	return fault("VALIDATION_FAILED", input.path(best, "/password", true), failure[0].Message)
}

package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/authz"
)

type Operation struct {
	Op              string          `json:"op"`
	Collection      string          `json:"collection"`
	ID              string          `json:"id,omitempty"`
	LocalID         string          `json:"localId,omitempty"`
	ExpectedVersion *Version        `json:"expectedVersion,omitempty"`
	Value           json.RawMessage `json:"value,omitempty"`
	// Before places a created rule ahead of this rule of the same list; without it the rule is appended.
	Before string `json:"before,omitempty"`
}
type Changes struct {
	// ExpectedSavedVersion guards the whole batch: it is refused if the saved head moved.
	ExpectedSavedVersion *Version    `json:"expectedSavedVersion,omitempty"`
	Operations           []Operation `json:"operations"`
	single               bool
	wire                 any
	// assigned maps create-operation indices to pre-allocated IDs (used by engine routes that audit before saving).
	assigned map[int]string
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

// ObjectCreateWithID is a create of one object under id, which the caller allocated with NewID; a client's create
// never names its ID.
func ObjectCreateWithID(collection, id string, body []byte) (Changes, error) {
	c, e := ObjectChange("create", collection, "", nil, body)
	if e != nil {
		return c, e
	}
	if err := c.AssignID(0, id); err != nil {
		return c, err
	}
	return c, nil
}

// AssignID binds a pre-allocated ID to the create operation at opIndex so audited creates use the exact ID recorded in audit.
func (c *Changes) AssignID(opIndex int, id string) error {
	if opIndex < 0 || opIndex >= len(c.Operations) || c.Operations[opIndex].Op != "create" {
		return fault("INVALID_REQUEST", "", "AssignID requires a create operation.")
	}
	collection := c.Operations[opIndex].Collection
	if prefix, _, _ := strings.Cut(id, "_"); prefix != idPrefixes[collection] || len(id) != len(prefix)+37 {
		return fault("INVALID_REQUEST", "", "Invalid object ID.")
	}
	if c.assigned == nil {
		c.assigned = map[int]string{}
	}
	c.assigned[opIndex] = id
	return nil
}

// Before places the single created rule of c ahead of the rule id.
func (c Changes) Before(id string) (Changes, error) {
	if len(c.Operations) != 1 || c.Operations[0].Op != "create" || id == "" {
		return c, fault("INVALID_REQUEST", "", "before applies to creating one rule.")
	}
	c.Operations[0].Before = id
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
	// legacy says the version was saved in a form before schema 4, and origin which of its rules were split
	// from which: see decodeSaved.
	legacy bool
	origin map[string]string
	// storedBody is the version's document as the store holds it, which for a legacy version is not configBody.
	storedBody []byte
	// questions are the decisions the version's upgrade met, read with their provisional choices.
	questions []Question
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

// Ordered collections keep their order; their list is versioned under {collection, collection}.
// Singletons are versioned the same way and written with replace or delete.
var (
	orderedCollections = []string{"rules", "nat.portForwards", "nat.oneToOne"}
	singletons         = []string{"management", "nat.outbound", "dns"}
	collections        = append([]string{"links", "segments", "zones", "roles", "aliases.addresses", "aliases.ports", "dhcp", "dhcp6", "virtualAddresses", "notifications.channels", "addons", "pki.cas", "pki.certificates", "pki.tlsCryptKeys", "acme.accounts", "acme.orders"}, append(slices.Clone(orderedCollections), singletons...)...)
)

func validCollection(s string) bool { return slices.Contains(collections, s) }

// listKey is the version key of an ordered list or a singleton.
func listKey(collection string) objectKey { return objectKey{collection, collection} }

// versionOf returns an object's version, "0" while it does not exist.
func versionOf(s *savedState, key objectKey) objectVersion {
	if v, ok := s.Versions[key]; ok {
		return v
	}
	return objectVersion{Version: "0"}
}

// idPrefixes are the prefixes of the IDs the engine assigns, by collection.
var idPrefixes = map[string]string{"links": "lnk", "segments": "seg", "zones": "zon", "roles": "rol", "aliases.addresses": "adr", "aliases.ports": "svc", "rules": "rul", "dhcp": "dhp", "dhcp6": "dh6", "virtualAddresses": "vip", "notifications.channels": "ntf", "nat.portForwards": "pfw", "nat.oneToOne": "oto", "pki.cas": "ca", "pki.certificates": "crt", "pki.tlsCryptKeys": "tck", "acme.accounts": "aca", "acme.orders": "aco"}

// NewID allocates an ID for a new object of collection, as a create does.
func NewID(collection string) (string, error) { return newID(collection) }

func newID(collection string) (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	prefix := idPrefixes[collection]
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
	for i := range d.Roles {
		m[objectKey{"roles", d.Roles[i].ID}] = &d.Roles[i]
	}
	for i := range d.Aliases.Addresses {
		m[objectKey{"aliases.addresses", d.Aliases.Addresses[i].ID}] = &d.Aliases.Addresses[i]
	}
	for i := range d.Aliases.Ports {
		m[objectKey{"aliases.ports", d.Aliases.Ports[i].ID}] = &d.Aliases.Ports[i]
	}
	for i := range d.Rules {
		m[objectKey{"rules", d.Rules[i].ID}] = &d.Rules[i]
	}
	for i := range d.NAT.PortForwards {
		m[objectKey{"nat.portForwards", d.NAT.PortForwards[i].ID}] = &d.NAT.PortForwards[i]
	}
	for i := range d.NAT.OneToOne {
		m[objectKey{"nat.oneToOne", d.NAT.OneToOne[i].ID}] = &d.NAT.OneToOne[i]
	}
	if d.Management != nil {
		m[listKey("management")] = d.Management
	}
	if d.NAT.Outbound.Mode != "" {
		m[listKey("nat.outbound")] = &d.NAT.Outbound
	}
	for i := range d.DHCP {
		m[objectKey{"dhcp", d.DHCP[i].ID}] = &d.DHCP[i]
	}
	for i := range d.DHCP6 {
		m[objectKey{"dhcp6", d.DHCP6[i].ID}] = &d.DHCP6[i]
	}
	for i := range d.VirtualAddresses {
		m[objectKey{"virtualAddresses", d.VirtualAddresses[i].ID}] = &d.VirtualAddresses[i]
	}
	for i := range d.Notifications.Channels {
		m[objectKey{"notifications.channels", d.Notifications.Channels[i].ID}] = &d.Notifications.Channels[i]
	}
	for i := range d.Addons {
		m[objectKey{"addons", d.Addons[i].ID}] = &d.Addons[i]
	}
	if d.DNS != nil {
		m[listKey("dns")] = d.DNS
	}
	for i := range d.PKI.CAs {
		m[objectKey{"pki.cas", d.PKI.CAs[i].ID}] = &d.PKI.CAs[i]
	}
	for i := range d.PKI.Certificates {
		m[objectKey{"pki.certificates", d.PKI.Certificates[i].ID}] = &d.PKI.Certificates[i]
	}
	for i := range d.PKI.TLSCryptKeys {
		m[objectKey{"pki.tlsCryptKeys", d.PKI.TLSCryptKeys[i].ID}] = &d.PKI.TLSCryptKeys[i]
	}
	for i := range d.ACME.Accounts {
		m[objectKey{"acme.accounts", d.ACME.Accounts[i].ID}] = &d.ACME.Accounts[i]
	}
	for i := range d.ACME.Orders {
		m[objectKey{"acme.orders", d.ACME.Orders[i].ID}] = &d.ACME.Orders[i]
	}
	return m
}

func prepareChanges(current *savedState, input Changes, next Version, now time.Time) (preparedChange, error) {
	return prepareChangesWithCatalog(current, input, next, now, nil, nil)
}

func prepareChangesWithCatalog(current *savedState, input Changes, next Version, now time.Time, catalog *addon.Catalog, managementHosts []string) (preparedChange, error) {
	var empty preparedChange
	if len(input.Operations) == 0 || len(input.Operations) > 100 {
		return empty, fault("VALIDATION_FAILED", "/operations", "Expected 1 to 100 operations.")
	}
	if v := input.ExpectedSavedVersion; v != nil {
		if _, e := ParseVersion(string(*v)); e != nil {
			return empty, fault("VALIDATION_FAILED", "/expectedSavedVersion", "Invalid saved version.")
		}
		if *v != current.Version {
			return empty, fault("PRECONDITION_FAILED", "/expectedSavedVersion", "The saved configuration changed since this batch was built.")
		}
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
	orders := map[int][]string{}
	for i, op := range input.Operations {
		ordered, single := slices.Contains(orderedCollections, op.Collection), slices.Contains(singletons, op.Collection)
		known := op.Op == "create" || op.Op == "replace" || op.Op == "delete" || op.Op == "order"
		misplaced := op.Op == "order" && !ordered || single && op.Op == "create" || op.Before != "" && (op.Op != "create" || !ordered)
		if !validCollection(op.Collection) || !known || misplaced {
			pick(fault("VALIDATION_FAILED", input.path(i, "", false), "Unknown operation or collection."), i)
			continue
		}
		key := objectKey{op.Collection, op.ID}
		if single || op.Op == "order" {
			// A singleton or a list order has no ID of its own; its version is the collection's.
			key = listKey(op.Collection)
			if op.ExpectedVersion == nil {
				pick(fault("PRECONDITION_REQUIRED", input.path(i, "/expectedVersion", false), "Expected version is required."), i)
			} else if v := versionOf(current, key); op.Op == "delete" && (v.Version == "0" || v.Deleted) {
				pick(fault("NOT_FOUND", input.path(i, "", false), "Nothing is set to delete."), i)
			} else if have := map[bool]Version{true: "0", false: v.Version}[v.Deleted]; have != *op.ExpectedVersion {
				f := fault("PRECONDITION_FAILED", "", "")
				f.Current = &Current{Kind: "object", Collection: key.Collection, ID: key.ID, Version: v.Version, Deleted: v.Deleted}
				pick(f, i)
			}
			if duplicates[key] {
				pick(fault("VALIDATION_FAILED", input.path(i, "", false), "Duplicate operation target."), i)
			}
			duplicates[key] = true
			keys[i] = key
			if op.Op == "order" {
				var ids []string
				if e := json.Unmarshal(op.Value, &ids); e != nil {
					pick(fault("VALIDATION_FAILED", input.path(i, "", true), "Expected the list's rule IDs in their new order."), i)
				}
				orders[i] = ids
				continue
			}
			if op.Op == "delete" {
				if len(op.Value) > 0 {
					pick(fault("VALIDATION_FAILED", input.path(i, "", false), "Delete accepts only the expected version."), i)
				}
				continue
			}
		}
		keys[i] = key
		if op.ExpectedVersion != nil {
			if _, e := ParseVersion(string(*op.ExpectedVersion)); e != nil {
				pick(fault("INVALID_REQUEST", input.path(i, "/expectedVersion", false), "Invalid version."), i)
			}
		}
		if op.Op != "create" && !single {
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
		scopeID := op.ID
		if op.Collection == "addons" && op.Op == "create" {
			if id, ok := value["id"].(string); ok {
				scopeID = id
			}
		}
		scope := secretScopeForOp(op.Collection, scopeID)
		for _, f := range secretFields(op.Collection, value) {
			if pw, ok := f.parent[f.name].(map[string]any); ok {
				if op.Collection == "addons" {
					if known, isSec := addonPropIsSecretRef(catalog, scopeID, f.name); known && !isSec {
						pick(fault("VALIDATION_FAILED", input.path(i, f.path, true), "Setting is not a secret-reference property."), i)
						continue
					}
					if fe := validateAddonSecretWrite(pw, input.path(i, f.path, true)); fe != nil {
						pick(fe, i)
						continue
					}
				}
				if _, set := pw["set"]; set {
					pick(secretPrecondition(current.Secrets, scope, pw, input.path(i, f.path+"/expectedVersion", true)), i)
				}
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
		if value == nil || op.Op == "order" {
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
		if slices.Contains(singletons, op.Collection) {
			continue
		}
		if op.Op == "create" {
			if op.Collection == "addons" {
				id, ok := value["id"].(string)
				if !ok || !addon.ValidID(id) {
					pick(fault("VALIDATION_FAILED", input.path(i, "/id", true), "Add-on create requires a valid catalog id."), i)
					continue
				}
				key := objectKey{op.Collection, id}
				if _, exists := before[key]; exists || duplicates[key] {
					pick(fault("VALIDATION_FAILED", input.path(i, "/id", true), "Add-on is already configured."), i)
					continue
				}
				duplicates[key] = true
				keys[i] = key
				if op.LocalID != "" {
					if !localLabel(op.LocalID) || labels[op.LocalID] != "" {
						pick(fault("VALIDATION_FAILED", input.path(i, "/localId", false), "Invalid or duplicate local label."), i)
					} else {
						labels[op.LocalID] = id
						created[op.LocalID] = id
					}
				}
				continue
			}
			if _, exists := value["id"]; exists {
				pick(fault("VALIDATION_FAILED", input.path(i, "/id", true), "Creation IDs are assigned by the engine."), i)
				continue
			}
			id, e := newID(op.Collection)
			if e != nil {
				return empty, fault("STORE_UNAVAILABLE", "", "Identity allocation failed.")
			}
			if assigned := input.assigned[i]; assigned != "" {
				if _, exists := before[objectKey{op.Collection, assigned}]; exists {
					pick(fault("VALIDATION_FAILED", input.path(i, "/id", true), "Object already exists."), i)
					continue
				}
				id = assigned
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
	setters := map[string]heldSecret{}
	// A deleted PKI object's key is removed with it, unless something else still holds its name.
	var droppedKeys []string
	for i, op := range input.Operations {
		key := keys[i]
		origins[key] = i
		if op.Op == "delete" {
			deleted[key] = true
			if op.Collection == "addons" {
				delete(current.Secrets.Scopes, addonSecretScope(key.ID))
			}
			if ref := keyRefOf(before[key]); ref != nil {
				droppedKeys = append(droppedKeys, ref.Secret)
			}
			for _, s := range acmeSecrets(before[key]) {
				if s.ref != nil {
					droppedKeys = append(droppedKeys, s.ref.Secret)
				}
			}
			continue
		}
		if op.Op == "order" {
			continue
		}
		value := values[i]
		resolveReferences(value, op.Collection, labels)
		scope := secretScopeForOp(op.Collection, key.ID)
		if op.Op == "create" && op.Collection == "acme.accounts" && value["accountKeyRef"] == nil {
			// An account created without a key is given one: it never leaves secrets.db but for andaru-outbound.
			pemKey, e := newACMEAccountKey()
			if e != nil {
				return empty, fault("STORE_UNAVAILABLE", "", "Account key generation failed.")
			}
			value["accountKeyRef"] = map[string]any{"secret": ACMESecretName(key.ID, "key"), "set": pemKey}
		}
		for _, f := range secretFields(op.Collection, value) {
			if pw, ok := f.parent[f.name].(map[string]any); ok {
				name, _ := pw["secret"].(string)
				set, setting := pw["set"].(string)
				setterKey := scope + "\x00" + name
				if setting {
					if _, dup := setters[setterKey]; dup {
						return empty, fault("VALIDATION_FAILED", input.path(i, f.path, true), "Duplicate secret set command.")
					}
					setters[setterKey] = heldSecret{key: key, scope: scope, path: f.path}
					if current.Secrets.Scopes[scope] == nil {
						current.Secrets.Scopes[scope] = map[string]secretEntry{}
					}
					old, exists := current.Secrets.Scopes[scope][name]
					if exists && old.Kind != secretKind(op.Collection) {
						return empty, fault("VALIDATION_FAILED", input.path(i, f.path, true), "This secret belongs to another kind of object.")
					}
					if !exists || old.Value != set {
						current.Secrets.Scopes[scope][name] = secretEntry{Kind: secretKind(op.Collection), Value: set, UpdatedAt: now.UTC().Format(time.RFC3339Nano), Version: next}
						changedSecrets[setterKey] = true
					}
				}
				reference := map[string]any{"secret": name}
				if pw["unresolved"] == true && !setting { // a set resolves it
					// Only a restore leaves a channel's or add-on's secret unresolved, which a client may echo back
					// unchanged; a new unresolved one would skip secret checks.
					if op.Collection == "notifications.channels" && !heldUnresolved(before[key], f.path, name) {
						return empty, fault("VALIDATION_FAILED", input.path(i, f.path, true), "A notification channel's secret must be set or exist.")
					}
					if op.Collection == "addons" && !heldUnresolved(before[key], f.path, name) {
						return empty, fault("VALIDATION_FAILED", input.path(i, f.path, true), "An add-on secret must be set or exist.")
					}
					if slices.Contains(pkiCollections, op.Collection) && !heldUnresolved(before[key], f.path, name) {
						return empty, fault("VALIDATION_FAILED", input.path(i, f.path, true), "A private key must be set or exist.")
					}
					if slices.Contains(acmeCollections, op.Collection) && !heldUnresolved(before[key], f.path, name) {
						return empty, fault("VALIDATION_FAILED", input.path(i, f.path, true), "An ACME secret must be set or exist.")
					}
					reference["unresolved"] = true
				}
				f.parent[f.name] = reference
			} else if op.Collection == "links" && value["kind"] == "pppoe" && op.Op == "replace" {
				if old, ok := before[key].(*link); ok && old.Password != nil {
					value["password"] = old.Password
				}
			} else if ref := acmeRefAt(before[key], f.path); ref != nil && op.Op == "replace" {
				f.parent[f.name] = ref // a replace that leaves an ACME secret out keeps it
			} else if ref := keyRefOf(before[key]); ref != nil && op.Op == "replace" {
				value["keyRef"] = ref // a replace that leaves the key out keeps it: a CA never loses its key by a rename
			} else if op.Op == "create" && (op.Collection == "pki.certificates" || op.Collection == "pki.tlsCryptKeys") {
				return empty, fault("VALIDATION_FAILED", input.path(i, "/keyRef", true), "A private key is required.")
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
	if d.Roles, e = rewrite(d.Roles, "roles", writes, deleted, createKeys, func(x authz.Role) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.Aliases.Addresses, e = rewrite(d.Aliases.Addresses, "aliases.addresses", writes, deleted, createKeys, func(x address) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.DHCP, e = rewrite(d.DHCP, "dhcp", writes, deleted, createKeys, func(x dhcpEntry) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.VirtualAddresses, e = rewrite(d.VirtualAddresses, "virtualAddresses", writes, deleted, createKeys, func(x virtualAddress) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.Notifications.Channels, e = rewrite(d.Notifications.Channels, "notifications.channels", writes, deleted, createKeys, func(x channel) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.Addons, e = rewrite(d.Addons, "addons", writes, deleted, createKeys, func(x addonConfig) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.PKI.CAs, e = rewrite(d.PKI.CAs, "pki.cas", writes, deleted, createKeys, func(x pkiCA) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.PKI.Certificates, e = rewrite(d.PKI.Certificates, "pki.certificates", writes, deleted, createKeys, func(x pkiCertificate) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.PKI.TLSCryptKeys, e = rewrite(d.PKI.TLSCryptKeys, "pki.tlsCryptKeys", writes, deleted, createKeys, func(x tlsCryptKey) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.ACME.Accounts, e = rewrite(d.ACME.Accounts, "acme.accounts", writes, deleted, createKeys, func(x acmeAccount) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.ACME.Orders, e = rewrite(d.ACME.Orders, "acme.orders", writes, deleted, createKeys, func(x acmeOrder) string { return x.ID }); e != nil {
		return empty, e
	}
	// Prune optional add-on secrets dropped from settings so they do not linger in secrets.db or block re-adding.
	for i := range d.Addons {
		a := d.Addons[i]
		if _, wasWritten := writes[objectKey{"addons", a.ID}]; !wasWritten {
			continue
		}
		scope := addonSecretScope(a.ID)
		entries := current.Secrets.Scopes[scope]
		if len(entries) == 0 {
			continue
		}
		referenced := map[string]bool{}
		for _, v := range a.Settings {
			if ref, ok := v.(map[string]any); ok {
				if name, ok := ref["secret"].(string); ok {
					referenced[name] = true
				}
			}
		}
		for name := range entries {
			if !referenced[name] {
				delete(entries, name)
			}
		}
		if len(entries) == 0 {
			delete(current.Secrets.Scopes, scope)
		}
	}
	if d.DHCP6, e = rewrite(d.DHCP6, "dhcp6", writes, deleted, createKeys, func(x dhcp6Entry) string { return x.ID }); e != nil {
		return empty, e
	}
	if d.Aliases.Ports, e = rewrite(d.Aliases.Ports, "aliases.ports", writes, deleted, createKeys, func(x portAlias) string { return x.ID }); e != nil {
		return empty, e
	}
	for _, col := range orderedCollections {
		var f *Fault
		switch col {
		case "rules":
			f = rewriteOrdered(&d.Rules, col, func(x rule) string { return x.ID }, input, keys, orders, writes, deleted, createKeys)
		case "nat.portForwards":
			f = rewriteOrdered(&d.NAT.PortForwards, col, func(x portForward) string { return x.ID }, input, keys, orders, writes, deleted, createKeys)
		case "nat.oneToOne":
			f = rewriteOrdered(&d.NAT.OneToOne, col, func(x oneToOne) string { return x.ID }, input, keys, orders, writes, deleted, createKeys)
		}
		if f != nil {
			return empty, f
		}
	}
	if deleted[listKey("management")] {
		d.Management = nil
	} else if raw, ok := writes[listKey("management")]; ok {
		var m management
		if e := json.Unmarshal(raw, &m); e != nil {
			return empty, fault("VALIDATION_FAILED", "", "Invalid typed object.")
		}
		d.Management = &m
	}
	if deleted[listKey("nat.outbound")] {
		d.NAT.Outbound = outbound{}
	} else if raw, ok := writes[listKey("nat.outbound")]; ok {
		if e := json.Unmarshal(raw, &d.NAT.Outbound); e != nil {
			return empty, fault("VALIDATION_FAILED", "", "Invalid typed object.")
		}
	}
	if deleted[listKey("dns")] {
		d.DNS = nil
	} else if raw, ok := writes[listKey("dns")]; ok {
		var s dnsService
		if e := json.Unmarshal(raw, &s); e != nil {
			return empty, fault("VALIDATION_FAILED", "", "Invalid typed object.")
		}
		d.DNS = &s
	}
	// A written segment stores its advertisement in the current form; loading keeps old versions byte for byte,
	// and an untouched segment keeps its bytes so an unchanged batch stays a no-op.
	for i := range d.Segments {
		if _, written := writes[objectKey{"segments", d.Segments[i].ID}]; !written {
			continue
		}
		if ra := d.Segments[i].Addressing.IPv6.RA; ra != nil && ra.Mode == "" {
			d.Segments[i].Addressing.IPv6.RA = &advertisement{Mode: ra.mode()}
		}
	}
	// Before validation, which would refuse an IPv6 forward left without its target in less helpful words.
	if f := vipStillTargeted(&current.Config, &d, input); f != nil {
		return empty, f
	}
	d.SchemaVersion = schemaVersion
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
	if catalog != nil {
		if catErrs := validateAddonsAgainstCatalog(catalog, &d, writes, managementHosts); len(catErrs) > 0 {
			return empty, rebaseValidation(validationError(catErrs), &d, origins, input)
		}
		normalizeDeclaration(&d)
	}
	held := heldSecrets(&d)
	for _, name := range droppedKeys {
		if !slices.ContainsFunc(held, func(h heldSecret) bool { return h.scope == "core" && h.ref.Secret == name }) {
			delete(current.Secrets.Scopes["core"], name)
		}
	}
	for _, h := range held {
		if !h.ref.Unresolved {
			if _, ok := current.Secrets.Scopes[h.scope][h.ref.Secret]; !ok {
				return empty, rebaseValidation(validationError{{h.path, "Named secret does not exist."}}, &d, origins, input)
			}
		}
	}
	// A reference this batch writes must name a secret of its collection's kind (secretKind), unresolved or not. The
	// refusal tells a client that a secret of another kind exists, as a version precondition already does; never its value.
	for _, h := range held {
		entry, exists := current.Secrets.Scopes[h.scope][h.ref.Secret]
		if !exists && h.ref.Unresolved || entry.Kind == secretKind(h.key.Collection) {
			continue
		}
		if _, written := writes[h.key]; written {
			return empty, rebaseValidation(validationError{{h.path, "This secret belongs to another kind of object."}}, &d, origins, input)
		}
		// A secret this batch creates may not take a name an unwritten object of the other kind holds unresolved.
		setterKey := h.scope + "\x00" + h.ref.Secret
		if setter, set := setters[setterKey]; set && changedSecrets[setterKey] {
			return empty, fault("VALIDATION_FAILED", input.path(origins[setter.key], setter.path, true), "Another kind of object holds this secret's name.")
		}
	}
	// A webhook URL is checked where its value is known: for the channels this batch writes, or whose URL it sets.
	for i, c := range d.Notifications.Channels {
		if c.Webhook == nil || c.Webhook.URL.Unresolved {
			continue
		}
		if _, written := writes[objectKey{"notifications.channels", c.ID}]; !written && !changedSecrets["core\x00"+c.Webhook.URL.Secret] {
			continue
		}
		if !webhookURL(current.Secrets.Scopes["core"][c.Webhook.URL.Secret].Value) {
			path := fmt.Sprintf("/notifications/channels/%d/webhook/url", i)
			return empty, rebaseValidation(validationError{{path, "Expected the secret to hold an http or https URL with a host."}}, &d, origins, input)
		}
	}
	if keyErrs := checkPKIKeys(&d, current.Secrets, writes, changedSecrets); len(keyErrs) > 0 {
		return empty, rebaseValidation(validationError(keyErrs), &d, origins, input)
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
			if slices.Contains(orderedCollections, op.Collection) {
				changes[listKey(op.Collection)] = objectChange{Collection: op.Collection, ID: op.Collection}
			}
			continue
		}
		if op.Op == "order" {
			changes[key] = objectChange{Collection: op.Collection, ID: op.Collection}
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
		// Any create, delete or reorder in an ordered list moves the list's own version.
		if slices.Contains(orderedCollections, op.Collection) && op.Op != "replace" {
			changes[listKey(op.Collection)] = objectChange{Collection: op.Collection, ID: op.Collection}
		}
	}
	for _, h := range held {
		if changedSecrets[h.scope+"\x00"+h.ref.Secret] {
			resultSet[h.key] = true
			if _, exists := changes[h.key]; !exists {
				changes[h.key] = objectChange{Collection: h.key.Collection, ID: h.key.ID}
			}
		}
	}
	if !same {
		p.State.Version = next
		carryLegacy(&p.State, after, changes)
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
	resolveAll := func(m map[string]any, k string) {
		if list, ok := m[k].([]any); ok {
			for i, s := range list {
				if text, ok := s.(string); ok && strings.HasPrefix(text, "@") {
					if id, ok := labels[text[1:]]; ok {
						list[i] = id
					}
				}
			}
		}
	}
	switch col {
	case "aliases.addresses", "aliases.ports":
		resolveAll(v, "members")
	case "rules":
		for _, side := range []string{"from", "to"} {
			if m, ok := v[side].(map[string]any); ok {
				resolveAll(m, "zones")
				resolveAll(m, "addresses")
			}
		}
		resolveAll(v, "ports")
	case "management", "dhcp", "dhcp6":
		resolve(v, "segment")
		resolveAll(v, "allowedSources")
	case "virtualAddresses":
		resolve(v, "segment")
		resolve(v, "hostSegment")
	case "dns":
		resolveAll(v, "segments")
		resolveAll(v, "virtualAddresses")
	case "nat.portForwards":
		resolveAll(v, "ingressZones")
		resolveAll(v, "from")
		if to, ok := v["to"].(map[string]any); ok {
			resolve(to, "address")
		}
	case "nat.oneToOne":
		resolve(v, "zone")
		resolveAll(v, "destination")
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

// owner maps a validation path to the object or singleton it names and the path below it.
func owner(d *declaration, path string) (objectKey, string) {
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	rest := func(from int) string {
		if from >= len(p) {
			return ""
		}
		return "/" + strings.Join(p[from:], "/")
	}
	switch p[0] {
	case "management", "dns":
		return listKey(p[0]), rest(1)
	case "nat":
		if len(p) > 1 && p[1] == "outbound" {
			return listKey("nat.outbound"), rest(2)
		}
		if len(p) < 2 {
			return objectKey{}, ""
		}
		p = append([]string{"nat." + p[1]}, p[2:]...)
	case "aliases", "notifications", "pki", "acme":
		if len(p) < 2 {
			return objectKey{}, ""
		}
		p = append([]string{p[0] + "." + p[1]}, p[2:]...)
	}
	if len(p) < 2 {
		return objectKey{}, ""
	}
	n, e := strconv.Atoi(p[1])
	if e != nil || n < 0 {
		return objectKey{}, ""
	}
	id := ""
	switch col := p[0]; {
	case col == "links" && n < len(d.Links):
		id = d.Links[n].ID
	case col == "segments" && n < len(d.Segments):
		id = d.Segments[n].ID
	case col == "zones" && n < len(d.Zones):
		id = d.Zones[n].ID
	case col == "roles" && n < len(d.Roles):
		id = d.Roles[n].ID
	case col == "aliases.addresses" && n < len(d.Aliases.Addresses):
		id = d.Aliases.Addresses[n].ID
	case col == "aliases.ports" && n < len(d.Aliases.Ports):
		id = d.Aliases.Ports[n].ID
	case col == "rules" && n < len(d.Rules):
		id = d.Rules[n].ID
	case col == "dhcp" && n < len(d.DHCP):
		id = d.DHCP[n].ID
	case col == "virtualAddresses" && n < len(d.VirtualAddresses):
		id = d.VirtualAddresses[n].ID
	case col == "notifications.channels" && n < len(d.Notifications.Channels):
		id = d.Notifications.Channels[n].ID
	case col == "addons" && n < len(d.Addons):
		id = d.Addons[n].ID
	case col == "dhcp6" && n < len(d.DHCP6):
		id = d.DHCP6[n].ID
	case col == "nat.portForwards" && n < len(d.NAT.PortForwards):
		id = d.NAT.PortForwards[n].ID
	case col == "nat.oneToOne" && n < len(d.NAT.OneToOne):
		id = d.NAT.OneToOne[n].ID
	case col == "pki.cas" && n < len(d.PKI.CAs):
		id = d.PKI.CAs[n].ID
	case col == "pki.certificates" && n < len(d.PKI.Certificates):
		id = d.PKI.Certificates[n].ID
	case col == "pki.tlsCryptKeys" && n < len(d.PKI.TLSCryptKeys):
		id = d.PKI.TLSCryptKeys[n].ID
	case col == "acme.accounts" && n < len(d.ACME.Accounts):
		id = d.ACME.Accounts[n].ID
	case col == "acme.orders" && n < len(d.ACME.Orders):
		id = d.ACME.Orders[n].ID
	}
	if id == "" {
		return objectKey{}, ""
	}
	return objectKey{p[0], id}, rest(2)
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
		case *pkiCA:
			return x.Name
		case *pkiCertificate:
			return x.Name
		case *tlsCryptKey:
			return x.Name
		case *acmeAccount:
			return x.Name
		case *acmeOrder:
			return x.Name
		}
		return ""
	}
	for _, detail := range details {
		path := detail.Path
		i := -1
		key, tail := owner(d, detail.Path)
		op, ok := origins[key]
		if ok {
			path = input.path(op, tail, true)
		} else if op, ok = origins[listKey(key.Collection)]; ok && key.Collection != "" {
			// A rule no operation wrote may still have been moved by an order operation.
			path = input.path(op, "", true)
		}
		if ok {
			i = op
		}
		if i < 0 && tail == "/name" {
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

// heldUnresolved says whether old, a channel, add-on or PKI object before the batch, holds name unresolved at path.
func heldUnresolved(old any, path, name string) bool {
	if ref := keyRefOf(old); ref != nil && path == "/keyRef" {
		return ref.Secret == name && ref.Unresolved
	}
	if ref := acmeRefAt(old, path); ref != nil {
		return ref.Secret == name && ref.Unresolved
	}
	switch obj := old.(type) {
	case *channel:
		for _, s := range obj.secrets() {
			if s.path == path {
				return s.ref != nil && s.ref.Secret == name && s.ref.Unresolved
			}
		}
	case *addonConfig:
		propName, ok := strings.CutPrefix(path, "/settings/")
		if !ok {
			return false
		}
		ref, ok := obj.Settings[propName].(map[string]any)
		if !ok {
			return false
		}
		sec, _ := ref["secret"].(string)
		unres, _ := ref["unresolved"].(bool)
		return sec == name && unres
	}
	return false
}

// rebaseSecrets blames the earliest batch operation whose own secret entry fails validation.
func rebaseSecrets(failure validationError, doc secretDocument, head Version, setters map[string]heldSecret, origins map[objectKey]int, input Changes) *Fault {
	best, path := -1, ""
	for _, key := range slices.Sorted(maps.Keys(setters)) {
		setter := setters[key]
		scope, name, _ := strings.Cut(key, "\x00")
		scopes := map[string]map[string]secretEntry{"core": {}}
		if scope == "core" {
			scopes["core"][name] = doc.Scopes["core"][name]
		} else {
			scopes[scope] = map[string]secretEntry{name: doc.Scopes[scope][name]}
		}
		one := secretDocument{SchemaVersion: doc.SchemaVersion, Scopes: scopes}
		if validateSecrets(one, head) != nil && (best < 0 || origins[setter.key] < best) {
			best, path = origins[setter.key], setter.path
		}
	}
	if best < 0 { // no one entry fails, as when the document as a whole is too large: blame the first setter
		best, path = 0, ""
		for _, name := range slices.Sorted(maps.Keys(setters)) {
			setter := setters[name]
			if i := origins[setter.key]; path == "" || i < best {
				best, path = i, setter.path
			}
		}
	}
	return fault("VALIDATION_FAILED", input.path(best, path, true), failure[0].Message)
}

// rewriteOrdered applies a batch's writes to one ordered list, then its placements (before) and reorders.
func rewriteOrdered[T any](list *[]T, col string, idOf func(T) string, input Changes, keys []objectKey, orders map[int][]string,
	writes map[objectKey][]byte, deleted map[objectKey]bool, created []objectKey) *Fault {
	var e error
	if *list, e = rewrite(*list, col, writes, deleted, created, idOf); e != nil {
		var f *Fault
		if errors.As(e, &f) {
			return f
		}
		return fault("VALIDATION_FAILED", "", "Invalid typed object.")
	}
	for i, op := range input.Operations {
		if op.Collection != col {
			continue
		}
		switch {
		case op.Op == "create" && op.Before != "":
			at := slices.IndexFunc(*list, func(r T) bool { return idOf(r) == op.Before })
			from := slices.IndexFunc(*list, func(r T) bool { return idOf(r) == keys[i].ID })
			if at < 0 {
				return fault("VALIDATION_FAILED", input.path(i, "/before", false), "Expected an entry of the same list.")
			}
			r := (*list)[from]
			*list = slices.Insert(slices.Delete(*list, from, from+1), at, r)
		case op.Op == "order":
			ids := orders[i]
			if len(ids) != len(*list) {
				return fault("VALIDATION_FAILED", input.path(i, "", true), "The order must name every entry of the list exactly once.")
			}
			placed := make([]T, 0, len(ids))
			for _, id := range ids {
				j := slices.IndexFunc(*list, func(r T) bool { return idOf(r) == id })
				if j < 0 || slices.ContainsFunc(placed, func(r T) bool { return idOf(r) == id }) {
					return fault("VALIDATION_FAILED", input.path(i, "", true), "The order must name every entry of the list exactly once.")
				}
				placed = append(placed, (*list)[j])
			}
			*list = placed
		}
	}
	return nil
}

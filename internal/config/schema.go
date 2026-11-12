package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema.json
var SchemaBundle []byte

//go:embed secret-schema.json
var secretSchema []byte

type schemaSet struct {
	declaration, changes, secret *jsonschema.Schema
	objects                      map[string]*jsonschema.Schema
}

var schemas = sync.OnceValues(func() (*schemaSet, error) {
	c, e := newSchemaCompiler(map[string][]byte{schemaURI: SchemaBundle, "https://andaru.invalid/private/config-secrets-v1": secretSchema})
	if e != nil {
		return nil, e
	}
	var s schemaSet
	if s.declaration, e = c.Compile(schemaURI + "#/$defs/declaration"); e != nil {
		return nil, e
	}
	if s.changes, e = c.Compile(schemaURI + "#/$defs/changes"); e != nil {
		return nil, e
	}
	if s.secret, e = c.Compile("https://andaru.invalid/private/config-secrets-v1"); e != nil {
		return nil, e
	}
	s.objects = make(map[string]*jsonschema.Schema, 3)
	for collection, kind := range map[string]string{"links": "link", "segments": "segment", "zones": "zone", "addresses": "address", "services": "service",
		"policy.input": "rule", "policy.forward": "rule", "management": "managementFields#", "nat.outbound": "outboundFields#", "dhcp": "dhcp", "dns": "dnsService#"} {
		kind, whole := strings.CutSuffix(kind, "#")
		if !whole {
			kind += "Write"
		}
		object, err := c.Compile(schemaURI + "#/$defs/" + kind)
		if err != nil {
			return nil, err
		}
		s.objects[collection] = object
	}
	return &s, nil
})

// newSchemaCompiler is the only schema compiler setup; the empty URL loader refuses every external reference.
func newSchemaCompiler(resources map[string][]byte) (*jsonschema.Compiler, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	for uri, raw := range resources {
		v, e := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if e != nil {
			return nil, e
		}
		if e = c.AddResource(uri, v); e != nil {
			return nil, e
		}
	}
	return c, nil
}
func schemaErrors(err error) error {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return errors.New("embedded schema unavailable")
	}
	var details validationError
	var walk func(*jsonschema.ValidationError)
	walk = func(v *jsonschema.ValidationError) {
		if len(details) >= 100 {
			return
		}
		if len(v.Causes) > 0 {
			for _, c := range v.Causes {
				walk(c)
			}
			return
		}
		p := safePointer(v.InstanceLocation)
		for _, d := range details {
			if d.Path == p {
				return
			}
		}
		details = append(details, FieldError{Path: p, Message: "Value does not satisfy the declared shape or constraint."})
	}
	walk(ve)
	return details
}
func safePointer(tokens []string) string {
	const fields = " schemaVersion links segments zones objects policy nat management dhcp dns ranges leaseTime dnsServers staticLeases mac hostname upstreams tlsName localDomain registerLeases outbound segment allowedSources input forward action to services log protocol ports types address prefix first last before order id name description kind mtu enabled permanentMac parent vlanId members mode username password secret set expectedVersion isSet updatedAt version addressing ipv4 ipv6 addresses gateway requestPrefixLength from subnetId ra managed otherInfo dns servers searchDomains op collection localId operations state savedVersion value items unusedSecrets "
	var b strings.Builder
	for _, t := range tokens {
		if _, e := strconv.ParseUint(t, 10, 32); e != nil && !strings.Contains(fields, " "+t+" ") {
			break
		}
		if b.Len()+len(t) > 512 {
			break
		}
		b.WriteByte('/')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1"))
	}
	return b.String()
}
func decodeDeclaration(raw []byte) (declaration, error) {
	var d declaration
	instance, e := decodeStrictLimit(raw, documentLimit)
	if e != nil {
		return d, e
	}
	s, e := schemas()
	if e != nil {
		return d, e
	}
	if e = s.declaration.Validate(instance); e != nil {
		return d, schemaErrors(e)
	}
	if e = json.Unmarshal(raw, &d); e != nil {
		return d, validationError{{Path: "", Message: "Expected bounded integer fields."}}
	}
	if details := slices.Concat(validateTopology(&d), validatePolicy(&d), validateServices(&d)); len(details) > 0 {
		return declaration{}, validationError(details)
	}
	normalizeDeclaration(&d)
	return d, nil
}

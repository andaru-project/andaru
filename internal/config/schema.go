package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema.json
var SchemaBundle []byte

//go:embed secret-schema.json
var secretSchema []byte

type schemaSet struct{ declaration, write, changes, secret *jsonschema.Schema }

var schemas = sync.OnceValues(func() (*schemaSet, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	for uri, raw := range map[string][]byte{schemaURI: SchemaBundle, "https://andaru.invalid/private/config-secrets-v1": secretSchema} {
		v, e := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if e != nil {
			return nil, e
		}
		if e = c.AddResource(uri, v); e != nil {
			return nil, e
		}
	}
	var s schemaSet
	var e error
	if s.declaration, e = c.Compile(schemaURI + "#/$defs/declaration"); e != nil {
		return nil, e
	}
	if s.write, e = c.Compile(schemaURI + "#/$defs/objectWrite"); e != nil {
		return nil, e
	}
	if s.changes, e = c.Compile(schemaURI + "#/$defs/changes"); e != nil {
		return nil, e
	}
	if s.secret, e = c.Compile("https://andaru.invalid/private/config-secrets-v1"); e != nil {
		return nil, e
	}
	return &s, nil
})

func compileSchema(raw []byte, uri string) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	v, e := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	if e = c.AddResource(uri, v); e != nil {
		return nil, e
	}
	return c.Compile(uri)
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
	const fields = " schemaVersion links segments zones id name description kind mtu enabled permanentMac parent vlanId members mode username password secret set expectedVersion isSet updatedAt version addressing ipv4 ipv6 addresses gateway requestPrefixLength from subnetId ra managed otherInfo op collection localId operations state savedVersion value items unusedSecrets "
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
	if details := validateTopology(&d); len(details) > 0 {
		return declaration{}, validationError(details)
	}
	normalizeDeclaration(&d)
	return d, nil
}

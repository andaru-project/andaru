package config

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The schema of the forms saved before schema 4, kept as it was so that their versions still load.
//
//go:embed schema-v3.json
var legacySchema []byte

const legacySchemaURI = "https://andaru.invalid/private/config-schema-v3"

// legacyTarget is the schema upgradeLegacy turns schemas 1 to 3 into.
const legacyTarget = 4

// legacyCollections is where the store recorded a collection's object versions before schema 4.
var legacyCollections = map[string][]string{"aliases.addresses": {"addresses"}, "aliases.ports": {"services"}, "rules": {"policy.input", "policy.forward"}}

// currentCollection names a collection as it is called now, whatever the form it was recorded in.
func currentCollection(name string) string {
	for current, legacy := range legacyCollections {
		for _, old := range legacy {
			if old == name {
				return current
			}
		}
	}
	return name
}

// decodeSaved decodes a saved or backed-up document of any form into the declaration it is now. stored is the
// form it was written in. A document of an earlier form keeps its bytes in the store and is read through
// upgradeLegacy each time; origin then names, for each rule the upgrade had to add, the rule it was split from.
func decodeSaved(raw []byte) (d declaration, stored int, origin map[string]string, err error) {
	d, stored, origin, _, err = decodeSavedWith(raw, nil)
	return
}

// decodeSavedWith is decodeSaved with answers to the decisions an upgrade meets (settings design, rule 3): each
// step from schema n applies the decisions of schema n, with their answers or provisional choices, then the
// migration from n. It returns the questions met. The migrations from schemas 1 to 3 are one step, so the decisions
// of each of those schemas see the document as saved, with the earlier ones applied.
func decodeSavedWith(raw []byte, answers map[string]string) (d declaration, stored int, origin map[string]string, questions []Question, err error) {
	var head struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err = json.Unmarshal(raw, &head); err != nil {
		return d, 0, nil, nil, errInvalidJSON
	}
	// An answer must name a decision; one whose question an earlier answer no longer raises is left unused.
	for id := range answers {
		if !slices.ContainsFunc(decisions, func(r decision) bool { return r.id == id }) {
			return d, 0, nil, nil, fault("VALIDATION_FAILED", "/choices/"+id, fmt.Sprintf("No decision is called %q.", id))
		}
	}
	if head.SchemaVersion >= schemaVersion {
		d, err = decodeDeclaration(raw)
		return d, head.SchemaVersion, nil, nil, err
	}
	instance, err := decodeStrictLimit(raw, documentLimit)
	if err != nil {
		return d, 0, nil, nil, err
	}
	s, err := schemas()
	if err != nil {
		return d, 0, nil, nil, err
	}
	if err = s.legacy.Validate(instance); err != nil {
		return d, 0, nil, nil, schemaErrors(err)
	}
	m := instance.(map[string]any)
	for from := head.SchemaVersion; from < legacyTarget; from++ {
		met, err := decide(m, from, answers)
		if err != nil {
			return d, 0, nil, nil, err
		}
		questions = append(questions, met...)
	}
	// The migration from schema 4 adds its step after this one, its decisions then its change, and gates the legacy
	// validation and upgradeLegacy above on a document of schema 1 to 3.
	origin = upgradeLegacy(m)
	upgraded, err := json.Marshal(m)
	if err == nil && len(origin) > 0 {
		upgraded, err = dropNeverMatching(m, origin, upgraded)
	}
	if err != nil {
		return d, 0, nil, nil, err
	}
	d, err = decodeDeclaration(upgraded)
	return d, head.SchemaVersion, origin, questions, err
}

// dropNeverMatching removes the rules upgradeLegacy added that can never match, and returns the document
// without them. A zone on a legacy side stood for both families, so "from lan or an IPv4 host, to an IPv6
// network" was a valid rule; its host half matches nothing, which a rule of the current form may not, and the
// engine before rendered no line for it. The half that keeps the legacy rule's ID is never such a half: it is
// the one with the zones.
func dropNeverMatching(m map[string]any, origin map[string]string, upgraded []byte) ([]byte, error) {
	var d declaration
	if err := json.Unmarshal(upgraded, &d); err != nil {
		return upgraded, nil // not a document decodeDeclaration accepts, which says why
	}
	drop := map[string]bool{}
	for _, detail := range validatePolicy(&d, len(d.Rules)*2+100) {
		var n int
		if detail.Message != neverAddresses && detail.Message != neverPorts {
			continue
		}
		if _, e := fmt.Sscanf(detail.Path, "/rules/%d/", &n); e == nil && n < len(d.Rules) && origin[d.Rules[n].ID] != "" {
			drop[d.Rules[n].ID] = true
			delete(origin, d.Rules[n].ID)
		}
	}
	if len(drop) == 0 {
		return upgraded, nil
	}
	rules, _ := m["rules"].([]any)
	m["rules"] = slices.DeleteFunc(rules, func(r any) bool { id, _ := r.(map[string]any)["id"].(string); return drop[id] })
	return json.Marshal(m)
}

// upgradeLegacy turns a document of schema 1 to 3 into schema 4, keeping what it means:
//   - objects.addresses and objects.services become aliases.addresses and aliases.ports, IDs unchanged;
//   - policy.input and policy.forward become one list, the input rules first with the firewall as their
//     destination. A packet only ever met one of the two lists, so their relative order never mattered;
//   - a side that listed zones and addresses together meant "any of these". A side now means "these zones and,
//     within them, these addresses", so such a rule becomes one rule per kind, next to each other.
func upgradeLegacy(m map[string]any) map[string]string {
	origin := map[string]string{}
	if objects, ok := m["objects"].(map[string]any); ok {
		m["aliases"] = map[string]any{"addresses": objects["addresses"], "ports": objects["services"]}
		delete(m, "objects")
	}
	if policy, ok := m["policy"].(map[string]any); ok {
		rules := []any{}
		for _, chain := range []string{"input", "forward"} {
			list, _ := policy[chain].([]any)
			for _, item := range list {
				old := item.(map[string]any)
				id, _ := old["id"].(string)
				tos := []map[string]any{{"zones": []any{firewallZone}, "addresses": []any{}}}
				if chain == "forward" {
					tos = legacySides(old["to"])
				}
				n := 0
				for _, from := range legacySides(old["from"]) {
					for _, to := range tos {
						r := maps.Clone(old)
						delete(r, "services")
						r["from"], r["to"], r["ports"] = from, to, old["services"]
						if n > 0 {
							derived := derivedRuleID(id, n)
							origin[derived] = id
							r["id"] = derived
						}
						n++
						rules = append(rules, r)
					}
				}
			}
		}
		m["rules"] = rules
		delete(m, "policy")
	}
	m["schemaVersion"] = legacyTarget
	return origin
}

// legacySides splits a legacy side, a list of zones and addresses of which any matched, into sides of one kind.
func legacySides(v any) []map[string]any {
	zones, addresses := []any{}, []any{}
	list, _ := v.([]any)
	for _, ref := range list {
		if s, _ := ref.(string); strings.HasPrefix(s, "zon_") {
			zones = append(zones, ref)
		} else {
			addresses = append(addresses, ref)
		}
	}
	if len(zones) > 0 && len(addresses) > 0 {
		return []map[string]any{{"zones": zones, "addresses": []any{}}, {"zones": []any{}, "addresses": addresses}}
	}
	return []map[string]any{{"zones": zones, "addresses": addresses}}
}

// derivedRuleID is the ID of the nth rule split from a legacy rule: the same on every load, since the legacy
// document is upgraded each time it is read.
func derivedRuleID(id string, n int) string {
	b := sha256.Sum256(fmt.Appendf(nil, "%s#%d", id, n))
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("rul_%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// carryLegacy makes the first version saved in the current form record every alias, every rule and the rule
// list as changed. That version and those after it then find their object versions in firewall_changes alone,
// and only the versions of the form before are read through legacyCollections.
func carryLegacy(next *savedState, after map[objectKey]any, changes map[objectKey]objectChange) {
	if !next.legacy {
		return
	}
	next.legacy, next.origin = false, nil
	carry := func(key objectKey) {
		if _, changed := changes[key]; !changed {
			changes[key] = objectChange{Collection: key.Collection, ID: key.ID}
		}
	}
	carry(listKey("rules"))
	for key := range after {
		if legacyCollections[key.Collection] != nil {
			carry(key)
		}
	}
}

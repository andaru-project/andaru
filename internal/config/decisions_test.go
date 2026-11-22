package config

import (
	"encoding/json"
	"testing"
)

// withDecisions replaces the decision records for one test.
func withDecisions(t *testing.T, records ...decision) {
	old := decisions
	decisions = records
	t.Cleanup(func() { decisions = old })
}

// zoneDecision asks, on schema from, whether to rename every zone; the provisional choice keeps them.
func zoneDecision(id string, from int, when func(zone map[string]any) bool) decision {
	zones := func(doc map[string]any) []map[string]any {
		var out []map[string]any
		list, _ := doc["zones"].([]any)
		for _, z := range list {
			if z, ok := z.(map[string]any); ok && when(z) {
				out = append(out, z)
			}
		}
		return out
	}
	return decision{
		id: id, from: from, question: "Rename the zones?", provisional: "keep",
		choices: []Choice{{ID: "keep", Label: "Keep"}, {ID: "rename", Label: "Rename"}, {ID: "mark", Label: "Mark"}},
		objects: func(doc map[string]any) []string {
			var ids []string
			for _, z := range zones(doc) {
				ids = append(ids, z["id"].(string))
			}
			return ids
		},
		apply: func(doc map[string]any, choice string) {
			for _, z := range zones(doc) {
				switch choice {
				case "rename":
					z["name"] = "renamed"
				case "mark":
					z["description"] = "marked"
				}
			}
		},
	}
}

func legacyAt(t *testing.T, schema int) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(legacyDocument(t), &doc); err != nil {
		t.Fatal(err)
	}
	doc["schemaVersion"] = schema
	if schema == 3 {
		doc["roles"] = []any{}
	}
	raw, err := canonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A document that needs a decision reads with its provisional choice and says so; an answer changes what it reads
// as, and an answer that is no choice is refused.
func TestDecisionReadsProvisionallyUntilAnswered(t *testing.T) {
	withDecisions(t, zoneDecision("rename", 1, func(map[string]any) bool { return true }))
	raw := legacyAt(t, 1)
	d, _, _, questions, err := decodeSavedWith(raw, nil)
	if err != nil || d.Zones[0].Name != "lan" || len(questions) != 1 || questions[0].Choice != "keep" || questions[0].Answered || questions[0].Objects[0] != legacyZone {
		t.Fatalf("provisional: %+v %+v %v", d.Zones, questions, err)
	}
	d, _, _, questions, err = decodeSavedWith(raw, map[string]string{"rename": "rename"})
	if err != nil || d.Zones[0].Name != "renamed" || !questions[0].Answered {
		t.Fatalf("answered: %+v %+v %v", d.Zones, questions, err)
	}
	if _, _, _, _, err = decodeSavedWith(raw, map[string]string{"rename": "nope"}); err == nil {
		t.Fatal("an answer that is no choice was accepted")
	}
	if _, _, _, _, err = decodeSavedWith(raw, map[string]string{"renme": "rename"}); err == nil {
		t.Fatal("an answer to no question was accepted")
	}
	// A document of the current schema meets no decision of an older one.
	current, err := canonical(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, questions, err = decodeSavedWith(current, nil); err != nil || len(questions) != 0 {
		t.Fatalf("current: %+v %v", questions, err)
	}
}

// Decisions run in schema order, each on the document the earlier ones left: a choice on schema 2 decides whether
// schema 3's question is asked at all.
func TestLaterDecisionSeesEarlierChoice(t *testing.T) {
	withDecisions(t,
		zoneDecision("first", 2, func(map[string]any) bool { return true }),
		zoneDecision("second", 3, func(z map[string]any) bool { return z["description"] == "marked" }))
	raw := legacyAt(t, 2)
	if _, _, _, questions, err := decodeSavedWith(raw, nil); err != nil || len(questions) != 1 || questions[0].ID != "first" {
		t.Fatalf("provisional: %+v %v", questions, err)
	}
	_, _, _, questions, err := decodeSavedWith(raw, map[string]string{"first": "mark"})
	if err != nil || len(questions) != 2 || questions[1].ID != "second" || questions[1].Answered {
		t.Fatalf("after marking: %+v %v", questions, err)
	}
	// A document saved under schema 3 never meets schema 2's decision.
	if _, _, _, questions, err = decodeSavedWith(legacyAt(t, 3), nil); err != nil || len(questions) != 0 {
		t.Fatalf("schema 3: %+v %v", questions, err)
	}
}

// A decision runs on the document of its schema, before the migration: a predicate reading a form only schemas 1 to
// 3 have (policy.forward) finds its objects.
func TestDecisionRunsBeforeTheMigration(t *testing.T) {
	record := zoneDecision("forward", 1, func(map[string]any) bool { return true })
	record.objects = func(doc map[string]any) []string {
		policy, _ := doc["policy"].(map[string]any)
		var ids []string
		forward, _ := policy["forward"].([]any)
		for _, r := range forward {
			ids = append(ids, r.(map[string]any)["id"].(string))
		}
		return ids
	}
	withDecisions(t, record)
	if _, _, _, questions, err := decodeSavedWith(legacyAt(t, 1), nil); err != nil || len(questions) != 1 || len(questions[0].Objects) == 0 {
		t.Fatalf("questions: %+v %v", questions, err)
	}
}

// Answers name their question by ID, so no two records share one.
func TestDecisionIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range decisions {
		if seen[d.id] {
			t.Fatalf("decision %q is recorded twice", d.id)
		}
		seen[d.id] = true
	}
}

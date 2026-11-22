package config

import (
	"fmt"
	"slices"
)

// decision is the user decision a breaking change needs (settings design, rule 3): which documents of a schema
// need it, what to ask, and what each choice does to the document. It runs on a document of schema from, before
// that schema's migration.
type decision struct {
	id, question string
	from         int
	choices      []Choice
	// provisional reads a document while nobody has answered: the choice closest to the old behaviour, or the one
	// that lets less traffic through.
	provisional string
	// objects returns the IDs the decision is about; none means the document does not need it.
	objects func(doc map[string]any) []string
	// apply makes a choice's change; the document stays of schema from.
	apply func(doc map[string]any, choice string)
}

// Choice is one answer to a Question.
type Choice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Question is a decision a document needs, as a reader sees it: Choice is the answer it was read with, the one
// given or, when not Answered, the provisional one.
type Question struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Choices     []Choice `json:"choices"`
	Provisional string   `json:"provisional"`
	Objects     []string `json:"objects"`
	Choice      string   `json:"choice"`
	Answered    bool     `json:"answered"`
}

// decisions holds the decision records. None exists yet: the first comes with the first breaking change that needs
// one, and the tests add their own.
var decisions []decision

// decide applies the decisions of schema from to doc, each with its answer or its provisional choice, and returns
// the questions doc needed. An answer that is not one of its question's choices is refused.
func decide(doc map[string]any, from int, answers map[string]string) ([]Question, error) {
	var out []Question
	for _, d := range decisions {
		if d.from != from {
			continue
		}
		objects := d.objects(doc)
		if len(objects) == 0 {
			continue
		}
		q := Question{ID: d.id, Question: d.question, Choices: slices.Clone(d.choices), Provisional: d.provisional, Objects: objects, Choice: d.provisional}
		if answer, ok := answers[d.id]; ok {
			if !slices.ContainsFunc(d.choices, func(c Choice) bool { return c.ID == answer }) {
				return nil, fault("VALIDATION_FAILED", "/choices/"+d.id, fmt.Sprintf("Expected one of the choices of question %q.", d.id))
			}
			q.Choice, q.Answered = answer, true
		}
		d.apply(doc, q.Choice)
		out = append(out, q)
	}
	return out, nil
}

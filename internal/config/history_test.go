package config

import (
	"encoding/json"
	"testing"
)

// Two versions that differ only in notifications realize the same network; any other difference, or a link secret's,
// does not.
func TestSameRealization(t *testing.T) {
	in := func(value string, secrets map[string]string) RealizationInput {
		return RealizationInput{Value: json.RawMessage(value), Secrets: secrets}
	}
	base := in(`{"rules":[{"id":"r1"}],"notifications":{"channels":[]}}`, map[string]string{"WAN": "a"})
	for _, c := range []struct {
		name  string
		other RealizationInput
		same  bool
	}{
		{"a channel added", in(`{"notifications":{"channels":[{"id":"c"}]},"rules":[{"id":"r1"}]}`, map[string]string{"WAN": "a"}), true},
		{"no notifications", in(`{"rules":[{"id":"r1"}]}`, map[string]string{"WAN": "a"}), true},
		{"a rule changed", in(`{"rules":[{"id":"r2"}],"notifications":{"channels":[]}}`, map[string]string{"WAN": "a"}), false},
		{"a link secret changed", in(`{"rules":[{"id":"r1"}],"notifications":{"channels":[]}}`, map[string]string{"WAN": "b"}), false},
	} {
		if got := SameRealization(base, c.other); got != c.same {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

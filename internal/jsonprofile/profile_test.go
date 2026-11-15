package jsonprofile

import (
	"strings"
	"testing"
)

func TestValidProfile(t *testing.T) {
	depth32 := strings.Repeat("[", 32) + "0" + strings.Repeat("]", 32)
	tests := []struct {
		name      string
		raw       []byte
		allowNull bool
		want      bool
	}{
		{name: "object", raw: []byte(`{"value":-123456789012345678}`), want: true},
		{name: "null allowed", raw: []byte(`{"value":null}`), allowNull: true, want: true},
		{name: "null rejected", raw: []byte(`{"value":null}`)},
		{name: "duplicate name", raw: []byte(`{"value":1,"value":2}`)},
		{name: "invalid UTF-8", raw: []byte{'"', 0xff, '"'}},
		{name: "trailing value", raw: []byte(`{} {}`)},
		{name: "empty", raw: nil},
		{name: "depth 32", raw: []byte(depth32), want: true},
		{name: "depth 33", raw: []byte("[" + depth32 + "]")},
		{name: "19 digit integer", raw: []byte(`9999999999999999999`), want: true},
		{name: "20 digit integer", raw: []byte(`10000000000000000000`)},
		{name: "fraction", raw: []byte(`1.5`)},
		{name: "exponent", raw: []byte(`1e2`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Valid(test.raw, test.allowNull); got != test.want {
				t.Fatalf("Valid(%q, %v) = %v, want %v", test.raw, test.allowNull, got, test.want)
			}
		})
	}
}

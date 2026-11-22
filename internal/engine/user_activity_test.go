package engine

import (
	"net/http/httptest"
	"testing"
)

func TestOnlyMarkedReadsAreNotActivity(t *testing.T) {
	for _, c := range []struct {
		method, header string
		activity       bool
	}{
		{"GET", "", true},
		{"GET", "1", false},
		{"HEAD", "1", false},
		{"GET", "yes", true},
		{"POST", "1", true},
		{"PUT", "1", true},
		{"DELETE", "1", true},
	} {
		r := httptest.NewRequest(c.method, "/api/v1/session", nil)
		if c.header != "" {
			r.Header.Set("X-Andaru-Background", c.header)
		}
		if got := userActivity(r); got != c.activity {
			t.Errorf("%s with %q: activity=%v, want %v", c.method, c.header, got, c.activity)
		}
	}
}

package aliasfeed

import (
	"fmt"
	"slices"
	"testing"
)

func TestMembers(t *testing.T) {
	got, err := Members([]string{"10.1.2.3/8", "2001:db8::1", "::ffff:192.0.2.1", "192.0.2.1", "2001:db8::/32"})
	if want := []string{"10.0.0.0/8", "192.0.2.1", "2001:db8::/32", "2001:db8::1"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %q %v, want %q", got, err, want)
	}
	if v4, v6 := Split(got); !slices.Equal(v4, got[:2]) || !slices.Equal(v6, got[2:]) {
		t.Fatalf("split %q %q", v4, v6)
	}
	if got, err := Members(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("an empty list: %q %v", got, err)
	}
	for _, bad := range []string{"example.com", "", "fe80::1%eth0", "::ffff:192.0.2.0/120", "10.0.0.0/33", "<html>"} {
		if _, err := Members([]string{"192.0.2.1", bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	many := make([]string, MaxMembers+1)
	for i := range many {
		many[i] = fmt.Sprintf("10.%d.%d.1", i/256, i%256)
	}
	if _, err := Members(many); err == nil {
		t.Fatal("more than MaxMembers accepted")
	}
	if _, err := Members(many[:MaxMembers]); err != nil {
		t.Fatal(err)
	}
}

package aliasfeed

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestHash(t *testing.T) {
	a := Hash("hostnames", "", []string{"B.example", "a.example"})
	if a != Hash("hostnames", "", []string{"a.example", "b.example"}) || !hashName.MatchString(a) {
		t.Fatalf("names not sorted and lower-cased: %s", a)
	}
	u := Hash("url", "https://x.example/list", nil)
	if u == Hash("url", "https://x.example/other", nil) || u == a || !hashName.MatchString(u) {
		t.Fatalf("hashes collide: %s %s", u, a)
	}
}

func TestLoadMovesAsideWhatDoesNotDecode(t *testing.T) {
	dir := t.TempDir()
	h := Hash("url", "https://x.example/list", nil)
	if s, err := Load(dir, "adr_1", h); err != nil || s.Members != nil {
		t.Fatalf("no state yet: %+v %v", s, err)
	}
	path := Path(dir, "adr_1", h)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"members":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Sources(dir); !slices.Equal(got["adr_1"], []string{h}) {
		t.Fatalf("sources %v", got)
	}
	if _, err := Load(dir, "adr_1", h); err == nil {
		t.Fatal("a corrupt state read")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the corrupt state stayed: %v", err)
	}
	aside, _ := filepath.Glob(path + ".corrupt-*")
	if data, err := os.ReadFile(aside[0]); len(aside) != 1 || err != nil || string(data) != `{"members":[` {
		t.Fatalf("moved aside %v: %q %v", aside, data, err)
	}
	if got := Sources(dir); len(got) != 0 {
		t.Fatalf("a moved file is a source: %v", got)
	}
}

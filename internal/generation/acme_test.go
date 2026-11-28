package generation

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestACMEHTTP01(t *testing.T) {
	var calls []string
	missing := false
	r := Realizer{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if missing {
			return nil, errors.New("No such file or directory")
		}
		return nil, nil
	}}
	if err := r.ACMEHTTP01(context.Background(), []string{"ens18", "br0.10"}); err != nil {
		t.Fatal(err)
	}
	if err := r.ACMEHTTP01(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{`/usr/sbin/nft add element inet andaru acme_http01_active { "ens18", "br0.10" }`, "/usr/sbin/nft flush set inet andaru acme_http01_active"}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls %q", calls)
	}
	// Closing when the filter holds no set succeeds: no HTTP-01 order is applied.
	missing = true
	if err := r.ACMEHTTP01(context.Background(), nil); err != nil {
		t.Fatalf("close without the set: %v", err)
	}
	// Non-ENOENT errors (e.g. permission denied or cancelled context) must not be swallowed.
	rPerm := Realizer{Run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("Operation not permitted")
	}}
	if err := rPerm.ACMEHTTP01(context.Background(), nil); err == nil {
		t.Fatal("permission failure was swallowed")
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.ACMEHTTP01(cctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled close returned %v, want context.Canceled", err)
	}
	if err := r.ACMEHTTP01(context.Background(), []string{`x" }; flush ruleset; {"`}); err == nil {
		t.Fatal("an injected name was accepted")
	}
	for name, ok := range map[string]bool{"ens18": true, "wg0": true, "br0.10": true, "": false, ".": false, "..": false, "a b": false, "sixteen-chars-xx": false, "-x": false} {
		if ValidIfname(name) != ok {
			t.Errorf("ValidIfname(%q) != %v", name, ok)
		}
	}
}

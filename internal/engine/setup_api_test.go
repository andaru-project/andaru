package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/config"
)

func TestSetupCodeStoreLifecycleAndPerSourceRateLimit(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	store := &setupCodeStore{stateDir: dir, now: func() time.Time { return now }}

	code, err := store.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(code) != 19 || strings.Count(code, "-") != 3 {
		t.Fatalf("unexpected formatted code %q", code)
	}
	info, err := os.Stat(filepath.Join(dir, "setup-code"))
	if err != nil {
		t.Fatalf("stat setup-code: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("setup-code mode: got %o, want 0600", info.Mode().Perm())
	}

	// Reload from disk returns the exact same code.
	reloaded := newSetupCodeStore(dir)
	again, err := reloaded.Current()
	if err != nil || again != code {
		t.Fatalf("reloaded code: got %q (%v), want %q", again, err, code)
	}

	// Case-insensitive and dash-free verification succeeds.
	if err := store.Verify("192.0.2.10", strings.ToLower(strings.ReplaceAll(code, "-", ""))); err != nil {
		t.Fatalf("Verify normalized valid code: %v", err)
	}

	// Five bad attempts from one source rate-limit that source only (not another source).
	for i := range setupMaxFailedAttempts {
		if err := store.Verify("192.0.2.99", "0000-0000-0000-0000"); !errors.Is(err, errSetupCodeInvalid) {
			t.Fatalf("attempt %d: got %v, want errSetupCodeInvalid", i, err)
		}
	}
	if err := store.Verify("192.0.2.99", code); !errors.Is(err, errSetupCodeRateLimited) {
		t.Fatalf("expected rate limit for abusing source, got %v", err)
	}
	if err := store.Verify("192.0.2.10", code); err != nil {
		t.Fatalf("real admin source must not be locked out by another source: %v", err)
	}

	// Advancing past backoff window allows the rate-limited source again.
	now = now.Add(setupBackoffWindow + time.Second)
	if err := store.Verify("192.0.2.99", code); err != nil {
		t.Fatalf("Verify after backoff window: %v", err)
	}

	// Rotate replaces the code; Clear removes the file and prevents regeneration.
	rotated, err := store.Rotate()
	if err != nil || rotated == code {
		t.Fatalf("Rotate: got %q (%v), want new code", rotated, err)
	}
	store.Clear()
	if _, err := os.Stat(filepath.Join(dir, "setup-code")); !os.IsNotExist(err) {
		t.Fatalf("setup-code must be removed after Clear, got err=%v", err)
	}
	if _, err := store.Current(); !errors.Is(err, errSetupCodeConsumed) {
		t.Fatalf("Current after Clear must fail with errSetupCodeConsumed, got %v", err)
	}
}

func TestValidateHostnameAndHandler(t *testing.T) {
	got, err := ValidateHostname("  Andaru-Edge01  ")
	if err != nil || got != "andaru-edge01" {
		t.Fatalf("ValidateHostname: got %q (%v)", got, err)
	}
	for _, bad := range []string{"", "-edge", "edge-", "edge.local", "bad host", strings.Repeat("a", 64)} {
		if _, err := ValidateHostname(bad); err == nil {
			t.Fatalf("expected invalid hostname for %q", bad)
		}
	}

	var applied string
	h := newHostnameHandler(func(_ context.Context, name string) error {
		applied = name
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/hostname", strings.NewReader(`{"hostname":"Edge-GW"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || applied != "edge-gw" {
		t.Fatalf("POST hostname: code=%d applied=%q body=%s", rec.Code, applied, rec.Body.String())
	}
	var reply map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply["hostname"] != "edge-gw" {
		t.Fatalf("unexpected reply: %v %s", err, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/system/hostname", strings.NewReader(`{"hostname":"bad.name"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid hostname: got %d, want 422", rec.Code)
	}
}

// TestSetupWizardBatchDecodesCleanly verifies the exact JSON shape emitted by web/src/setup.ts
// passes Andaru's real config.DecodeChanges schema and precondition checks.
func TestSetupWizardBatchDecodesCleanly(t *testing.T) {
	batch := `{
		"expectedSavedVersion": "0",
		"operations": [
			{"op":"create","collection":"zones","localId":"lan_zone","value":{"name":"LAN"}},
			{"op":"create","collection":"links","localId":"mgmt_link","value":{"name":"enp1s0","kind":"ethernet","permanentMac":"52:54:00:aa:bb:01","mtu":1500,"enabled":true}},
			{"op":"create","collection":"zones","localId":"wan_zone","value":{"name":"WAN"}},
			{"op":"create","collection":"links","localId":"wan_link","value":{"name":"enp2s0","kind":"ethernet","permanentMac":"52:54:00:aa:bb:02","mtu":1500,"enabled":true}},
			{"op":"create","collection":"segments","localId":"wan_seg","value":{"name":"WAN","link":"@wan_link","zone":"@wan_zone","addressing":{"ipv4":{"mode":"static","addresses":["203.0.113.2/30"],"gateway":"203.0.113.1"},"ipv6":{"mode":"none"}}}},
			{"op":"create","collection":"segments","localId":"mgmt_seg","value":{"name":"Management LAN","link":"@mgmt_link","zone":"@lan_zone","addressing":{"ipv4":{"mode":"static","addresses":["192.168.1.1/24"]},"ipv6":{"mode":"none"}}}},
			{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"@mgmt_seg"}},
			{"op":"create","collection":"dhcp","value":{"name":"Management DHCP","segment":"@mgmt_seg","ranges":[{"first":"192.168.1.100","last":"192.168.1.199"}]}},
			{"op":"replace","collection":"dns","expectedVersion":"0","value":{"mode":"forward","upstreams":[{"address":"1.1.1.1","protocol":"do53"}],"segments":["@mgmt_seg"]}},
			{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}}
		]
	}`
	if _, err := config.DecodeChanges([]byte(batch)); err != nil {
		t.Fatalf("DecodeChanges rejected setup wizard batch: %v", err)
	}
}

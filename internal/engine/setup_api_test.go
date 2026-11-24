package engine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSetupCodeStoreLifecycleAndRateLimit(t *testing.T) {
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
	if err := store.Verify(strings.ToLower(strings.ReplaceAll(code, "-", ""))); err != nil {
		t.Fatalf("Verify normalized valid code: %v", err)
	}

	// Five bad attempts trigger rate limiting.
	for i := range setupMaxFailedAttempts {
		if err := store.Verify("0000-0000-0000-0000"); !errors.Is(err, errSetupCodeInvalid) {
			t.Fatalf("attempt %d: got %v, want errSetupCodeInvalid", i, err)
		}
	}
	if err := store.Verify(code); !errors.Is(err, errSetupCodeRateLimited) {
		t.Fatalf("expected rate limit even for valid code, got %v", err)
	}

	// Advancing past backoff window allows verification again.
	now = now.Add(setupBackoffWindow + time.Second)
	if err := store.Verify(code); err != nil {
		t.Fatalf("Verify after backoff window: %v", err)
	}

	// Rotate replaces the code; Clear removes the file.
	rotated, err := store.Rotate()
	if err != nil || rotated == code {
		t.Fatalf("Rotate: got %q (%v), want new code", rotated, err)
	}
	store.Clear()
	if _, err := os.Stat(filepath.Join(dir, "setup-code")); !os.IsNotExist(err) {
		t.Fatalf("setup-code must be removed after Clear, got err=%v", err)
	}
}

func TestValidateAuthorizedKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	valid := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " admin@laptop"

	out, err := validateAuthorizedKey(valid)
	if err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if !strings.HasSuffix(string(out), " admin@laptop\n") {
		t.Fatalf("unexpected marshaled key: %q", string(out))
	}
	if out, err := validateAuthorizedKey("   "); err != nil || len(out) != 0 {
		t.Fatalf("blank key: got %q %v", out, err)
	}
	for _, bad := range []string{
		"not-a-key",
		`command="/bin/sh" ` + valid,
		valid + "\n" + valid,
	} {
		if _, err := validateAuthorizedKey(bad); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
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
	h := newHostnameHandler(hostnameRunnerFunc(func(_ context.Context, name string) error {
		applied = name
		return nil
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/hostname", strings.NewReader(`{"hostname":"Edge-GW"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || applied != "edge-gw" {
		t.Fatalf("POST hostname: code=%d applied=%q body=%s", rec.Code, applied, rec.Body.String())
	}
	var reply map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply["hostname"] != "edge-gw" {
		t.Fatalf("unexpected reply: %v %s", err, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/system/hostname", strings.NewReader(`{"hostname":"bad.name"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid hostname: got %d, want 422", rec.Code)
	}
}

type hostnameRunnerFunc func(ctx context.Context, hostname string) error

func (f hostnameRunnerFunc) SetHostname(ctx context.Context, hostname string) error {
	return f(ctx, hostname)
}

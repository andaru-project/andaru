package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadAddonBusTokensFileValidation(t *testing.T) {
	// Relative path rejected before touching disk.
	if _, err := readAddonBusTokensFile("relative.json"); err == nil {
		t.Fatal("expected error for relative path")
	}

	// Non-0600 file rejected.
	dir := t.TempDir()
	badMode := filepath.Join(dir, "tokens.json")
	if err := os.WriteFile(badMode, []byte(`{"sample":"abc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAddonBusTokensFile(badMode); err == nil {
		t.Fatal("expected error for 0644 file")
	}

	// Symlink rejected via O_NOFOLLOW.
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, []byte(`{"sample":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readAddonBusTokensFile(link); err == nil {
		t.Fatal("expected error for symlink")
	}

	// Relative path to realizeCommand addon-bus-tokens is a usage error (exit 2).
	var stdout, stderr bytes.Buffer
	code := realizeCommand(context.Background(), []string{"addon-bus-tokens", "rel.json"}, &stdout, &stderr, func(string, string) (realizeVerbs, error) {
		return &fakeVerbs{}, nil
	})
	if code != 2 {
		t.Fatalf("realizeCommand relative path exit = %d, want 2 (stderr: %s)", code, stderr.String())
	}

	if os.Geteuid() != 0 {
		return
	}
	// Root-only valid & malformed JSON checks in a root-owned 0700 directory.
	rootDir, err := os.MkdirTemp("/var/tmp", "andaru-tok-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rootDir) })
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(rootDir, "tokens.json")
	if err := os.WriteFile(good, []byte(`{"sample":"0123456789abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readAddonBusTokensFile(good)
	if err != nil {
		t.Fatalf("readAddonBusTokensFile valid: %v", err)
	}
	if got["sample"] != "0123456789abcdef" {
		t.Fatalf("got %+v", got)
	}
	for name, body := range map[string]string{
		"bad slug":    `{"Bad_Slug":"abc"}`,
		"empty token": `{"sample":""}`,
		"newline":     `{"sample":"a\nb"}`,
		"trailing":    `{"sample":"abc"}{}`,
		"null":        `null`,
		"malformed":   `not-json`,
	} {
		p := filepath.Join(rootDir, name+".json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readAddonBusTokensFile(p); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

func TestWriteAddonLogOutput(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("writeAddonLogOutput requires root-owned state directory")
	}
	stateDir, err := os.MkdirTemp("/var/tmp", "andaru-log-state-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	catDir := t.TempDir()
	slugDir := filepath.Join(catDir, "sample")
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "manifestVersion": 1,
  "requires": [{"name": "andaru.extension-api", "version": ">=1.0.0 <2.0.0"}],
  "id": "org.andaru.sample",
  "slug": "sample",
  "name": "Sample",
  "version": "1.0.0",
  "application": {"name": "Sample", "version": "1.0"},
  "permissions": [{"name": "logs", "title": "Logs"}],
  "runtime": {"image": "ghcr.io/example/sample@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "network": "host"},
  "storage": [{"name": "logs", "purpose": "state", "mountPath": "/var/log/sample"}],
  "logs": [{"name": "appfile", "title": "File", "permission": "logs", "source": {"type": "file", "storage": "logs", "path": "app.log"}}]
}`
	if err := os.WriteFile(filepath.Join(slugDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(stateDir, "addons", "sample", "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "app.log"), []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(stateDir, "out.json")
	if err := os.WriteFile(outPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAddonLogOutput(catDir, stateDir, "sample", "appfile", 2, outPath); err != nil {
		t.Fatalf("writeAddonLogOutput: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Lines     []string `json:"lines"`
		Truncated bool     `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || !slices.Equal(got.Lines, []string{"b", "c"}) || !got.Truncated {
		t.Fatalf("got %+v (err=%v)", got, err)
	}

	// Output path outside stateDir is rejected.
	outside := filepath.Join(t.TempDir(), "escape.json")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAddonLogOutput(catDir, stateDir, "sample", "appfile", 2, outside); err == nil {
		t.Fatal("expected rejection for output path outside stateDir")
	}
}

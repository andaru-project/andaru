package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/generation"
)

func TestWriteIPsecStatus(t *testing.T) {
	dir := t.TempDir()
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if filepath.Base(name) == "systemctl" {
			return []byte("inactive\n"), errors.New("exit status 3")
		}
		return nil, errors.New("strongSwan is stopped")
	}
	out := filepath.Join(dir, "ipsec-status-1234.json")
	if err := writeIPsecStatus(t.Context(), dir, out, run); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	var got generation.IPsecStatus
	if err != nil || json.Unmarshal(raw, &got) != nil || got.ServiceActive || got.Connections == nil || got.SAs == nil {
		t.Fatalf("%s %v", raw, err)
	}
	for _, bad := range []string{filepath.Join(dir, "sub", "ipsec-status-1.json"), filepath.Join(dir, "wg-status-1.json"), filepath.Join(dir, "config.db")} {
		if err := writeIPsecStatus(t.Context(), dir, bad, run); err == nil {
			t.Fatalf("unexpectedly accepted %q", bad)
		}
	}
	link := filepath.Join(dir, "ipsec-status-link.json")
	if err := os.Symlink(filepath.Join(dir, "config.db"), link); err != nil {
		t.Fatal(err)
	}
	if err := writeIPsecStatus(t.Context(), dir, link, run); err == nil {
		t.Fatal("symlink target was accepted")
	}
}

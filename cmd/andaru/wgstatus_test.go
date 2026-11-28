package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
)

func TestWriteWireGuardStatus(t *testing.T) {
	dir := t.TempDir()
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("wg0\tPRIVATE=\tPUB=\t51820\toff\nwg0\tPEER=\tPSK=\t(none)\t10.0.0.2/32\t0\t0\t0\toff\n"), nil
	}
	out := filepath.Join(dir, "wg-status.json")
	if err := writeWireGuardStatus(t.Context(), dir, out, run, time.Now()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	var got generation.WireGuardStatus
	if err != nil || json.Unmarshal(raw, &got) != nil || len(got.Interfaces) != 1 || got.Interfaces[0].Peers[0].PublicKey != "PEER=" {
		t.Fatalf("%s %v", raw, err)
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := writeWireGuardStatus(t.Context(), dir, filepath.Join(dir, "sub", "x.json"), run, time.Now()); err == nil {
		t.Fatal("a path below the state directory was accepted")
	}
}

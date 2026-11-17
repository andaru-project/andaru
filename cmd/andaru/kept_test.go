package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRecoveryReplaces(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(t.TempDir(), "kept")
	keptTestRoot = root
	t.Cleanup(func() { keptTestRoot = "" })
	load := func() ([]byte, map[string]string, error) {
		return []byte(`{"links":[{"password":{"secret":"WAN_PASSWORD"}}]}`), map[string]string{"WAN_PASSWORD": "TEST_ONLY_SECRET"}, nil
	}
	var out, errOut bytes.Buffer
	if code := storeRecovery(context.Background(), "", state, nil, &out, &errOut, load); code != 0 {
		t.Fatalf("code %d %s", code, errOut.String())
	}
	body := readRecovery(t, root)
	if !strings.Contains(body, "TEST_ONLY_SECRET") || strings.Contains(body, "account") {
		t.Fatalf("body %s", body)
	}
	load = func() ([]byte, map[string]string, error) {
		return []byte(`{"links":[]}`), map[string]string{"WAN_PASSWORD": "TEST_ONLY_SECOND"}, nil
	}
	if code := storeRecovery(context.Background(), "", state, nil, &out, &errOut, load); code != 0 {
		t.Fatal(code)
	}
	if body = readRecovery(t, root); !strings.Contains(body, "TEST_ONLY_SECOND") || strings.Contains(body, "TEST_ONLY_SECRET") {
		t.Fatalf("replaced body %s", body)
	}
}

func TestStoreRecoveryRefusesFlags(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(t.TempDir(), "kept")
	keptTestRoot = root
	t.Cleanup(func() { keptTestRoot = "" })
	var out, errOut bytes.Buffer
	for _, args := range [][]string{{"extra"}, {"--password", "secret"}} {
		if code := storeRecovery(context.Background(), "", state, args, &out, &errOut, nil); code != 2 || out.Len() != 0 {
			t.Fatalf("args %q code %d out %q", args, code, out.String())
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "recovery", "recovery.json")); !os.IsNotExist(err) {
		t.Fatal("refused command wrote recovery")
	}
}

func readRecovery(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "recovery", "recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

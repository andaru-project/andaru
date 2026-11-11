//go:build integration

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storeFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		// The -shm file is SQLite's lock and index memory; readers change it too.
		if err == nil && info.Mode().IsRegular() && !strings.HasSuffix(path, "-shm") {
			data, _ := os.ReadFile(path)
			out[path] = data
		}
		return nil
	})
	return out
}

func TestDraftPublishesNothing(t *testing.T) {
	dir := ownedDir(t)
	s := openTestStore(t, dir)
	if _, err := s.Save(context.Background(), "unix:uid:0", createZone("existing")); err != nil {
		t.Fatal(err)
	}
	before := storeFiles(t, dir)
	batch, _ := DecodeChanges([]byte(`{"expectedSavedVersion":"1","operations":[{"op":"create","collection":"links","localId":"up","value":{"name":"up","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:aa:bb:01"}}]}`))
	draft, err := s.Draft(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if draft.SavedVersion != "1" || !bytes.Contains(draft.Value, []byte(`"existing"`)) || !bytes.Contains(draft.Value, []byte(`"permanentMac":"52:54:00:aa:bb:01"`)) {
		t.Fatalf("draft = %s at %s", draft.Value, draft.SavedVersion)
	}
	after := storeFiles(t, dir)
	for path, data := range before {
		if !bytes.Equal(after[path], data) {
			t.Fatalf("%s changed", path)
		}
	}
	if head, _ := s.RealizationInput(context.Background(), nil); head.SavedVersion != "1" {
		t.Fatalf("head moved to %s", head.SavedVersion)
	}

	bad := createZone("")
	if _, err := s.Draft(context.Background(), bad); err == nil {
		t.Fatal("an invalid draft was accepted")
	}
	// A draft is only previewed, so it must never carry a secret value.
	secret, _ := DecodeChanges([]byte(`{"operations":[{"op":"create","collection":"links","value":{"name":"p","kind":"pppoe","mtu":1492,"enabled":true,"parent":"lnk_00000000-0000-4000-8000-000000000001","username":"u","password":{"set":"x"}}}]}`))
	_, err = s.Draft(context.Background(), secret)
	f := requireFault(t, err, "VALIDATION_FAILED")
	raw, _ := json.Marshal(f.Details)
	if !strings.Contains(string(raw), "/operations/0/value/password") {
		t.Fatalf("details = %s", raw)
	}
}

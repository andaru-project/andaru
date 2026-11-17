package kept

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestRecordLastLAN(t *testing.T) {
	state := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(Root) })
	writeLive(t, state, 1, "7")
	extractLAN = func(context.Context, string, string) ([]byte, error) { return []byte("lan-7"), nil }
	t.Cleanup(func() { extractLAN = extractFromStore })
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(Root, "first", "first.json"), []byte("keep"), 0o600); err == nil {
		t.Fatal("first should not exist yet")
	}
	_ = os.RemoveAll(Root)
	if err := RecordLastLAN(ctx, state, "7", authz.Actor{Kind: authz.ActorAccount, ID: "00000000-0000-4000-8000-000000000001"}); err != nil {
		t.Fatal(err)
	}
	got, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.Read("last-lan")
	if err != nil || string(body) != "lan-7" {
		t.Fatalf("last-lan %q %v", body, err)
	}
	if err = os.MkdirAll(filepath.Join(Root, "first"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(Root, "first", "first.json"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = RecordLastLAN(ctx, state, "7", authz.Actor{Kind: authz.ActorAccount, ID: "00000000-0000-4000-8000-000000000001"}); err != nil {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(filepath.Join(Root, "first", "first.json"))
	if err != nil || string(kept) != "keep" {
		t.Fatalf("first changed: %q %v", kept, err)
	}
	_ = os.RemoveAll(Root)
	for _, actor := range []authz.Actor{{Kind: authz.ActorToken, ID: "tok"}, {Kind: authz.ActorSystem, ID: "lease"}} {
		if err = RecordLastLAN(ctx, state, "7", actor); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Lstat(filepath.Join(Root, "last-lan", "last-lan.json")); !os.IsNotExist(err) {
			t.Fatalf("actor %s wrote last-lan", actor.Kind)
		}
	}
	if err = RecordLastLAN(ctx, state, "8", authz.Actor{Kind: authz.ActorAccount, ID: "00000000-0000-4000-8000-000000000001"}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(Root, "last-lan", "last-lan.json")); !os.IsNotExist(err) {
		t.Fatal("stale version wrote last-lan")
	}
}

func TestRecordFirstOnce(t *testing.T) {
	state := t.TempDir()
	t.Cleanup(func() { _ = os.RemoveAll(Root) })
	extractLAN = func(context.Context, string, string) ([]byte, error) { return []byte("first-lan"), nil }
	t.Cleanup(func() { extractLAN = extractFromStore })
	ctx := context.Background()
	if err := RecordFirst(ctx, state, "7"); err != nil {
		t.Fatal(err)
	}
	if err := RecordFirst(ctx, state, "9"); err != nil {
		t.Fatal(err)
	}
	s, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.Read("first")
	if err != nil || string(body) != "first-lan" {
		t.Fatalf("first %q %v", body, err)
	}
	_ = os.RemoveAll(Root)
	extractLAN = func(context.Context, string, string) ([]byte, error) { return nil, errUplink }
	if err = RecordFirst(ctx, state, "7"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(Root, "first")); !os.IsNotExist(err) {
		t.Fatal("uplink created first")
	}
}

func writeLive(t *testing.T, state string, id int, version string) {
	t.Helper()
	dir := filepath.Join(state, "generations", "1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"live": id})
	if err := os.WriteFile(filepath.Join(state, "generations", "pointers.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"id": id, "savedVersion": version})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
}

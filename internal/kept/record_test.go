package kept

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestRecordLiveLAN(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kept")
	state := t.TempDir()
	ctx := context.Background()
	actor := authz.Actor{Kind: authz.ActorAccount, ID: "00000000-0000-4000-8000-000000000001"}
	if err := RecordLiveLAN(ctx, root, state, actor, []byte("lan-live"), nil); err != nil {
		t.Fatal(err)
	}
	s, err := OpenIn(root, state)
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.Read("last-lan")
	if err != nil || string(body) != "lan-live" {
		t.Fatalf("%q %v", body, err)
	}
	if err = CopyFirst(ctx, root, state, []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if err = RecordLiveLAN(ctx, root, state, actor, []byte("lan-live"), nil); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(root, "first", "first.json"))
	if err != nil || string(first) != "keep" {
		t.Fatalf("first %q %v", first, err)
	}
	_ = os.RemoveAll(filepath.Join(root, "last-lan"))
	for _, other := range []authz.Actor{{Kind: authz.ActorToken, ID: "tok"}, {Kind: authz.ActorSystem, ID: "lease"}} {
		if err = RecordLiveLAN(ctx, root, state, other, []byte("no"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = os.Lstat(filepath.Join(root, "last-lan", "last-lan.json")); !os.IsNotExist(err) {
		t.Fatal("non-admin wrote last-lan")
	}
	if err = RecordLiveLAN(ctx, root, state, actor, nil, errors.New("UPLINK_MANAGEMENT")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(root, "last-lan", "last-lan.json")); !os.IsNotExist(err) {
		t.Fatal("uplink wrote last-lan")
	}
}

func TestCopyFirstOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kept")
	state := t.TempDir()
	ctx := context.Background()
	if err := CopyFirst(ctx, root, state, []byte("first-lan")); err != nil {
		t.Fatal(err)
	}
	if err := CopyFirst(ctx, root, state, []byte("later")); err != nil {
		t.Fatal(err)
	}
	s, err := OpenIn(root, state)
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.Read("first")
	if err != nil || string(body) != "first-lan" {
		t.Fatalf("%q %v", body, err)
	}
}

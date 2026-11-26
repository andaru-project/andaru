//go:build integration

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

// The live aliases are those of the saved version the live generation was applied from, not the saved head's; a
// saved version that does not read is taken as the saved head.
func TestSavedAndLiveAliases(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()
	stateDir := filepath.Join(privateTestDir(t), "state")
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var versions []config.Version
	for _, value := range []string{
		`{"name":"cloudflare","kind":"url","url":"https://www.cloudflare.com/ips-v4","refreshSeconds":3600}`,
		`{"name":"vpn","kind":"hostnames","names":["vpn.example.com"],"refreshSeconds":300}`,
	} {
		change, err := config.ObjectChange("create", "aliases.addresses", "", nil, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		saved, err := store.Save(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, change)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, saved.SavedVersion)
	}
	aliases := savedAndLive(stateDir, store)
	if saved, live, err := aliases(ctx); err != nil || len(saved) != 2 || live != nil {
		t.Fatalf("before an apply: %+v %+v %v", saved, live, err)
	}
	dir := generation.Dir(stateDir)
	live := func(version string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "1"), 0o700); err != nil ||
			os.WriteFile(filepath.Join(dir, "pointers.json"), []byte(`{"confirmed":1,"lastKnownGood":1,"pending":null,"live":1}`), 0o600) != nil ||
			os.WriteFile(filepath.Join(dir, "1", "manifest.json"), []byte(`{"id":1,"savedVersion":"`+version+`","files":[]}`), 0o600) != nil {
			t.Fatal("write the live generation")
		}
	}
	// Live at the older version: one alias, while the saved head has two.
	live(string(versions[0]))
	saved, got, err := aliases(ctx)
	if err != nil || len(saved) != 2 || len(got) != 1 || got[0].Name != "cloudflare" {
		t.Fatalf("live at %s: %+v %+v %v", versions[0], saved, got, err)
	}
	// A version the store no longer has.
	live("999")
	if saved, got, err = aliases(ctx); err != nil || len(saved) != 2 || len(got) != 2 {
		t.Fatalf("live at a version gone: %+v %+v %v", saved, got, err)
	}
}

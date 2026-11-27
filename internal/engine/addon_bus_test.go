package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

func testBusAddonCatalog(t *testing.T) *addon.Catalog {
	t.Helper()
	dir := t.TempDir()
	slugDir := filepath.Join(dir, "sample")
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "manifestVersion": 1,
  "requires": [{"name": "andaru.extension-api", "version": ">=1.0.0 <2.0.0"}],
  "id": "org.andaru.sample",
  "slug": "sample",
  "name": "Sample",
  "description": "Sample add-on",
  "version": "1.0.0",
  "application": {"name": "Sample", "version": "1.0.0"},
  "permissions": [{"name": "view", "title": "View"}],
  "runtime": {
    "image": "ghcr.io/example/sample@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "network": "host",
    "profile": "unprivileged"
  },
  "bus": {
    "publish": [
      {"topic": "notification/addon.sample", "title": "Notifications"},
      {"topic": "addon/sample/status", "title": "Status"}
    ],
    "subscribe": [
      {"topic": "apply/+", "title": "Apply events"},
      {"topic": "config/head", "title": "Config head"}
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(slugDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := addon.LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func seedLiveGeneration(t *testing.T, stateDir string, id int, savedVersion string, slugs ...string) {
	t.Helper()
	dir := generation.Dir(stateDir)
	files := []generation.FileEntry{}
	contents := map[string][]byte{}
	for _, slug := range slugs {
		p := addonQuadletDir + "andaru-addon-" + slug + ".container"
		files = append(files, generation.FileEntry{Path: p, Mode: "0600"})
		contents[p] = []byte("[Container]\nImage=ghcr.io/example/" + slug + "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n")
	}
	gotID, err := generation.Create(dir, generation.Manifest{
		SavedVersion:          savedVersion,
		CreatedAt:             time.Now().UTC(),
		ConfirmTimeoutSeconds: 120,
		Files:                 files,
	}, contents)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != id {
		t.Fatalf("created generation %d, want %d", gotID, id)
	}
	raw, err := json.Marshal(generation.Pointers{Live: &id, Confirmed: &id})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pointers.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAddonBusSyncLifecycleAndRevocation(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	runDir, err := os.MkdirTemp("", "addon-bus-run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })

	addonSock := filepath.Join(runDir, "addon.sock")
	l, err := net.Listen("unix", addonSock)
	if err != nil {
		t.Fatal(err)
	}
	events, err := bus.Open(bus.Options{
		Path:           filepath.Join(stateDir, "bus.db"),
		TokenListeners: map[string]net.Listener{"addons": l},
	})
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	defer events.Close()

	// Seed a stale temp file from a crashed engine to confirm startup cleanup.
	staleTemp := filepath.Join(runDir, "addon-bus-tokens-stale.json")
	if err := os.WriteFile(staleTemp, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cat := testBusAddonCatalog(t)
	realizer := generation.Realizer{Root: root}

	grantsByVersion := map[config.Version][]config.AddonBusGrant{
		"1": {{
			ID:      "org.andaru.sample",
			Enabled: true,
			Publish: map[string]string{
				"notification/addon.sample": "granted",
				"addon/sample/status":       "denied",
			},
			Subscribe: map[string]string{
				"apply/+":     "granted",
				"config/head": "denied",
			},
		}},
		"2": {{
			ID:      "org.andaru.sample",
			Enabled: true,
			Publish: map[string]string{
				"notification/addon.sample": "denied",
			},
			Subscribe: map[string]string{
				"apply/+":     "denied",
				"config/head": "granted",
			},
		}},
		"3": {{
			ID:      "org.andaru.sample",
			Enabled: false,
			Publish: map[string]string{"notification/addon.sample": "granted"},
		}},
	}

	realizeCalls := 0
	var failRealize error
	syncer := newAddonBusSync(
		root, stateDir, runDir, cat, events,
		func(_ context.Context, v config.Version) ([]config.AddonBusGrant, error) {
			return grantsByVersion[v], nil
		},
		func(ctx context.Context, tokensFile string) error {
			realizeCalls++
			if failRealize != nil {
				return failRealize
			}
			raw, err := os.ReadFile(tokensFile)
			if err != nil {
				return err
			}
			var tokens map[string]string
			if err := json.Unmarshal(raw, &tokens); err != nil {
				return err
			}
			return realizer.SyncAddonBusTokens(ctx, tokens)
		},
	)

	// 1. No live generation -> fast path skips realize, cleans stale temp file.
	if retry := syncer.sync(context.Background()); retry {
		t.Fatal("unexpected retry with no live generation")
	}
	if realizeCalls != 0 {
		t.Fatalf("realizeCalls = %d, want 0 with no add-ons", realizeCalls)
	}
	if _, err := os.Stat(staleTemp); !os.IsNotExist(err) {
		t.Fatalf("stale temp file not cleaned: %v", err)
	}

	// 2. Generation 1 goes live with granted topics -> token written (64 hex chars) and client connects.
	seedLiveGeneration(t, stateDir, 1, "1", "sample")
	if retry := syncer.sync(context.Background()); retry {
		t.Fatal("sync gen 1 requested retry")
	}
	if realizeCalls != 1 {
		t.Fatalf("realizeCalls = %d, want 1", realizeCalls)
	}
	tok1Bytes, err := os.ReadFile(root + "/run/andaru-addons/sample/bus/token")
	if err != nil {
		t.Fatalf("read token 1: %v", err)
	}
	tok1 := string(tok1Bytes)
	if len(tok1) != 64 {
		t.Fatalf("token length = %d, want 64 (256-bit hex)", len(tok1))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c1, err := bus.Dial(ctx, addonSock, "addon.sample", tok1)
	if err != nil {
		t.Fatalf("dial gen 1: %v", err)
	}
	defer c1.Close()
	if err := c1.Subscribe(ctx, "apply/+"); err != nil {
		t.Fatalf("subscribe granted apply/+: %v", err)
	}
	var refused *bus.RefusedError
	if err := c1.Subscribe(ctx, "config/head"); !errors.As(err, &refused) {
		t.Fatalf("denied config/head err = %v, want RefusedError", err)
	}

	// Second sync for the same live generation is a no-op (no unnecessary rotation).
	if retry := syncer.sync(context.Background()); retry || realizeCalls != 1 {
		t.Fatalf("duplicate sync ran: retry=%v calls=%d", retry, realizeCalls)
	}

	// 3. Generation 2 changes grants, but realize fails transiently: old client MUST ALREADY be disconnected!
	seedLiveGeneration(t, stateDir, 2, "2", "sample")
	failRealize = errors.New("reset in progress")
	if retry := syncer.sync(context.Background()); !retry {
		t.Fatal("expected retry on realize failure")
	}
	select {
	case _, ok := <-c1.Messages():
		if ok {
			t.Fatal("expected c1 disconnected immediately on grant change even when realize failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("c1 was not disconnected on grant change")
	}

	// 4. Retry succeeds: new token written, new grants active, old token refused.
	failRealize = nil
	if retry := syncer.sync(context.Background()); retry {
		t.Fatal("retry gen 2 failed")
	}
	tok2Bytes, err := os.ReadFile(root + "/run/andaru-addons/sample/bus/token")
	if err != nil {
		t.Fatal(err)
	}
	tok2 := string(tok2Bytes)
	if tok2 == tok1 || len(tok2) != 64 {
		t.Fatalf("expected fresh 64-char token on gen 2, got %q", tok2)
	}
	if bad, err := bus.Dial(ctx, addonSock, "addon.sample", tok1); err == nil {
		bad.Close()
		t.Fatal("old token accepted after gen 2")
	}
	c2, err := bus.Dial(ctx, addonSock, "addon.sample", tok2)
	if err != nil {
		t.Fatalf("dial gen 2: %v", err)
	}
	defer c2.Close()
	if err := c2.Subscribe(ctx, "config/head"); err != nil {
		t.Fatalf("subscribe newly granted config/head: %v", err)
	}
	if err := c2.Subscribe(ctx, "apply/+"); !errors.As(err, &refused) {
		t.Fatalf("revoked apply/+ err = %v, want RefusedError", err)
	}

	// 5. Generation 3 disables add-on: c2 disconnected and token file removed.
	seedLiveGeneration(t, stateDir, 3, "3")
	if retry := syncer.sync(context.Background()); retry {
		t.Fatal("sync gen 3 failed")
	}
	select {
	case _, ok := <-c2.Messages():
		if ok {
			t.Fatal("expected c2 disconnected when add-on disabled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("c2 not disconnected on disable")
	}
	if _, err := os.Stat(root + "/run/andaru-addons/sample/bus/token"); !os.IsNotExist(err) {
		t.Fatalf("expected token file removed on disable, err=%v", err)
	}
}

func TestAddonBusSyncRollbackAfterFailedSyncAndPrepareRevert(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	runDir, err := os.MkdirTemp("", "addon-bus-rb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })

	addonSock := filepath.Join(runDir, "addon.sock")
	l, err := net.Listen("unix", addonSock)
	if err != nil {
		t.Fatal(err)
	}
	events, err := bus.Open(bus.Options{
		Path:           filepath.Join(stateDir, "bus.db"),
		TokenListeners: map[string]net.Listener{"addons": l},
	})
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	defer events.Close()

	cat := testBusAddonCatalog(t)
	realizer := generation.Realizer{Root: root}
	grantsByVersion := map[config.Version][]config.AddonBusGrant{
		"1": {{
			ID:        "org.andaru.sample",
			Enabled:   true,
			Subscribe: map[string]string{"apply/+": "granted"},
		}},
		"2": {{
			ID:        "org.andaru.sample",
			Enabled:   true,
			Subscribe: map[string]string{"config/head": "granted"},
		}},
	}
	var failRealize error
	syncer := newAddonBusSync(
		root, stateDir, runDir, cat, events,
		func(_ context.Context, v config.Version) ([]config.AddonBusGrant, error) {
			return grantsByVersion[v], nil
		},
		func(ctx context.Context, tokensFile string) error {
			if failRealize != nil {
				return failRealize
			}
			raw, err := os.ReadFile(tokensFile)
			if err != nil {
				return err
			}
			var tokens map[string]string
			if err := json.Unmarshal(raw, &tokens); err != nil {
				return err
			}
			return realizer.SyncAddonBusTokens(ctx, tokens)
		},
	)

	// Gen 1 synced.
	seedLiveGeneration(t, stateDir, 1, "1", "sample")
	if syncer.sync(context.Background()) {
		t.Fatal("gen 1 sync failed")
	}

	// Gen 2 goes live, sync fails after revoking gen 1's client, then rolls back to gen 1 before retry.
	seedLiveGeneration(t, stateDir, 2, "2", "sample")
	failRealize = errors.New("transient")
	if !syncer.sync(context.Background()) {
		t.Fatal("expected retry on transient failure")
	}
	// Roll pointers back to gen 1 and clear failure: sync MUST re-register gen 1 instead of thinking lastLive==1 is synced!
	one := 1
	rawPtr, _ := json.Marshal(generation.Pointers{Live: &one, Confirmed: &one})
	if err := os.WriteFile(filepath.Join(generation.Dir(stateDir), "pointers.json"), rawPtr, 0o600); err != nil {
		t.Fatal(err)
	}
	failRealize = nil
	if syncer.sync(context.Background()) {
		t.Fatal("rollback to gen 1 sync failed")
	}
	tok, err := os.ReadFile(root + "/run/andaru-addons/sample/bus/token")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := bus.Dial(ctx, addonSock, "addon.sample", string(tok))
	if err != nil {
		t.Fatalf("gen 1 client stayed locked out after failed gen 2 + rollback: %v", err)
	}
	if err := c.Subscribe(ctx, "apply/+"); err != nil {
		t.Fatalf("gen 1 grant missing: %v", err)
	}
	_ = c.Close()

	// beginPrepare(2) failure returns error and immediately reconciles live gen 1 back onto the bus.
	failRealize = errors.New("realize timeout")
	if finishFail, err := syncer.beginPrepare(ctx, 2); err == nil || finishFail != nil {
		t.Fatalf("expected beginPrepare error on realize failure, got finish!=nil=%v err=%v", finishFail != nil, err)
	}
	// Once failure clears, live gen 1 can be synced/re-registered without being wedged in preparing state.
	failRealize = nil
	if syncer.sync(ctx) {
		t.Fatal("sync after failed beginPrepare failed")
	}

	// beginPrepare(2) pre-provisions gen 2 before p.Live moves and suppresses background apply/+ syncs during Activate.
	finishPrepare, err := syncer.beginPrepare(ctx, 2)
	if err != nil {
		t.Fatalf("beginPrepare(2): %v", err)
	}
	tok2, err := os.ReadFile(root + "/run/andaru-addons/sample/bus/token")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := bus.Dial(ctx, addonSock, "addon.sample", string(tok2))
	if err != nil {
		t.Fatalf("prepared gen 2 client dial failed: %v", err)
	}
	// Background sync (e.g., from Activating apply/+ event while p.Live is still 1) MUST be suppressed during prepare.
	if syncer.sync(ctx) {
		t.Fatal("background sync during prepare requested retry")
	}
	if err := c2.Subscribe(ctx, "config/head"); err != nil {
		t.Fatalf("prepared gen 2 client was disrupted by background sync during prepare: %v", err)
	}
	// Simulated failed Activate: p.Live is still 1; finishPrepare reverts to gen 1 and disconnects c2.
	finishPrepare(ctx)
	select {
	case _, ok := <-c2.Messages():
		if ok {
			t.Fatal("expected prepared gen 2 client disconnected on revert")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prepared gen 2 client not disconnected on revert")
	}
}

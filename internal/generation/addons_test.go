package generation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAddonDigest = "ghcr.io/tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testAddonFiles(healthURL string, extraEnv string) (Manifest, map[string][]byte) {
	hdr := ""
	if healthURL != "" {
		hdr = "# andaru-health-url: " + healthURL + " 200\n"
	}
	container := hdr + `[Unit]
Description=Andaru Add-on: Tailscale
After=andaru-network-boot.service systemd-networkd.service andaru-engine.service
ConditionPathExists=!/run/andaru-boot/safe-mode
ConditionPathExists=!/var/lib/andaru/safe-mode-next
ConditionPathExists=!/run/andaru-boot/addons-held
ConditionKernelCommandLine=!andaru.safe=1

[Container]
Image=` + testAddonDigest + `
Pull=never
Network=host
NoNewPrivileges=true
DropCapability=ALL
AddCapability=NET_ADMIN
AddDevice=/dev/net/tun
Volume=/var/lib/andaru/addons/tailscale/state:/var/lib/tailscale:Z
Volume=/run/andaru-addons/tailscale/secrets:/run/andaru/secrets:ro,Z
EnvironmentFile=/run/andaru-addons/tailscale/andaru-addon-tailscale.env

[Service]
Restart=on-failure
RestartSec=5
TimeoutStopSec=5
`
	env := "ANDARU_ADDON_SLUG=tailscale\n" + extraEnv
	secret := "tskey-secret-value"
	files := map[string][]byte{
		"/run/containers/systemd/andaru-addon-tailscale.container":                   []byte(container),
		"/run/andaru-addons/tailscale/andaru-addon-tailscale.env":                    []byte(env),
		"/run/andaru-addons/tailscale/secrets/andaru-addon-tailscale-auth_key.secret": []byte(secret),
	}
	m := Manifest{
		SavedVersion:          "1",
		CreatedAt:             time.Now().UTC(),
		ConfirmTimeoutSeconds: 60,
		Files: []FileEntry{
			{Path: "/run/containers/systemd/andaru-addon-tailscale.container", Mode: "0600"},
			{Path: "/run/andaru-addons/tailscale/andaru-addon-tailscale.env", Mode: "0600"},
			{Path: "/run/andaru-addons/tailscale/secrets/andaru-addon-tailscale-auth_key.secret", Mode: "0400"},
		},
	}
	return m, files
}

func TestAddonActivateHealthFailureRollbackAndSafeMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var mu sync.Mutex
	var cmds []string
	unitState := "active"
	healthCode := 200

	r := Realizer{
		StateDir: dir,
		Root:     root,
		Now:      time.Now,
		VerifyWait: 250 * time.Millisecond,
		Poll:       25 * time.Millisecond,
		LockWait:   time.Second,
		Chown:      func(string, int, int) error { return nil },
		HealthGet: func(_ context.Context, _ string) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			return healthCode, nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			mu.Lock()
			cmds = append(cmds, filepath.Base(name)+" "+strings.Join(args, " "))
			st := unitState
			mu.Unlock()
			if filepath.Base(name) == "systemctl" && len(args) >= 4 && args[0] == "show" && args[2] == "ActiveState" {
				return []byte(st + "\n"), nil
			}
			if filepath.Base(name) == "networkctl" && len(args) >= 1 && args[0] == "list" {
				return []byte(`{"Interfaces":[]}`), nil
			}
			if filepath.Base(name) == "ip" {
				return []byte(`[]`), nil
			}
			return nil, nil
		},
	}

	// 1. Activate generation 1 with healthy add-on.
	m1, f1 := testAddonFiles("http://127.0.0.1:9002/healthz", "TS_HOSTNAME=gw1\n")
	id1, err := Create(Dir(dir), m1, f1)
	if err != nil {
		t.Fatalf("Create gen 1: %v", err)
	}
	if err := r.Activate(t.Context(), id1); err != nil {
		t.Fatalf("Activate gen 1: %v", err)
	}
	// Verify secret file mode is 0400 and storage dir created.
	secStat, err := os.Stat(root + "/run/andaru-addons/tailscale/secrets/andaru-addon-tailscale-auth_key.secret")
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if secStat.Mode().Perm() != 0o400 {
		t.Fatalf("secret mode = %o, want 0400", secStat.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, "var", "lib", "andaru", "addons", "tailscale", "state")); err != nil {
		t.Fatalf("storage dir not created: %v", err)
	}
	if err := r.Confirm(t.Context(), id1); err != nil {
		t.Fatalf("Confirm gen 1: %v", err)
	}

	// ReferencedAddonImages reports gen 1's digest.
	refs, err := ReferencedAddonImages(dir)
	if err != nil {
		t.Fatalf("ReferencedAddonImages: %v", err)
	}
	if !refs[testAddonDigest] {
		t.Fatalf("expected digest %s in refs %+v", testAddonDigest, refs)
	}

	// Manual AddonControl works on applied add-on and rejects unknown slug.
	if err := r.AddonControl(t.Context(), "restart", "tailscale"); err != nil {
		t.Fatalf("AddonControl restart: %v", err)
	}
	if err := r.AddonControl(t.Context(), "restart", "other"); !errors.Is(err, ErrAddonNotApplied) {
		t.Fatalf("AddonControl unknown err = %v, want ErrAddonNotApplied", err)
	}

	// 2. Activate generation 2 with failing health check -> automatic rollback to gen 1, gen 2 marked failed.
	mu.Lock()
	healthCode = 503
	mu.Unlock()
	m2, f2 := testAddonFiles("http://127.0.0.1:9002/healthz", "TS_HOSTNAME=gw2\n")
	m2.SavedVersion = "2"
	id2, err := Create(Dir(dir), m2, f2)
	if err != nil {
		t.Fatalf("Create gen 2: %v", err)
	}
	if err := r.Activate(t.Context(), id2); err == nil {
		t.Fatal("expected Activate gen 2 to fail on 503 health check")
	}
	g2, err := Read(Dir(dir), id2)
	if err != nil {
		t.Fatalf("Read gen 2: %v", err)
	}
	if g2.State.State != Failed {
		t.Fatalf("gen 2 state = %q, want %q", g2.State.State, Failed)
	}
	// Restored env file must be gen 1's TS_HOSTNAME=gw1.
	restoredEnv, err := os.ReadFile(root + "/run/andaru-addons/tailscale/andaru-addon-tailscale.env")
	if err != nil || !strings.Contains(string(restoredEnv), "TS_HOSTNAME=gw1") {
		t.Fatalf("expected restored gen 1 env, got %q (err=%v)", string(restoredEnv), err)
	}

	// 3. Safe mode active: Activate succeeds even with unhealthy add-on (skips start & readiness),
	// and manual AddonControl refuses with ErrSafeModeActive.
	if err := os.MkdirAll(filepath.Dir(root+SafeModeBootFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+SafeModeBootFile, []byte("boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.AddonControl(t.Context(), "restart", "tailscale"); !errors.Is(err, ErrSafeModeActive) {
		t.Fatalf("AddonControl in safe mode err = %v, want ErrSafeModeActive", err)
	}
	mu.Lock()
	cmds = nil
	unitState = "failed"
	mu.Unlock()
	m3, f3 := testAddonFiles("http://127.0.0.1:9002/healthz", "TS_HOSTNAME=gw3\n")
	m3.SavedVersion = "3"
	id3, err := Create(Dir(dir), m3, f3)
	if err != nil {
		t.Fatalf("Create gen 3: %v", err)
	}
	if err := r.Activate(t.Context(), id3); err != nil {
		t.Fatalf("Activate gen 3 during safe mode should succeed without starting add-ons: %v", err)
	}
	mu.Lock()
	startedDuringSafeMode := slices.ContainsFunc(cmds, func(c string) bool {
		return strings.Contains(c, "start --no-block andaru-addon-tailscale.service")
	})
	mu.Unlock()
	if startedDuringSafeMode {
		t.Fatalf("add-on unit must not be started during safe mode; cmds=%v", cmds)
	}
	if err := r.Confirm(t.Context(), id3); err != nil {
		t.Fatalf("Confirm gen 3: %v", err)
	}

	// 4. ReleaseAddonsHold starts live add-ons once boot safe mode is cleared.
	_ = os.Remove(root + SafeModeBootFile)
	if err := os.WriteFile(root+AddonsHeldFile, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	cmds = nil
	mu.Unlock()
	if err := r.ReleaseAddonsHold(t.Context()); err != nil {
		t.Fatalf("ReleaseAddonsHold: %v", err)
	}
	mu.Lock()
	startedOnRelease := slices.ContainsFunc(cmds, func(c string) bool {
		return strings.Contains(c, "start --no-block andaru-addon-tailscale.service")
	})
	mu.Unlock()
	if !startedOnRelease {
		t.Fatalf("ReleaseAddonsHold should start live add-on unit; cmds=%v", cmds)
	}

	// 5. Teardown removes add-on units and /run/andaru-addons.
	if err := r.Teardown(t.Context()); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, err := os.Stat(root + "/run/andaru-addons"); !os.IsNotExist(err) {
		t.Fatalf("expected /run/andaru-addons removed on teardown, err=%v", err)
	}
}

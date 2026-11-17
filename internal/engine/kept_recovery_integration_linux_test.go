//go:build integration

package engine

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
)

func TestStoreRecoveryThroughRunningEngine(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()
	stateDir := filepath.Join(privateTestDir(t), "state")
	store, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	change, err := config.ObjectChange("create", "zones", "", nil, []byte(`{"name":"kept-lan"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, change); err != nil {
		t.Fatal(err)
	}
	peer, err := os.MkdirTemp("/var/tmp", "andaru-kept-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(peer) })
	if err = os.Chmod(peer, 0o700); err != nil {
		t.Fatal(err)
	}
	keptRoot := filepath.Join(peer, "kept")
	recoveryKeptRoot = keptRoot
	t.Cleanup(func() { recoveryKeptRoot = "" })
	handler := newHandler(func(context.Context) ([]host.LinkStatus, error) { return nil, nil },
		newSavedHandler(store, func(context.Context) ([]host.LinkStatus, error) { return nil, nil },
			func() (host.RealizerFacts, error) { return host.RealizerFacts{}, nil }, nil, http.NotFoundHandler(), nil))
	sock := filepath.Join(peer, "engine.sock")
	startTestEngine(t, sock, handler)
	bin := andaruTestBinary(t)
	cmd := exec.Command(bin, "ctl", "--socket", sock, "kept", "store-recovery")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if !strings.Contains(string(out), "stored") {
		t.Fatalf("output %s", out)
	}
	body, err := os.ReadFile(filepath.Join(keptRoot, "recovery", "recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "kept-lan") {
		t.Fatalf("recovery %s", body)
	}
}

func andaruTestBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("ANDARU_TEST_BINARY")
	if filepath.IsAbs(bin) {
		return bin
	}
	bin = filepath.Join(t.TempDir(), "andaru")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/andaru-project/andaru/cmd/andaru")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build andaru: %v %s", err, out)
	}
	return bin
}

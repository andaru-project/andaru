package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/generation"
)

func TestSystemStatusIncludesSafeMode(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	systemRoot = root
	t.Cleanup(func() { systemRoot = "" })
	writeUnder(t, root, "/proc/cpuinfo", "processor\t: 0\nFeatures\t: fp asimd\n")
	writeUnder(t, root, generation.SafeModeBootFile, "boot\n")
	writeUnder(t, root, generation.AddonsHeldFile, "1\n")

	h := newHandlerWithState(nil, http.NotFoundHandler(), nil, nil, nil, nil, nil, nil, nil, nil, stateDir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/status/system"))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	validResponse(t, "SystemResponse", rec.Body.Bytes())
	var body struct {
		SafeMode generation.SafeModeStatus `json:"safeMode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.SafeMode.Active || !slices.Equal(body.SafeMode.Reasons, []string{"boot", "settingsHold"}) {
		t.Fatalf("safeMode = %+v", body.SafeMode)
	}
}

func TestAddonsHoldReleasesWhenDecisionsClear(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	heldPath := root + generation.AddonsHeldFile
	if err := os.MkdirAll(filepath.Dir(heldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(heldPath, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var pending atomic.Bool
	pending.Store(true)
	released := make(chan struct{}, 2)
	h := newAddonsHold(root, stateDir, func(context.Context) (bool, error) {
		return pending.Load(), nil
	}, func(context.Context) error {
		_ = os.Remove(heldPath)
		released <- struct{}{}
		return nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); h.run(ctx) }()

	// Pending upgrade decision keeps hold in place.
	h.nudge()
	select {
	case <-released:
		t.Fatal("released hold while upgrade decisions were pending")
	case <-time.After(80 * time.Millisecond):
	}

	// Clearing decisions and nudging (as OnSave does) releases the hold.
	pending.Store(false)
	h.nudge()
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("hold was not released after upgrade decisions cleared")
	}

	// Subsequent nudges are no-ops once the file is gone.
	h.nudge()
	select {
	case <-released:
		t.Fatal("release ran again after hold file was already removed")
	case <-time.After(80 * time.Millisecond):
	}
	cancel()
	<-done
}

package addon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStagerStageUnstageAndRollbackProtection(t *testing.T) {
	catDir := t.TempDir()
	tsDir := filepath.Join(catDir, "tailscale")
	if err := os.MkdirAll(tsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tsDir, "manifest.json"), []byte(validTailscaleManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	const digest = "ghcr.io/tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	var calls []string
	present := false
	referenced := true
	fixedNow := time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)

	stager := Stager{
		StateDir:   stateDir,
		CatalogDir: catDir,
		Now:        func() time.Time { return fixedNow },
		ReferencedImages: func() (map[string]bool, error) {
			if referenced {
				return map[string]bool{digest: true}, nil
			}
			return map[string]bool{}, nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			switch {
			case len(args) == 2 && args[0] == "pull" && args[1] == digest:
				present = true
				return []byte("pulled\n"), nil
			case len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[4] == digest:
				if !present {
					return nil, errors.New("missing")
				}
				return []byte("sha256:deadbeef\n"), nil
			case len(args) == 2 && args[0] == "rmi" && args[1] == digest:
				present = false
				return []byte("untagged\n"), nil
			case len(args) == 3 && args[0] == "image" && args[1] == "exists" && args[2] == digest:
				if present {
					return nil, nil
				}
				return nil, errors.New("not found")
			default:
				return nil, errors.New("unexpected command: " + strings.Join(args, " "))
			}
		},
	}

	// Unknown slug rejected.
	if _, err := stager.Stage(t.Context(), "missing"); !errors.Is(err, ErrAddonNotFound) {
		t.Fatalf("Stage(missing) err = %v, want ErrAddonNotFound", err)
	}
	// Invalid slug rejected.
	if _, err := stager.Stage(t.Context(), "../evil"); err == nil {
		t.Fatal("expected invalid slug rejection")
	}

	got, err := stager.Stage(t.Context(), "tailscale")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if got.Digest != digest || got.ImageID != "sha256:deadbeef" || !got.StagedAt.Equal(fixedNow) {
		t.Fatalf("Stage = %+v", got)
	}

	st, err := ReadStagedState(stateDir)
	if err != nil {
		t.Fatalf("ReadStagedState: %v", err)
	}
	if !slices.Equal(st.Digests(), []string{digest}) {
		t.Fatalf("Digests = %v", st.Digests())
	}

	// Refused while referenced by a retained generation.
	if err := stager.Unstage(t.Context(), "tailscale"); !errors.Is(err, ErrImageReferenced) {
		t.Fatalf("Unstage while referenced err = %v, want ErrImageReferenced", err)
	}

	// Allowed once unreferenced.
	referenced = false
	if err := stager.Unstage(t.Context(), "tailscale"); err != nil {
		t.Fatalf("Unstage: %v", err)
	}
	st, err = ReadStagedState(stateDir)
	if err != nil {
		t.Fatalf("ReadStagedState after unstage: %v", err)
	}
	if len(st.Digests()) != 0 {
		t.Fatalf("expected empty staged list, got %v", st.Digests())
	}
}

func TestRemoveAllManagedImages(t *testing.T) {
	stateDir := t.TempDir()
	const d1 = "ghcr.io/tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := writeStagedState(stateDir, StagedState{Images: map[string]StagedImage{
		d1: {Digest: d1, ImageID: "sha256:abc"},
	}}); err != nil {
		t.Fatal(err)
	}
	var removed []string
	err := RemoveAllManagedImages(t.Context(), stateDir, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 3 && args[0] == "rmi" && args[1] == "-f" {
			removed = append(removed, args[2])
			return nil, nil
		}
		return nil, errors.New("unexpected")
	})
	if err != nil {
		t.Fatalf("RemoveAllManagedImages: %v", err)
	}
	if !slices.Equal(removed, []string{d1}) {
		t.Fatalf("removed = %v", removed)
	}
	if _, err := os.Stat(filepath.Join(stateDir, StagedStateFile)); !os.IsNotExist(err) {
		t.Fatalf("expected staged file removed, stat err = %v", err)
	}

	// Partial removal failure must keep failed digest recorded for retry.
	const d2 = "ghcr.io/tailscale/tailscale@sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err := writeStagedState(stateDir, StagedState{Images: map[string]StagedImage{
		d1: {Digest: d1, ImageID: "sha256:1"},
		d2: {Digest: d2, ImageID: "sha256:2"},
	}}); err != nil {
		t.Fatal(err)
	}
	err = RemoveAllManagedImages(t.Context(), stateDir, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 3 && args[0] == "rmi" && args[1] == "-f" {
			if args[2] == d2 {
				return nil, errors.New("busy")
			}
			return nil, nil
		}
		if len(args) == 3 && args[0] == "image" && args[1] == "exists" && args[2] == d2 {
			return nil, nil
		}
		return nil, errors.New("not found")
	})
	if err == nil {
		t.Fatal("expected partial removal error")
	}
	st, rerr := ReadStagedState(stateDir)
	if rerr != nil {
		t.Fatalf("ReadStagedState after partial failure: %v", rerr)
	}
	if !slices.Equal(st.Digests(), []string{d2}) {
		t.Fatalf("expected failed digest %s retained for retry, got %v", d2, st.Digests())
	}
}

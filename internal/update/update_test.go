package update

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const sampleAvailableJSON = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "Host",
  "status": {
    "booted": {
      "image": {
        "image": {
          "image": "ghcr.io/andaru-project/andaru:stable",
          "transport": "registry",
          "signature": "containerPolicy"
        },
        "version": "0.5.0-a.4",
        "timestamp": "2026-11-23T10:00:00Z",
        "imageDigest": "sha256:1111"
      },
      "cachedUpdate": {
        "image": {
          "image": "ghcr.io/andaru-project/andaru:stable",
          "transport": "registry",
          "signature": "containerPolicy"
        },
        "version": "0.5.0-a.5",
        "timestamp": "2026-11-24T10:00:00Z",
        "imageDigest": "sha256:2222"
      }
    },
    "staged": null,
    "rollback": {
      "image": {
        "image": {
          "image": "ghcr.io/andaru-project/andaru:0.5.0-a.3",
          "transport": "registry",
          "signature": "containerPolicy"
        },
        "version": "0.5.0-a.3",
        "imageDigest": "sha256:0000"
      }
    }
  }
}`

const sampleStagedJSON = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "Host",
  "status": {
    "booted": {
      "image": {
        "image": {
          "image": "ghcr.io/andaru-project/andaru:stable",
          "transport": "registry",
          "signature": "containerPolicy"
        },
        "version": "0.5.0-a.4",
        "imageDigest": "sha256:1111"
      }
    },
    "staged": {
      "image": {
        "image": {
          "image": "ghcr.io/andaru-project/andaru:stable",
          "transport": "registry",
          "signature": "containerPolicy"
        },
        "version": "0.5.0-a.5",
        "imageDigest": "sha256:2222"
      }
    }
  }
}`

func TestParseBootcStatus(t *testing.T) {
	st, err := ParseBootcStatus([]byte(sampleAvailableJSON))
	if err != nil {
		t.Fatalf("ParseBootcStatus: %v", err)
	}
	if !st.Supported || st.State != StateAvailable || st.RebootNeeded {
		t.Fatalf("unexpected state: %+v", st)
	}
	if st.Booted == nil || st.Booted.Version != "0.5.0-a.4" || st.Booted.Signature != "containerPolicy" {
		t.Fatalf("unexpected booted: %+v", st.Booted)
	}
	if st.Available == nil || st.Available.Version != "0.5.0-a.5" || st.Available.Digest != "sha256:2222" {
		t.Fatalf("unexpected available: %+v", st.Available)
	}
	if st.Rollback == nil || st.Rollback.Version != "0.5.0-a.3" {
		t.Fatalf("unexpected rollback: %+v", st.Rollback)
	}

	staged, err := ParseBootcStatus([]byte(sampleStagedJSON))
	if err != nil {
		t.Fatalf("ParseBootcStatus staged: %v", err)
	}
	if staged.State != StateStaged || !staged.RebootNeeded || staged.Staged == nil || staged.Staged.Version != "0.5.0-a.5" {
		t.Fatalf("unexpected staged status: %+v", staged)
	}

	// Same digest cachedUpdate collapses to up_to_date.
	sameDigest := strings.ReplaceAll(sampleAvailableJSON, "sha256:2222", "sha256:1111")
	upToDate, err := ParseBootcStatus([]byte(sameDigest))
	if err != nil {
		t.Fatalf("ParseBootcStatus up_to_date: %v", err)
	}
	if upToDate.State != StateUpToDate || upToDate.Available != nil {
		t.Fatalf("expected up_to_date without available, got %+v", upToDate)
	}

	// Non-bootc host reports unsupported.
	unsup, err := ParseBootcStatus([]byte(`{"status":{"booted":null}}`))
	if err != nil {
		t.Fatalf("ParseBootcStatus unsupported: %v", err)
	}
	if unsup.Supported || unsup.State != StateUnsupported {
		t.Fatalf("expected unsupported, got %+v", unsup)
	}
}

func TestManagerCheckStageRollbackAndIdleFence(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	var calls []string
	statusJSON := sampleAvailableJSON

	mgr := Manager{
		StateDir: dir,
		Now:      func() time.Time { return now },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if len(args) >= 1 && args[0] == "status" {
				return []byte(statusJSON), nil
			}
			if len(args) >= 1 && args[0] == "upgrade" && len(args) == 2 && args[1] == "--quiet" {
				statusJSON = sampleStagedJSON
			}
			return nil, nil
		},
	}

	st, err := mgr.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st.State != StateAvailable || st.CheckedAt == nil || !st.CheckedAt.Equal(now) {
		t.Fatalf("unexpected check status: %+v", st)
	}
	cached, exists, err := ReadStatus(dir)
	if err != nil || !exists || cached.State != StateAvailable {
		t.Fatalf("cached status mismatch: exists=%v err=%v st=%+v", exists, err, cached)
	}

	// Busy idle check blocks Stage, Rollback, and Reboot before calling bootc/systemctl.
	busy := mgr
	busy.CheckIdle = func(context.Context) error { return errors.New("apply pending") }
	if _, err := busy.Stage(context.Background()); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("expected busy error on Stage, got %v", err)
	}
	if _, err := busy.Rollback(context.Background()); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("expected busy error on Rollback, got %v", err)
	}
	if err := busy.Reboot(context.Background()); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("expected busy error on Reboot, got %v", err)
	}

	// Idle Stage succeeds and persists staged state.
	staged, err := mgr.Stage(context.Background())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if staged.State != StateStaged || !staged.RebootNeeded {
		t.Fatalf("expected staged with rebootNeeded, got %+v", staged)
	}

	// Missing bootc binary records unsupported without error.
	noBootc := Manager{
		StateDir: t.TempDir(),
		Now:      func() time.Time { return now },
		Run:      func(context.Context, string, ...string) ([]byte, error) { return nil, exec.ErrNotFound },
	}
	unsup, err := noBootc.Check(context.Background())
	if err != nil || unsup.Supported || unsup.State != StateUnsupported {
		t.Fatalf("expected unsupported without error, got err=%v st=%+v", err, unsup)
	}
}

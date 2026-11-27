package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseCheckEstimate(t *testing.T) {
	sample := `Update available for: docker://ghcr.io/andaru-project/andaru:stable
  Version: 0.5.0-a.5
  Digest: sha256:2222
Total new layers: 320   Size: 1.1 GB
Removed layers:   12    Size: 44.3 MB
Added layers:     14    Size: 48.2 MB
`
	est := ParseCheckEstimate([]byte(sample))
	if est == nil {
		t.Fatal("expected non-nil DownloadEstimate")
	}
	if est.TotalLayers != 320 || est.TotalBytes != 1_100_000_000 {
		t.Fatalf("unexpected total: %+v", est)
	}
	if est.RemovedLayers != 12 || est.RemovedBytes != 44_300_000 {
		t.Fatalf("unexpected removed: %+v", est)
	}
	if est.DownloadLayers != 14 || est.DownloadBytes != 48_200_000 {
		t.Fatalf("unexpected download: %+v", est)
	}

	// Zero added layers (metadata-only change) and non-breaking spaces / binary units.
	metaOnly := "Total new layers: 68  Size: 512\u00a0MiB\nAdded layers: 0  Size: 0 bytes\n"
	est = ParseCheckEstimate([]byte(metaOnly))
	if est == nil || est.DownloadLayers != 0 || est.DownloadBytes != 0 || est.TotalLayers != 68 || est.TotalBytes != 512*1024*1024 {
		t.Fatalf("unexpected metadata-only estimate: %+v", est)
	}

	// No changes output returns nil.
	if est := ParseCheckEstimate([]byte("No changes in: docker://ghcr.io/andaru-project/andaru:stable\n")); est != nil {
		t.Fatalf("expected nil for no-changes output, got %+v", est)
	}
}

func TestApplyProgressLine(t *testing.T) {
	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(2 * time.Second)
	t2 := t0.Add(5 * time.Second)

	cur := Progress{Phase: "starting", StartedAt: t0, UpdatedAt: t0}

	// Start handshake and malformed JSON are ignored.
	if next, ok := ApplyProgressLine(cur, []byte(`{"type":"Start","version":"0.1.0"}`), t1); ok || next != cur {
		t.Fatalf("Start event should be ignored, got ok=%v next=%+v", ok, next)
	}
	if next, ok := ApplyProgressLine(cur, []byte(`not-json`), t1); ok || next != cur {
		t.Fatalf("malformed line should be ignored, got ok=%v next=%+v", ok, next)
	}

	// ProgressBytes populates byte + layer metrics.
	pullLine := `{"type":"ProgressBytes","task":"pulling","description":"Pulling Image: sha256:2222","id":"sha256:2222","bytesCached":1000000000,"bytes":24000000,"bytesTotal":48000000,"stepsCached":306,"steps":7,"stepsTotal":14,"subtasks":[]}`
	cur, ok := ApplyProgressLine(cur, []byte(pullLine), t1)
	if !ok {
		t.Fatal("expected ProgressBytes to apply")
	}
	if cur.Phase != "pulling" || cur.Bytes != 24_000_000 || cur.BytesTotal != 48_000_000 || cur.BytesCached != 1_000_000_000 || cur.Steps != 7 || cur.StepsTotal != 14 || cur.StepsCached != 306 || !cur.UpdatedAt.Equal(t1) {
		t.Fatalf("unexpected ProgressBytes state: %+v", cur)
	}

	// ProgressSteps transitions to importing and resets byte counters.
	importLine := `{"type":"ProgressSteps","task":"importing","description":"Importing Image","id":"sha256:2222","stepsCached":0,"steps":0,"stepsTotal":1,"subtasks":[]}`
	cur, ok = ApplyProgressLine(cur, []byte(importLine), t2)
	if !ok {
		t.Fatal("expected ProgressSteps to apply")
	}
	if cur.Phase != "importing" || cur.Bytes != 0 || cur.BytesTotal != 0 || cur.StepsTotal != 1 || !cur.UpdatedAt.Equal(t2) {
		t.Fatalf("unexpected ProgressSteps state: %+v", cur)
	}
}

func TestDefaultProgressRunnerReadsFD3IncludingLargeLines(t *testing.T) {
	// Emit two JSON lines to fd 3, including one > 100 KiB to exercise the enlarged scanner buffer.
	bigPad := strings.Repeat("a", 120*1024)
	script := `printf '{"type":"Start","version":"0.1.0"}\n' >&3; ` +
		`printf '{"type":"ProgressBytes","task":"pulling","description":"%s","id":"x","bytesCached":0,"bytes":100,"bytesTotal":200,"stepsCached":0,"steps":1,"stepsTotal":2,"subtasks":[]}\n' "$BIG_PAD" >&3`

	t.Setenv("BIG_PAD", bigPad)
	var lines []string
	_, err := DefaultProgressRunner(context.Background(), func(line []byte) {
		lines = append(lines, string(line))
	}, "/bin/sh", "-c", script)
	if err != nil {
		t.Fatalf("DefaultProgressRunner: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines from fd 3, got %d", len(lines))
	}
	var ev bootcProgressEvent
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil || ev.Bytes != 100 || len(ev.Description) != len(bigPad) {
		t.Fatalf("large line decode failed: err=%v bytes=%d descLen=%d", err, ev.Bytes, len(ev.Description))
	}
}

func TestManagerCheckEstimateAndStageProgress(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	statusJSON := sampleAvailableJSON

	var observed []Progress
	var sawProgressMode os.FileMode

	mgr := Manager{
		StateDir: dir,
		Now:      func() time.Time { return now },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) >= 1 && args[0] == "status" {
				return []byte(statusJSON), nil
			}
			if len(args) >= 2 && args[0] == "upgrade" && args[1] == "--check" {
				return []byte("Update available\nTotal new layers: 320  Size: 1.0 GB\nAdded layers: 14  Size: 48.2 MB\n"), nil
			}
			return nil, nil
		},
		RunWithProgress: func(_ context.Context, onLine func([]byte), _ string, args ...string) ([]byte, error) {
			if len(args) < 4 || args[2] != "--progress-fd" || args[3] != "3" {
				t.Fatalf("expected --progress-fd 3 args, got %v", args)
			}
			// Initial starting progress must already exist on disk with mode 0600.
			if info, err := os.Stat(ProgressPath(dir)); err == nil {
				sawProgressMode = info.Mode().Perm()
			}
			if p, err := ReadProgress(dir); err == nil && p != nil {
				observed = append(observed, *p)
			}

			// Emit pulling progress, then importing progress.
			now = now.Add(time.Second)
			onLine([]byte(`{"type":"ProgressBytes","task":"pulling","description":"Pulling Image","id":"sha256:2222","bytesCached":900000000,"bytes":24100000,"bytesTotal":48200000,"stepsCached":306,"steps":7,"stepsTotal":14,"subtasks":[]}`))
			if p, err := ReadProgress(dir); err == nil && p != nil {
				observed = append(observed, *p)
			}

			// Rapid mid-layer update within throttle window should NOT force a write yet.
			now = now.Add(100 * time.Millisecond)
			onLine([]byte(`{"type":"ProgressBytes","task":"pulling","description":"Pulling Image","id":"sha256:2222","bytesCached":900000000,"bytes":25000000,"bytesTotal":48200000,"stepsCached":306,"steps":7,"stepsTotal":14,"subtasks":[]}`))
			if p, err := ReadProgress(dir); err != nil || p == nil || p.Bytes != 24_100_000 {
				t.Fatalf("expected throttled write to keep 24100000, got %+v err=%v", p, err)
			}

			// Phase change to importing flushes immediately even inside the 500ms window.
			now = now.Add(50 * time.Millisecond)
			onLine([]byte(`{"type":"ProgressSteps","task":"importing","description":"Importing Image","id":"sha256:2222","stepsCached":0,"steps":0,"stepsTotal":1,"subtasks":[]}`))
			if p, err := ReadProgress(dir); err == nil && p != nil {
				observed = append(observed, *p)
			}

			statusJSON = sampleStagedJSON
			return nil, nil
		},
	}

	checked, err := mgr.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if checked.Download == nil || checked.Download.DownloadLayers != 14 || checked.Download.DownloadBytes != 48_200_000 || checked.Download.TotalLayers != 320 {
		t.Fatalf("expected parsed download estimate on Check, got %+v", checked.Download)
	}

	staged, err := mgr.Stage(context.Background())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if sawProgressMode != 0o600 {
		t.Fatalf("progress.json mode=%v, want 0600", sawProgressMode)
	}
	if len(observed) != 3 || observed[0].Phase != "starting" || observed[1].Phase != "pulling" || observed[1].Bytes != 24_100_000 || observed[2].Phase != "importing" {
		t.Fatalf("unexpected progress snapshots: %+v", observed)
	}
	if staged.State != StateStaged || staged.Download != nil {
		t.Fatalf("expected staged with cleared download estimate, got %+v", staged)
	}
	// progress.json is removed once Stage completes.
	if p, err := ReadProgress(dir); err != nil || p != nil {
		t.Fatalf("expected progress.json removed after Stage, got p=%+v err=%v", p, err)
	}

	// Failed Stage also cleans up progress.json.
	failing := mgr
	failing.RunWithProgress = func(context.Context, func([]byte), string, ...string) ([]byte, error) {
		return nil, errors.New("registry timeout")
	}
	if _, err := failing.Stage(context.Background()); err == nil {
		t.Fatal("expected Stage failure")
	}
	if p, err := ReadProgress(dir); err != nil || p != nil {
		t.Fatalf("expected progress.json removed after failed Stage, got p=%+v err=%v", p, err)
	}
}

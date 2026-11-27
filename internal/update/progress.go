package update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	progressWriteInterval = 500 * time.Millisecond
	maxProgressLineBytes  = 4 * 1024 * 1024
)

// DownloadEstimate captures the layer and byte delta reported by `bootc upgrade --check`
// before the user initiates a download.
type DownloadEstimate struct {
	DownloadBytes  uint64 `json:"downloadBytes"`
	DownloadLayers int    `json:"downloadLayers"`
	TotalBytes     uint64 `json:"totalBytes,omitempty"`
	TotalLayers    int    `json:"totalLayers,omitempty"`
	RemovedBytes   uint64 `json:"removedBytes,omitempty"`
	RemovedLayers  int    `json:"removedLayers,omitempty"`
}

// Progress captures live download, import, and staging progress streamed from `bootc upgrade --progress-fd`.
type Progress struct {
	Phase       string    `json:"phase"`
	Description string    `json:"description,omitempty"`
	Bytes       uint64    `json:"bytes,omitempty"`
	BytesTotal  uint64    `json:"bytesTotal,omitempty"`
	BytesCached uint64    `json:"bytesCached,omitempty"`
	Steps       uint64    `json:"steps,omitempty"`
	StepsTotal  uint64    `json:"stepsTotal,omitempty"`
	StepsCached uint64    `json:"stepsCached,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

// ProgressRunner runs a host command that writes JSON-lines progress events to fd 3 (--progress-fd 3).
type ProgressRunner func(ctx context.Context, onLine func([]byte), name string, args ...string) ([]byte, error)

// ProgressPath returns the live staging progress file path inside stateDir.
func ProgressPath(stateDir string) string {
	return filepath.Join(stateDir, "update", "progress.json")
}

// ReadProgress loads the current staging progress snapshot from stateDir.
// Returns (nil, nil) when no progress file exists.
func ReadProgress(stateDir string) (*Progress, error) {
	raw, err := os.ReadFile(ProgressPath(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read update progress: %w", err)
	}
	var p Progress
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse update progress: %w", err)
	}
	return &p, nil
}

// WriteProgress persists a progress snapshot atomically inside stateDir/update/progress.json (mode 0600).
func WriteProgress(stateDir string, p Progress) error {
	return writeAtomicJSON(stateDir, ProgressPath(stateDir), "progress-*.tmp", p)
}

// ClearProgress removes any active progress snapshot from stateDir.
func ClearProgress(stateDir string) {
	_ = os.Remove(ProgressPath(stateDir))
}

// DefaultProgressRunner executes name with args, wiring a pipe onto child fd 3 so bootc's
// `--progress-fd 3` JSON-lines stream is delivered to onLine as events arrive.
func DefaultProgressRunner(ctx context.Context, onLine func([]byte), name string, args ...string) ([]byte, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create progress pipe: %w", err)
	}

	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.ExtraFiles = []*os.File{pw}

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	_ = pw.Close()

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		defer func() { _ = pr.Close() }()
		scanner := bufio.NewScanner(pr)
		// bootc's subtasks array grows with each completed layer; 300+ layers can exceed 64 KiB.
		scanner.Buffer(make([]byte, 64*1024), maxProgressLineBytes)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) > 0 && onLine != nil {
				onLine(line)
			}
		}
	}()

	waitErr := cmd.Wait()
	<-scanDone

	if waitErr != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), waitErr, strings.TrimSpace(out.String()))
	}
	return out.Bytes(), nil
}

// bootcProgressEvent matches the progress-v0 JSON-lines schema emitted by `bootc --progress-fd`.
type bootcProgressEvent struct {
	Type        string `json:"type"`
	Task        string `json:"task"`
	Description string `json:"description"`
	Bytes       uint64 `json:"bytes"`
	BytesCached uint64 `json:"bytesCached"`
	BytesTotal  uint64 `json:"bytesTotal"`
	Steps       uint64 `json:"steps"`
	StepsCached uint64 `json:"stepsCached"`
	StepsTotal  uint64 `json:"stepsTotal"`
}

// ApplyProgressLine updates cur from one JSON line emitted by `bootc upgrade --progress-fd`.
// Returns (updated, true) when cur changed from a recognized progress event.
func ApplyProgressLine(cur Progress, line []byte, now time.Time) (Progress, bool) {
	var ev bootcProgressEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return cur, false
	}
	switch ev.Type {
	case "ProgressBytes":
		phase := strings.TrimSpace(ev.Task)
		if phase == "" {
			phase = "pulling"
		}
		cur.Phase = phase
		cur.Description = strings.TrimSpace(ev.Description)
		cur.Bytes = ev.Bytes
		cur.BytesTotal = ev.BytesTotal
		cur.BytesCached = ev.BytesCached
		cur.Steps = ev.Steps
		cur.StepsTotal = ev.StepsTotal
		cur.StepsCached = ev.StepsCached
		cur.UpdatedAt = now
		return cur, true
	case "ProgressSteps":
		phase := strings.TrimSpace(ev.Task)
		if phase == "" {
			phase = "staging"
		}
		cur.Phase = phase
		cur.Description = strings.TrimSpace(ev.Description)
		cur.Bytes = 0
		cur.BytesTotal = 0
		cur.BytesCached = 0
		cur.Steps = ev.Steps
		cur.StepsTotal = ev.StepsTotal
		cur.StepsCached = ev.StepsCached
		cur.UpdatedAt = now
		return cur, true
	default:
		return cur, false
	}
}

// ParseCheckEstimate extracts layer counts and download sizes from `bootc upgrade --check` output
// (formatted by ostree_container::ManifestDiff::print).
func ParseCheckEstimate(output []byte) *DownloadEstimate {
	var est DownloadEstimate
	var seenTotal, seenAdded bool

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case hasPrefixFold(line, "Total new layers:"):
			if count, size, ok := parseLayerSizeLine(line[len("Total new layers:"):]); ok {
				est.TotalLayers = count
				est.TotalBytes = size
				seenTotal = true
			}
		case hasPrefixFold(line, "Removed layers:"):
			if count, size, ok := parseLayerSizeLine(line[len("Removed layers:"):]); ok {
				est.RemovedLayers = count
				est.RemovedBytes = size
			}
		case hasPrefixFold(line, "Added layers:"):
			if count, size, ok := parseLayerSizeLine(line[len("Added layers:"):]); ok {
				est.DownloadLayers = count
				est.DownloadBytes = size
				seenAdded = true
			}
		}
	}

	if !seenAdded && !seenTotal {
		return nil
	}
	return &est
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func parseLayerSizeLine(rest string) (int, uint64, bool) {
	idx := strings.Index(strings.ToLower(rest), "size:")
	if idx < 0 {
		return 0, 0, false
	}
	countStr := strings.TrimSpace(rest[:idx])
	sizeStr := strings.TrimSpace(rest[idx+len("size:"):])

	count, err := strconv.Atoi(countStr)
	if err != nil || count < 0 {
		return 0, 0, false
	}
	size, ok := parseHumanSize(sizeStr)
	if !ok {
		return 0, 0, false
	}
	return count, size, true
}

func parseHumanSize(s string) (uint64, bool) {
	// GLib's g_format_size may use a regular space or non-breaking space between number and unit.
	cleaned := strings.NewReplacer("\u00a0", " ", "\u202f", " ").Replace(strings.TrimSpace(s))
	fields := strings.Fields(cleaned)
	if len(fields) != 2 {
		return 0, false
	}
	numStr := fields[0]
	if strings.Count(numStr, ",") == 1 && !strings.Contains(numStr, ".") {
		numStr = strings.Replace(numStr, ",", ".", 1)
	}
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil || math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
		return 0, false
	}

	var mult float64
	switch strings.ToLower(fields[1]) {
	case "b", "byte", "bytes":
		mult = 1
	case "kb":
		mult = 1e3
	case "kib":
		mult = 1024
	case "mb":
		mult = 1e6
	case "mib":
		mult = 1024 * 1024
	case "gb":
		mult = 1e9
	case "gib":
		mult = 1024 * 1024 * 1024
	case "tb":
		mult = 1e12
	case "tib":
		mult = 1024 * 1024 * 1024 * 1024
	case "pb":
		mult = 1e15
	case "pib":
		mult = 1024 * 1024 * 1024 * 1024 * 1024
	default:
		return 0, false
	}

	scaled := math.Round(val * mult)
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled < 0 || scaled > float64(math.MaxUint64) {
		return 0, false
	}
	return uint64(scaled), true
}

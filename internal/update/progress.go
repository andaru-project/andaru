package update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	progressWaitDelay     = 5 * time.Second
	progressDrainGrace    = time.Second
)

// DownloadEstimate captures the layer and byte delta reported by `bootc upgrade --check`
// before the user initiates a download.
type DownloadEstimate struct {
	DownloadBytes  uint64 `json:"downloadBytes"`
	DownloadLayers int    `json:"downloadLayers"`
	TotalBytes     uint64 `json:"totalBytes,omitempty"`
	TotalLayers    int    `json:"totalLayers,omitempty"`
}

// Progress captures live download, import, and staging progress streamed from `bootc upgrade --progress-fd`.
type Progress struct {
	Phase       string    `json:"phase"`
	Description string    `json:"description,omitempty"`
	Bytes       uint64    `json:"bytes,omitempty"`
	BytesTotal  uint64    `json:"bytesTotal,omitempty"`
	Steps       uint64    `json:"steps,omitempty"`
	StepsTotal  uint64    `json:"stepsTotal,omitempty"`
	StepsCached uint64    `json:"stepsCached,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	UpdatedAt   time.Time `json:"updatedAt,omitzero"`
}

// StartingProgress returns the initial progress snapshot recorded before bootc emits its first event.
func StartingProgress(at time.Time) Progress {
	at = at.UTC()
	return Progress{
		Phase:     "starting",
		StartedAt: at,
		UpdatedAt: at,
	}
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
	return writeAtomicJSON(ProgressPath(stateDir), p)
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
	cmd.WaitDelay = progressWaitDelay

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
		readProgressLines(pr, maxProgressLineBytes, onLine)
	}()

	waitErr := cmd.Wait()
	select {
	case <-scanDone:
	case <-time.After(progressDrainGrace):
		// Unblock the reader if an orphaned grandchild inherited fd 3.
		_ = pr.Close()
		<-scanDone
	}

	if waitErr != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), waitErr, strings.TrimSpace(out.String()))
	}
	return out.Bytes(), nil
}

// readProgressLines reads newline-delimited JSON records without ever closing r early on an oversized line,
// discarding any single line exceeding maxBytes while continuing to process subsequent lines.
func readProgressLines(r io.Reader, maxBytes int, onLine func([]byte)) {
	br := bufio.NewReaderSize(r, 64*1024)
	var buf []byte
	discard := false
	for {
		frag, err := br.ReadSlice('\n')
		if !discard {
			if len(buf)+len(frag) > maxBytes {
				buf = buf[:0]
				discard = true
			} else {
				buf = append(buf, frag...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !discard {
			line := bytes.TrimSpace(buf)
			if len(line) > 0 && onLine != nil {
				onLine(line)
			}
		}
		buf = buf[:0]
		discard = false
		if err != nil {
			return
		}
	}
}

// isUnsupportedProgressFD reports whether child output indicates the installed bootc binary rejects --progress-fd.
func isUnsupportedProgressFD(out []byte) bool {
	lower := strings.ToLower(string(out))
	if !strings.Contains(lower, "--progress-fd") {
		return false
	}
	for _, marker := range []string{"unexpected argument", "unrecognized", "unknown flag", "unknown option", "invalid option"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// bootcProgressEvent matches the progress-v0 JSON-lines schema emitted by `bootc --progress-fd`.
type bootcProgressEvent struct {
	Type        string `json:"type"`
	Task        string `json:"task"`
	Description string `json:"description"`
	Bytes       uint64 `json:"bytes"`
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
	var defaultPhase string
	switch ev.Type {
	case "ProgressBytes":
		defaultPhase = "pulling"
	case "ProgressSteps":
		defaultPhase = "staging"
	default:
		return cur, false
	}

	phase := strings.TrimSpace(ev.Task)
	if phase == "" {
		phase = defaultPhase
	}
	next := Progress{
		Phase:       phase,
		Description: strings.TrimSpace(ev.Description),
		Steps:       ev.Steps,
		StepsTotal:  ev.StepsTotal,
		StepsCached: ev.StepsCached,
		StartedAt:   cur.StartedAt,
		UpdatedAt:   now,
	}
	if ev.Type == "ProgressBytes" {
		next.Bytes = ev.Bytes
		next.BytesTotal = ev.BytesTotal
	}
	return next, true
}

// ParseCheckEstimate extracts layer counts and download sizes from `bootc upgrade --check` output
// (formatted by ostree_container::ManifestDiff::print).
func ParseCheckEstimate(output []byte) *DownloadEstimate {
	var est DownloadEstimate
	var seenTotal, seenAdded bool

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := cutPrefixFold(line, "Total new layers:"); ok {
			count, size, valid := parseLayerSizeLine(rest)
			if !valid {
				continue
			}
			est.TotalLayers = count
			est.TotalBytes = size
			seenTotal = true
			continue
		}
		if rest, ok := cutPrefixFold(line, "Added layers:"); ok {
			count, size, valid := parseLayerSizeLine(rest)
			if !valid {
				// Never report a fabricated 0 B download estimate when the Added layers line is malformed.
				return nil
			}
			est.DownloadLayers = count
			est.DownloadBytes = size
			seenAdded = true
		}
	}

	if !seenAdded && !seenTotal {
		return nil
	}
	return &est
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

func parseLayerSizeLine(rest string) (int, uint64, bool) {
	countStr, sizeStr, ok := strings.Cut(strings.ToLower(rest), "size:")
	if !ok {
		return 0, 0, false
	}
	count, err := strconv.Atoi(strings.TrimSpace(countStr))
	if err != nil || count < 0 {
		return 0, 0, false
	}
	size, ok := parseHumanSize(sizeStr)
	if !ok {
		return 0, 0, false
	}
	return count, size, true
}

var sizeUnits = map[string]float64{
	"b":     1,
	"byte":  1,
	"bytes": 1,
	"kb":    1e3,
	"kib":   1 << 10,
	"mb":    1e6,
	"mib":   1 << 20,
	"gb":    1e9,
	"gib":   1 << 30,
	"tb":    1e12,
	"tib":   1 << 40,
	"pb":    1e15,
	"pib":   1 << 50,
}

func parseHumanSize(s string) (uint64, bool) {
	// strings.Fields splits on all unicode.IsSpace runes, including GLib's U+00A0 / U+202F.
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0, false
	}
	val, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
		return 0, false
	}
	mult, ok := sizeUnits[strings.ToLower(fields[1])]
	if !ok {
		return 0, false
	}
	scaled := math.Round(val * mult)
	// float64(math.MaxUint64) rounds up to 2^64, so reject >= 2^64 to avoid uint64 overflow.
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled < 0 || scaled >= math.Exp2(64) {
		return 0, false
	}
	return uint64(scaled), true
}

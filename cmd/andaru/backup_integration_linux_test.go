package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/andaru-project/andaru/internal/backup"
)

const backupTestPassphrase = "literal-test-passphrase"

func backupFixtureArchive(t *testing.T, passphrase string) []byte {
	t.Helper()
	live := 2
	raw, err := backup.Encode(context.Background(), backup.Source{
		AndaruVersion: "0.4.0-test", SavedVersion: "7", CapturedAt: time.Date(2026, 11, 15, 12, 0, 0, 123, time.UTC),
		Network: backup.NetworkObservation{Live: &live, LiveSavedVersion: "6"},
	}, backup.Documents{
		Configuration: json.RawMessage(`{"schemaVersion":1,"links":[],"segments":[],"zones":[]}`),
		ConfigurationSecrets: func() json.RawMessage {
			if passphrase == "" {
				return nil
			}
			return json.RawMessage(`{"schemaVersion":1,"secrets":{"core":{"TEST_KEY":{"kind":"password","value":"PLAINTEXT_SENTINEL"}}}}`)
		}(),
	}, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type strictBackupFixture struct {
	t                *testing.T
	mu               sync.Mutex
	preview          []byte
	commits          atomic.Int32
	dropCommit       atomic.Bool
	previewProcessed chan struct{}
	previewOnce      sync.Once
}

func (f *strictBackupFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/api/v1/backups":
		if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
			f.t.Errorf("invalid create request: %s %s %q", r.Method, r.URL, r.Header.Get("Content-Type"))
			http.Error(w, "bad request", 400)
			return
		}
		var options struct {
			IncludeSecrets bool   `json:"includeSecrets"`
			Passphrase     string `json:"passphrase,omitempty"`
		}
		rawOptions, readErr := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
		if readErr != nil || len(rawOptions) > 64<<10 || decodeStrict(rawOptions, &options) != nil || (options.IncludeSecrets != (options.Passphrase != "")) {
			f.t.Error("invalid create options")
			http.Error(w, "bad request", 400)
			return
		}
		raw := backupFixtureArchive(f.t, options.Passphrase)
		contentType := "application/zip"
		if options.IncludeSecrets {
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Andaru-Saved-Version", "7")
		w.Header().Set("Andaru-Captured-At", "2026-11-15T12:00:00.000000123Z")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	case "/api/v1/restores/preview", "/api/v1/restores":
		archiveRaw, options, ok := f.readMultipart(r)
		if !ok {
			http.Error(w, "bad request", 400)
			return
		}
		decoded, err := backup.Decode(r.Context(), archiveRaw, options.Passphrase)
		if err != nil {
			f.writeError(w, 422, "DECRYPTION_FAILED", "The backup could not be decrypted.")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.URL.Path == "/api/v1/restores/preview" {
			f.mu.Lock()
			f.preview = append([]byte(nil), archiveRaw...)
			f.mu.Unlock()
			if len(options.Stores) == 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{"state": "inspection", "manifest": decoded.Manifest})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"state": "restore-preview", "manifest": decoded.Manifest, "expectedSavedVersion": "4",
					"configurationChanges": map[string]int{"created": 1, "updated": 0, "deleted": 0},
					"secretChanges":        map[string]int{"created": 0, "updated": 1, "deleted": 0},
					"unresolvedSecrets":    []string{}, "blockingSecrets": []string{}, "hardwareFindings": []any{},
				})
			}
			if f.previewProcessed != nil {
				f.previewOnce.Do(func() { close(f.previewProcessed) })
			}
			return
		}
		f.mu.Lock()
		same := bytes.Equal(f.preview, archiveRaw)
		f.mu.Unlock()
		if !same || options.ExpectedSavedVersion != "4" || len(options.Stores) == 0 {
			f.t.Error("commit did not pin preview bytes/options/version")
			http.Error(w, "bad request", 400)
			return
		}
		f.commits.Add(1)
		if f.dropCommit.Load() {
			connection, _, hijackErr := w.(http.Hijacker).Hijack()
			if hijackErr == nil {
				_ = connection.Close()
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "saved", "savedVersion": "5", "unresolvedSecrets": []string{}})
	default:
		http.NotFound(w, r)
	}
}

func (f *strictBackupFixture) readMultipart(r *http.Request) ([]byte, restoreOptions, bool) {
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		return nil, restoreOptions{}, false
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, restoreOptions{}, false
	}
	var archiveRaw []byte
	var options restoreOptions
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || seen[part.FormName()] {
			return nil, restoreOptions{}, false
		}
		seen[part.FormName()] = true
		switch part.FormName() {
		case "archive":
			archiveRaw, err = io.ReadAll(io.LimitReader(part, backup.MaxArchiveSize+1))
		case "options":
			rawOptions, readErr := io.ReadAll(io.LimitReader(part, (64<<10)+1))
			if readErr != nil || len(rawOptions) > 64<<10 {
				return nil, restoreOptions{}, false
			}
			err = decodeStrict(rawOptions, &options)
		default:
			return nil, restoreOptions{}, false
		}
		if err != nil {
			return nil, restoreOptions{}, false
		}
	}
	return archiveRaw, options, seen["archive"] && seen["options"] && len(archiveRaw) <= backup.MaxArchiveSize
}

func (f *strictBackupFixture) writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "details": []any{}}})
}

func startBackupFixture(t *testing.T, fixture http.Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: fixture}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	return path
}

func withBackupPTY(t *testing.T) (*os.File, *os.File, func()) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	old := openBackupTTY
	openBackupTTY = func() (*os.File, error) {
		fd, err := unix.Dup(int(slave.Fd()))
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "backup-test-tty"), nil
	}
	return master, slave, func() { openBackupTTY = old; master.Close(); slave.Close() }
}

func awaitBackupPrompt(t *testing.T, master *os.File, marker string, transcript *bytes.Buffer) {
	t.Helper()
	buf := make([]byte, 1024)
	poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if strings.Contains(transcript.String(), marker) {
			return
		}
		if n, _ := unix.Poll(poll, 50); n > 0 {
			n, err := unix.Read(int(master.Fd()), buf)
			if err == nil {
				transcript.Write(buf[:n])
			}
		}
	}
	t.Fatalf("prompt %q not found in %q", marker, transcript.String())
}

func awaitBackupPromptOrExit(t *testing.T, master *os.File, marker string, transcript *bytes.Buffer, code <-chan int) {
	t.Helper()
	buf := make([]byte, 1024)
	poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if strings.Contains(transcript.String(), marker) {
			return
		}
		select {
		case got := <-code:
			t.Fatalf("CLI exited with code %d before prompt %q: %s", got, marker, transcript.String())
		default:
		}
		if n, _ := unix.Poll(poll, 50); n > 0 {
			n, err := unix.Read(int(master.Fd()), buf)
			if err == nil {
				transcript.Write(buf[:n])
			}
		}
	}
	t.Fatalf("prompt %q not found after preview completed: %s", marker, transcript.String())
}

func TestBackupTerminalPromptDisablesEchoAndCancellationRestoresTermios(t *testing.T) {
	master, slave, cleanup := withBackupPTY(t)
	defer cleanup()
	initial, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := readBackupPassphrase(ctx, "Cancellation prompt: ")
		result <- err
	}()
	var transcript bytes.Buffer
	awaitBackupPrompt(t, master, "Cancellation prompt: ", &transcript)
	during, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if during.Lflag&unix.ECHO != 0 {
		t.Fatal("terminal echo remained enabled when the prompt became observable")
	}
	if _, err = master.WriteString("TEST_ONLY"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled prompt returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled terminal read did not stop")
	}
	restored, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if *restored != *initial {
		t.Fatal("terminal settings were not restored after cancellation")
	}
	if _, err = master.WriteString("NONSECRET\n"); err != nil {
		t.Fatal(err)
	}
	poll := []unix.PollFd{{Fd: int32(slave.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 2000); err != nil || n != 1 {
		t.Fatalf("next nonsecret terminal read was not ready: %v", err)
	}
	var next [64]byte
	n, err := unix.Read(int(slave.Fd()), next[:])
	if err != nil {
		t.Fatal(err)
	}
	if string(next[:n]) != "NONSECRET\n" {
		t.Fatal("cancelled passphrase remained queued for the next terminal read")
	}
}

func TestBackupTerminalCompletedLinePreservesQueuedInput(t *testing.T) {
	master, slave, cleanup := withBackupPTY(t)
	defer cleanup()
	initial, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		value, err := readBackupPassphrase(ctx, "Completed prompt: ")
		if err == nil && value != "TEST_ONLY" {
			err = errors.New("completed passphrase was not returned intact")
		}
		result <- err
	}()
	var transcript bytes.Buffer
	awaitBackupPrompt(t, master, "Completed prompt: ", &transcript)
	if _, err = master.WriteString("TEST_ONLY\nNONSECRET\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed terminal read did not stop")
	}
	restored, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if *restored != *initial {
		t.Fatal("terminal settings were not restored after a completed line")
	}
	poll := []unix.PollFd{{Fd: int32(slave.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 2000); err != nil || n != 1 {
		t.Fatalf("completed passphrase flushed the next terminal line: %v", err)
	}
	var next [64]byte
	n, err := unix.Read(int(slave.Fd()), next[:])
	if err != nil {
		t.Fatal(err)
	}
	if string(next[:n]) != "NONSECRET\n" {
		t.Fatal("completed passphrase changed the next terminal line")
	}
}

func TestBackupTerminalRejectsPassphraseOverLimit(t *testing.T) {
	master, _, cleanup := withBackupPTY(t)
	defer cleanup()
	result := make(chan error, 1)
	go func() {
		_, err := readBackupPassphrase(context.Background(), "Bounded prompt: ")
		result <- err
	}()
	var transcript bytes.Buffer
	awaitBackupPrompt(t, master, "Bounded prompt: ", &transcript)
	_, _ = master.WriteString(strings.Repeat("x", backup.MaxPassphraseBytes+1) + "\n")
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("oversized terminal passphrase was accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("oversized terminal passphrase was not rejected promptly")
	}
	if strings.Contains(transcript.String(), strings.Repeat("x", 32)) {
		t.Fatal("oversized passphrase was echoed")
	}
}

func TestBackupCLIPassphraseMismatchAndWrongConfirmationDoNotMutate(t *testing.T) {
	fixture := &strictBackupFixture{t: t}
	socket := startBackupFixture(t, fixture)
	dir := t.TempDir()
	master, _, cleanup := withBackupPTY(t)
	defer cleanup()
	var stdout, stderr, transcript bytes.Buffer
	output := filepath.Join(dir, "mismatch.age")
	code := make(chan int, 1)
	go func() {
		code <- runBackup(context.Background(), socket, []string{"create", "--output", output, "--include-secrets"}, nil, &stdout, &stderr)
	}()
	awaitBackupPrompt(t, master, "New backup passphrase:", &transcript)
	_, _ = master.WriteString("first-passphrase\n")
	awaitBackupPrompt(t, master, "Repeat backup passphrase:", &transcript)
	_, _ = master.WriteString("second-passphrase\n")
	if got := <-code; got == 0 {
		t.Fatal("mismatched passphrases succeeded")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("mismatched passphrases left an artifact")
	}
	if strings.Contains(transcript.String(), "first-passphrase") || strings.Contains(transcript.String(), "second-passphrase") {
		t.Fatal("mismatched passphrase was echoed")
	}

	input := filepath.Join(dir, "plain.zip")
	if err := os.WriteFile(input, backupFixtureArchive(t, ""), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	transcript.Reset()
	code = make(chan int, 1)
	go func() {
		code <- runBackup(context.Background(), socket, []string{"restore", "--input", input, "--stores", "configuration"}, nil, &stdout, &stderr)
	}()
	awaitBackupPrompt(t, master, "Type RESTORE", &transcript)
	_, _ = master.WriteString("NOPE\n")
	if got := <-code; got == 0 || fixture.commits.Load() != 0 {
		t.Fatal("wrong confirmation committed restore")
	}
	if strings.Contains(transcript.String(), "NOPE") {
		t.Fatal("restore confirmation was echoed")
	}
}

func TestBackupCLIReportsUnknownCommitOutcome(t *testing.T) {
	fixture := &strictBackupFixture{t: t}
	fixture.dropCommit.Store(true)
	socket := startBackupFixture(t, fixture)
	input := filepath.Join(t.TempDir(), "plain.zip")
	if err := os.WriteFile(input, backupFixtureArchive(t, ""), 0600); err != nil {
		t.Fatal(err)
	}
	master, _, cleanup := withBackupPTY(t)
	defer cleanup()
	var stdout, stderr, transcript bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- runBackup(context.Background(), socket, []string{"restore", "--input", input, "--stores", "configuration"}, nil, &stdout, &stderr)
	}()
	awaitBackupPrompt(t, master, "Type RESTORE", &transcript)
	_, _ = master.WriteString("RESTORE\n")
	if got := <-code; got == 0 || fixture.commits.Load() != 1 {
		t.Fatal("lost commit reply was not treated as an unknown outcome")
	}
	if !strings.Contains(stderr.String(), "outcome may be unknown") || !strings.Contains(stderr.String(), "inspect the current saved head") || strings.Contains(stdout.String(), `"state":"saved"`) {
		t.Fatalf("unknown outcome was not actionable: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestBackupCLIRejectsNoninteractiveSecretsAndRestore(t *testing.T) {
	fixture := &strictBackupFixture{t: t}
	socket := startBackupFixture(t, fixture)
	old := openBackupTTY
	openBackupTTY = func() (*os.File, error) { return nil, unix.ENXIO }
	defer func() { openBackupTTY = old }()
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	secretOutput := filepath.Join(dir, "secret.age")
	if code := runBackup(context.Background(), socket, []string{"create", "--output", secretOutput, "--include-secrets"}, bytes.NewBufferString(backupTestPassphrase), &stdout, &stderr); code == 0 {
		t.Fatal("nonterminal secret create succeeded")
	}
	if _, err := os.Stat(secretOutput); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused create left output")
	}
	plain := filepath.Join(dir, "plain.zip")
	if err := os.WriteFile(plain, backupFixtureArchive(t, ""), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runBackup(context.Background(), socket, []string{"restore", "--input", plain, "--stores", "configuration"}, strings.NewReader("RESTORE\n"), &stdout, &stderr); code == 0 || fixture.commits.Load() != 0 {
		t.Fatal("nonterminal restore committed")
	}
}

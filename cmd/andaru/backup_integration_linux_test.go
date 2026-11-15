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

func TestBackupCLITerminalSecretsAndRestore(t *testing.T) {
	fixture := &strictBackupFixture{t: t, previewProcessed: make(chan struct{})}
	socket := startBackupFixture(t, fixture)
	dir := t.TempDir()
	output := filepath.Join(dir, "secret.zip.age")
	master, _, cleanup := withBackupPTY(t)
	defer cleanup()
	var stdout, stderr, transcript bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- runBackup(context.Background(), socket, []string{"create", "--output", output, "--include-secrets"}, nil, &stdout, &stderr)
	}()
	awaitBackupPrompt(t, master, "New backup passphrase:", &transcript)
	_, _ = master.WriteString(backupTestPassphrase + "\n")
	awaitBackupPrompt(t, master, "Repeat backup passphrase:", &transcript)
	_, _ = master.WriteString(backupTestPassphrase + "\n")
	if got := <-code; got != 0 {
		t.Fatalf("create code %d: %s", got, stderr.String())
	}
	raw, err := os.ReadFile(output)
	if err != nil || !bytes.HasPrefix(raw, []byte("age-encryption.org/")) || bytes.Contains(raw, []byte("PLAINTEXT_SENTINEL")) {
		t.Fatal("secret archive was not safely encrypted", err)
	}
	var stat unix.Stat_t
	if err = unix.Stat(output, &stat); err != nil || stat.Mode&0777 != 0600 || stat.Uid != 0 {
		t.Fatal("backup output is not root-owned mode 0600", err)
	}
	if strings.Contains(transcript.String()+stdout.String()+stderr.String(), backupTestPassphrase) {
		t.Fatal("passphrase was echoed or reported")
	}

	transcript.Reset()
	stdout.Reset()
	stderr.Reset()
	code = make(chan int, 1)
	go func() {
		code <- runBackup(context.Background(), socket, []string{"restore", "--input", output, "--stores", "configuration,configurationSecrets"}, nil, &stdout, &stderr)
	}()
	awaitBackupPrompt(t, master, "Backup passphrase:", &transcript)
	_, _ = master.WriteString(backupTestPassphrase + "\n")
	// Scrypt work factor 18 is intentionally expensive under -race; synchronize on
	// completed preview processing instead of weakening crypto or guessing its duration.
	select {
	case <-fixture.previewProcessed:
	case got := <-code:
		t.Fatalf("restore exited with code %d before preview completed: %s", got, stderr.String())
	case <-time.After(2 * time.Minute):
		t.Fatal("restore preview did not complete within the test's crypto-phase budget")
	}
	awaitBackupPromptOrExit(t, master, "Type RESTORE", &transcript, code)
	replacement := backupFixtureArchive(t, "different-passphrase")
	if err = os.WriteFile(output, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = master.WriteString("RESTORE\n")
	if got := <-code; got != 0 || fixture.commits.Load() != 1 {
		t.Fatalf("restore code %d commits %d: %s", got, fixture.commits.Load(), stderr.String())
	}
	if strings.Count(transcript.String(), "RESTORE") != 1 || strings.Contains(transcript.String(), backupTestPassphrase) {
		t.Fatal("terminal secrets or confirmation were echoed")
	}
	if !strings.Contains(stdout.String(), `"state":"restore-preview"`) || !strings.Contains(stdout.String(), `"state":"saved"`) || !strings.Contains(stdout.String(), `"selectedStores":["configuration","configurationSecrets"]`) || !strings.Contains(stdout.String(), `"networkingApplied":false`) {
		t.Fatal("missing structured preview/status", stdout.String())
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

func TestBackupCLIOutputNoReplaceAndSafeInputs(t *testing.T) {
	fixture := &strictBackupFixture{t: t}
	socket := startBackupFixture(t, fixture)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	output := filepath.Join(dir, "backup.zip")
	if code := runBackup(context.Background(), socket, []string{"create", "--output", output}, nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"state":"created"`) {
		t.Fatalf("plain nonterminal create failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runBackup(context.Background(), socket, []string{"inspect", "--input", output}, nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"state":"inspection"`) {
		t.Fatalf("plain nonterminal inspect failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("KEEP_ME"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runBackup(context.Background(), socket, []string{"create", "--output", output}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("overwrote existing artifact")
	}
	if raw, _ := os.ReadFile(output); string(raw) != "KEEP_ME" {
		t.Fatalf("existing output changed: %q", raw)
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("TARGET"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, output); err != nil {
		t.Fatal(err)
	}
	if code := runBackup(context.Background(), socket, []string{"create", "--output", output}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("followed output symlink")
	}
	if raw, _ := os.ReadFile(target); string(raw) != "TARGET" {
		t.Fatal("symlink target changed")
	}
	inputLink := filepath.Join(dir, "input-link")
	if err := os.Symlink(target, inputLink); err != nil {
		t.Fatal(err)
	}
	if code := runBackup(context.Background(), socket, []string{"inspect", "--input", inputLink}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("followed input symlink")
	}

	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if code := runBackup(context.Background(), socket, []string{"inspect", "--input", fifo}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("accepted FIFO")
	}
	oversize := filepath.Join(dir, "oversize")
	file, err := os.Create(oversize)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(backup.MaxArchiveSize + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if code := runBackup(context.Background(), socket, []string{"inspect", "--input", oversize}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("accepted oversized input")
	}
}

func TestBackupCLIOutputPublicationCollisionAndSyncFailure(t *testing.T) {
	fixture := &strictBackupFixture{t: t}
	socket := startBackupFixture(t, fixture)
	dir := t.TempDir()
	output := filepath.Join(dir, "race.zip")
	oldRename := backupRenameNoReplace
	backupRenameNoReplace = func(oldDir int, oldName string, newDir int, newName string) error {
		fd, err := unix.Openat(newDir, newName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, _ = unix.Write(fd, []byte("RACE_WINNER"))
		_ = unix.Close(fd)
		return oldRename(oldDir, oldName, newDir, newName)
	}
	defer func() { backupRenameNoReplace = oldRename }()
	var stdout, stderr bytes.Buffer
	if code := runBackup(context.Background(), socket, []string{"create", "--output", output}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("publication collision succeeded")
	}
	backupRenameNoReplace = oldRename
	if raw, _ := os.ReadFile(output); string(raw) != "RACE_WINNER" {
		t.Fatalf("collision output changed: %q", raw)
	}

	output = filepath.Join(dir, "sync-failure.zip")
	oldFsync := backupFsync
	calls := 0
	backupFsync = func(fd int) error {
		calls++
		if calls == 2 {
			return unix.EIO
		}
		return oldFsync(fd)
	}
	defer func() { backupFsync = oldFsync }()
	stdout.Reset()
	stderr.Reset()
	if code := runBackup(context.Background(), socket, []string{"create", "--output", output}, nil, &stdout, &stderr); code == 0 || stdout.Len() != 0 {
		t.Fatal("sync failure reported success")
	}
	backupFsync = oldFsync
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".andaru-backup-") {
			t.Fatal("left plaintext staging file")
		}
	}
}

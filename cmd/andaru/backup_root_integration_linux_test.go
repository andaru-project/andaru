//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/andaru-project/andaru/internal/backup"
)

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

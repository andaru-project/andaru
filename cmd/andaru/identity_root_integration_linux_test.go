//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/engine"
	"golang.org/x/sys/unix"
)

func awaitIdentityPromptCount(t *testing.T, master *os.File, marker string, count int, transcript *bytes.Buffer) {
	t.Helper()
	buf := make([]byte, 1024)
	poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if strings.Count(transcript.String(), marker) >= count {
			return
		}
		if n, _ := unix.Poll(poll, 50); n > 0 {
			n, err := unix.Read(int(master.Fd()), buf)
			if err == nil {
				transcript.Write(buf[:n])
			}
		}
	}
	t.Fatalf("prompt %q count %d not found in %q", marker, count, transcript.String())
}

func TestIdentityCLIRootPTYAndActualAPIFlow(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("identity integration requires root")
	}
	root, err := os.MkdirTemp("/var/tmp", "andaru-identity-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "run", "engine.sock")
	stateDir := filepath.Join(root, "state")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Serve(ctx, socket, stateDir) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("engine shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("engine did not stop")
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err = os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine socket unavailable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	master, _, cleanup := withBackupPTY(t)
	defer cleanup()
	const firstPassword = "unique-bootstrap-password-41"
	var bootstrapOut, bootstrapErr bytes.Buffer
	bootstrapCode := make(chan int, 1)
	go func() {
		bootstrapCode <- runIdentity(context.Background(), socket, []string{"users", "bootstrap-admin", "--login", "rootadmin", "--display-name", "Root Administrator"}, &bootstrapOut, &bootstrapErr)
	}()
	var transcript bytes.Buffer
	awaitIdentityPromptCount(t, master, "New administrator password:", 1, &transcript)
	_, _ = master.WriteString(firstPassword + "\n")
	awaitIdentityPromptCount(t, master, "Repeat new administrator password:", 1, &transcript)
	_, _ = master.WriteString(firstPassword + "\n")
	awaitIdentityPromptCount(t, master, "Type BOOTSTRAP", 1, &transcript)
	_, _ = master.WriteString("BOOTSTRAP\n")
	if code := <-bootstrapCode; code != 0 || bootstrapErr.Len() != 0 || strings.Contains(transcript.String(), firstPassword) || strings.Contains(bootstrapOut.String(), firstPassword) {
		t.Fatalf("bootstrap code=%d stdout=%q stderr=%q tty=%q", code, bootstrapOut.String(), bootstrapErr.String(), transcript.String())
	}
	var bootstrap struct{ Target string `json:"target"` }
	if err = json.Unmarshal(bootstrapOut.Bytes(), &bootstrap); err != nil || bootstrap.Target == "" {
		t.Fatalf("bootstrap response=%q err=%v", bootstrapOut.String(), err)
	}

	call := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		if code := runIdentity(context.Background(), socket, args, &out, &diag); code != 0 {
			t.Fatalf("identity %v code=%d stdout=%q stderr=%q", args, code, out.String(), diag.String())
		}
		return out.Bytes()
	}
	users := call("users", "list", "--limit", "10")
	var page struct {
		Items []struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		} `json:"items"`
	}
	if err = json.Unmarshal(users, &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != bootstrap.Target {
		t.Fatalf("users=%s err=%v", users, err)
	}
	created := call("users", "create", "--login", "service01", "--kind", "service", "--roles", "read-only", "--revision", "1")
	var operation struct{ Target string `json:"target"` }
	if err = json.Unmarshal(created, &operation); err != nil || operation.Target == "" {
		t.Fatalf("create=%s err=%v", created, err)
	}
	serviceRaw := call("users", "get", operation.Target)
	var service struct{ Revision int64 `json:"revision"` }
	if err = json.Unmarshal(serviceRaw, &service); err != nil {
		t.Fatal(err)
	}
	call("users", "update", operation.Target, "--display-name", "Automation", "--revision", fmt.Sprint(service.Revision))
	serviceRaw = call("users", "get", operation.Target)
	if err = json.Unmarshal(serviceRaw, &service); err != nil {
		t.Fatal(err)
	}
	token := call("tokens", "create", "--account-id", operation.Target, "--name", "integration", "--scope", "status.read", "--no-expiry")
	var issued struct {
		Selector string `json:"selector"`
		Secret   string `json:"secret"`
	}
	if err = json.Unmarshal(token, &issued); err != nil || issued.Selector == "" || issued.Secret == "" {
		t.Fatalf("token=%s err=%v", token, err)
	}
	listedTokens := call("tokens", "list", "--account-id", operation.Target)
	if !bytes.Contains(listedTokens, []byte(issued.Selector)) || bytes.Contains(listedTokens, []byte(issued.Secret)) {
		t.Fatalf("token list leaked or omitted token: %s", listedTokens)
	}
	serviceRaw = call("users", "get", operation.Target)
	if err = json.Unmarshal(serviceRaw, &service); err != nil {
		t.Fatal(err)
	}
	call("users", "disable", operation.Target, "--revision", fmt.Sprint(service.Revision))
	call("tokens", "revoke", issued.Selector)
	serviceRaw = call("users", "get", operation.Target)
	if err = json.Unmarshal(serviceRaw, &service); err != nil {
		t.Fatal(err)
	}
	call("users", "delete", operation.Target, "--revision", fmt.Sprint(service.Revision))
	var missingOut, missingErr bytes.Buffer
	if code := runIdentity(context.Background(), socket, []string{"users", "get", operation.Target}, &missingOut, &missingErr); code != 1 || missingOut.Len() != 0 || !strings.Contains(missingErr.String(), "NOT_FOUND") {
		t.Fatalf("deleted user remained readable: code=%d stdout=%q stderr=%q", code, missingOut.String(), missingErr.String())
	}
	audits := call("audit", "list", "--limit", "100")
	for _, action := range []string{"account.created", "account.updated", "account.disabled", "account.deleted", "token.created", "token.revoked"} {
		if !bytes.Contains(audits, []byte(`"action":"`+action+`"`)) {
			t.Fatalf("actual %s audit event missing: %s", action, audits)
		}
	}

	adminRaw := call("users", "get", bootstrap.Target)
	var admin struct{ Revision int64 `json:"revision"` }
	if err = json.Unmarshal(adminRaw, &admin); err != nil || admin.Revision < 3 {
		t.Fatalf("admin=%s err=%v", adminRaw, err)
	}
	const resetPassword = "unique-reset-password-42"
	var resetOut, resetErr bytes.Buffer
	resetCode := make(chan int, 1)
	go func() {
		resetCode <- runIdentity(context.Background(), socket, []string{"users", "password-reset", bootstrap.Target, "--revision", fmt.Sprint(admin.Revision)}, &resetOut, &resetErr)
	}()
	awaitIdentityPromptCount(t, master, "New password:", 1, &transcript)
	_, _ = master.WriteString(resetPassword + "\n")
	awaitIdentityPromptCount(t, master, "Repeat new password:", 1, &transcript)
	_, _ = master.WriteString(resetPassword + "\n")
	if code := <-resetCode; code != 0 || resetErr.Len() != 0 || strings.Contains(transcript.String(), resetPassword) || strings.Contains(resetOut.String(), resetPassword) {
		t.Fatalf("reset code=%d stdout=%q stderr=%q tty=%q", code, resetOut.String(), resetErr.String(), transcript.String())
	}
}

func TestIdentityCLIBootstrapPromptsFailClosed(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("identity integration requires root")
	}
	t.Run("password mismatch", func(t *testing.T) {
		master, _, cleanup := withBackupPTY(t)
		defer cleanup()
		var stdout, stderr, transcript bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- runIdentity(context.Background(), "/does/not/exist", []string{"users", "bootstrap-admin", "--login", "rootadmin"}, &stdout, &stderr)
		}()
		awaitIdentityPromptCount(t, master, "New administrator password:", 1, &transcript)
		_, _ = master.WriteString("unique-bootstrap-password-41\n")
		awaitIdentityPromptCount(t, master, "Repeat new administrator password:", 1, &transcript)
		_, _ = master.WriteString("different-bootstrap-password-42\n")
		if code := <-done; code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "matching passwords") || strings.Contains(transcript.String(), "unique-bootstrap-password-41") {
			t.Fatalf("mismatch code=%d stdout=%q stderr=%q tty=%q", code, stdout.String(), stderr.String(), transcript.String())
		}
	})
	t.Run("typed confirmation", func(t *testing.T) {
		master, _, cleanup := withBackupPTY(t)
		defer cleanup()
		var stdout, stderr, transcript bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- runIdentity(context.Background(), "/does/not/exist", []string{"users", "bootstrap-admin", "--login", "rootadmin"}, &stdout, &stderr)
		}()
		awaitIdentityPromptCount(t, master, "New administrator password:", 1, &transcript)
		_, _ = master.WriteString("unique-bootstrap-password-41\n")
		awaitIdentityPromptCount(t, master, "Repeat new administrator password:", 1, &transcript)
		_, _ = master.WriteString("unique-bootstrap-password-41\n")
		awaitIdentityPromptCount(t, master, "Type BOOTSTRAP", 1, &transcript)
		_, _ = master.WriteString("NO\n")
		if code := <-done; code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "cancelled") || strings.Contains(transcript.String(), "unique-bootstrap-password-41") {
			t.Fatalf("confirmation code=%d stdout=%q stderr=%q tty=%q", code, stdout.String(), stderr.String(), transcript.String())
		}
	})
}

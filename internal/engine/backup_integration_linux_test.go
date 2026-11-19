//go:build integration

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/backup"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/identity"
)

func init() {
	for i, argument := range os.Args {
		if argument != "--backup-peer-helper" || i+1 >= len(os.Args) {
			continue
		}
		client := unixTestClient(os.Args[i+1])
		defer client.CloseIdleConnections()
		for _, input := range []struct {
			path, contentType, body string
		}{
			{backupPath, "application/json", `{"includeSecrets":true,"passphrase":"not-read"}`},
			{restorePath, "multipart/form-data; boundary=x", "--x\r\nsecret bytes\r\n--x--\r\n"},
		} {
			request, _ := http.NewRequest(http.MethodPost, "http://unix"+input.path, strings.NewReader(input.body))
			request.Header.Set("Content-Type", input.contentType)
			response, err := client.Do(request)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(91)
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusForbidden || !bytes.Contains(body, []byte("FORBIDDEN")) || bytes.Contains(body, []byte("not-read")) || bytes.Contains(body, []byte("secret bytes")) {
				os.Exit(92)
			}
		}
		fmt.Println("403 FORBIDDEN")
		os.Exit(0)
	}
}

func startBackupEngine(t *testing.T) (*http.Client, string, context.CancelFunc) {
	t.Helper()
	requireRoot(t)
	root := privateTestDir(t)
	stateDir := filepath.Join(root, "state")
	socket := filepath.Join(root, "run", "engine.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, socket, stateDir) }()
	client := unixTestClient(socket)
	client.Timeout = 30 * time.Second
	deadline := time.Now().Add(5 * time.Second)
	for {
		request, _ := http.NewRequest(http.MethodGet, "http://unix/api/v1/openapi.json", nil)
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("backup engine did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("engine shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("engine did not stop")
		}
	})
	return client, stateDir, cancel
}

func postBackup(t *testing.T, client *http.Client, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://unix"+backupPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func postRestore(t *testing.T, client *http.Client, path string, archive []byte, options string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("archive", "ignored-client-name.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	part, err = writer.CreateFormField("options")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, options); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://unix"+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func responseBytes(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBackupAPIRoundTrip(t *testing.T) {
	client, stateDir, _ := startBackupEngine(t)
	created := postBackup(t, client, `{"includeSecrets":false}`)
	archive := responseBytes(t, created)
	if created.StatusCode != http.StatusOK || created.Header.Get("Content-Type") != "application/zip" || created.Header.Get("Andaru-Saved-Version") != "0" || created.Header.Get("Andaru-Identity-Revision") != "0" || created.Header.Get("Andaru-Captured-At") == "" {
		t.Fatalf("create contract: %d %v %s", created.StatusCode, created.Header, archive)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "generations", "pointers.json")); !os.IsNotExist(err) {
		t.Fatalf("backup wrote generation pointers: %v", err)
	}

	secretBackup := postBackup(t, client, `{"includeSecrets":true,"passphrase":"correct horse battery staple"}`)
	secretArchive := responseBytes(t, secretBackup)
	if secretBackup.StatusCode != http.StatusOK || secretBackup.Header.Get("Content-Type") != "application/octet-stream" || !bytes.HasPrefix(secretArchive, []byte("age-encryption.org/")) || bytes.Contains(secretArchive, []byte("correct horse battery staple")) {
		t.Fatalf("secret backup exposed input or used the wrong envelope: %d %v", secretBackup.StatusCode, secretBackup.Header)
	}

	inspection := postRestore(t, client, restorePreviewPath, archive, `{}`)
	inspectionBody := responseBytes(t, inspection)
	if inspection.StatusCode != http.StatusOK || !bytes.Contains(inspectionBody, []byte(`"state":"inspection"`)) || bytes.Contains(inspectionBody, []byte("configurationChanges")) {
		t.Fatalf("inspection: %d %s", inspection.StatusCode, inspectionBody)
	}
	preview := postRestore(t, client, restorePreviewPath, archive, `{"stores":["configuration"]}`)
	previewBody := responseBytes(t, preview)
	var plan struct {
		State    string            `json:"state"`
		Expected string            `json:"expectedSavedVersion"`
		Hardware []json.RawMessage `json:"hardwareFindings"`
	}
	if err := json.Unmarshal(previewBody, &plan); err != nil {
		t.Fatal(err)
	}
	if preview.StatusCode != http.StatusOK || plan.State != "restore-preview" || plan.Expected != "0" || plan.Hardware == nil {
		t.Fatalf("preview: %d %s", preview.StatusCode, previewBody)
	}
	committed := postRestore(t, client, restorePath, archive, `{"operationId":"restore-roundtrip-1","stores":["configuration"],"expectedSavedVersion":"0"}`)
	commitBody := responseBytes(t, committed)
	if committed.StatusCode != http.StatusOK || !bytes.Contains(commitBody, []byte(`"state":"saved"`)) || !bytes.Contains(commitBody, []byte(`"savedVersion":"1"`)) {
		t.Fatalf("commit: %d %s", committed.StatusCode, commitBody)
	}
	replayed := postRestore(t, client, restorePath, archive, `{"operationId":"restore-roundtrip-1","stores":["configuration"],"expectedSavedVersion":"0"}`)
	replayedBody := responseBytes(t, replayed)
	if replayed.StatusCode != http.StatusOK || !bytes.Contains(replayedBody, []byte(`"replayed":true`)) {
		t.Fatalf("replay: %d %s", replayed.StatusCode, replayedBody)
	}
	stale := postRestore(t, client, restorePath, archive, `{"operationId":"restore-roundtrip-2","stores":["configuration"],"expectedSavedVersion":"0"}`)
	staleBody := responseBytes(t, stale)
	if stale.StatusCode != http.StatusPreconditionFailed || !bytes.Contains(staleBody, []byte("PRECONDITION_FAILED")) {
		t.Fatalf("stale replay: %d %s", stale.StatusCode, staleBody)
	}
	missing := postRestore(t, client, restorePath, archive, `{"stores":["configuration"],"expectedSavedVersion":"1"}`)
	missingBody := responseBytes(t, missing)
	if missing.StatusCode != http.StatusPreconditionRequired || !bytes.Contains(missingBody, []byte("PRECONDITION_REQUIRED")) {
		t.Fatalf("missing precondition: %d %s", missing.StatusCode, missingBody)
	}
}

func TestRestoreAPINoMutationOnInvalidArchive(t *testing.T) {
	client, _, _ := startBackupEngine(t)
	created := postBackup(t, client, `{"includeSecrets":false}`)
	archive := responseBytes(t, created)
	invalid := postRestore(t, client, restorePath, archive[:len(archive)/2], `{"operationId":"invalid-archive-restore","stores":["configuration"],"expectedSavedVersion":"0"}`)
	invalidBody := responseBytes(t, invalid)
	if invalid.StatusCode != http.StatusBadRequest || !bytes.Contains(invalidBody, []byte("INVALID_ARCHIVE")) {
		t.Fatalf("truncated archive: %d %s", invalid.StatusCode, invalidBody)
	}
	after := postBackup(t, client, `{"includeSecrets":false}`)
	body := responseBytes(t, after)
	if after.StatusCode != http.StatusOK || after.Header.Get("Andaru-Saved-Version") != "0" {
		t.Fatalf("invalid archive mutated saved head: %d %v %s", after.StatusCode, after.Header, body)
	}
}

func TestBackupAPIRejectsNoncanonicalOptionKeysWithoutMutation(t *testing.T) {
	client, _, _ := startBackupEngine(t)
	baseline := postBackup(t, client, `{"includeSecrets":false}`)
	archive := responseBytes(t, baseline)
	if baseline.StatusCode != http.StatusOK || baseline.Header.Get("Andaru-Saved-Version") != "0" {
		t.Fatalf("baseline backup: %d %v", baseline.StatusCode, baseline.Header)
	}
	for _, body := range []string{
		`{"IncludeSecrets":false}`,
		`{"includeSecrets":false,"IncludeSecrets":true}`,
	} {
		response := postBackup(t, client, body)
		responseBody := responseBytes(t, response)
		if response.StatusCode != http.StatusBadRequest || !bytes.Contains(responseBody, []byte("INVALID_REQUEST")) {
			t.Fatalf("noncanonical create options accepted: %d %s", response.StatusCode, responseBody)
		}
	}
	for _, options := range []string{
		`{"Stores":["configuration"]}`,
		`{"stores":["configuration"],"Stores":[]}`,
	} {
		response := postRestore(t, client, restorePreviewPath, archive, options)
		responseBody := responseBytes(t, response)
		if response.StatusCode != http.StatusBadRequest || !bytes.Contains(responseBody, []byte("INVALID_REQUEST")) {
			t.Fatalf("noncanonical restore options accepted: %d %s", response.StatusCode, responseBody)
		}
	}
	after := postBackup(t, client, `{"includeSecrets":false}`)
	afterBody := responseBytes(t, after)
	if after.StatusCode != http.StatusOK || after.Header.Get("Andaru-Saved-Version") != "0" {
		t.Fatalf("malformed options changed saved state: %d %v %s", after.StatusCode, after.Header, afterBody)
	}
}

func TestBackupCaptureConcurrentSaveAndActivation(t *testing.T) {
	requireRoot(t)
	stateDir := filepath.Join(privateTestDir(t), "state")
	store, err := config.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	identities, err := identity.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer identities.Close()
	audits, err := audit.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer audits.Close()
	security := newSecurityCoordinator(identities, audits, stateDir)
	generationDir := generation.Dir(stateDir)
	if err := os.Mkdir(generationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(generationDir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	backups := newBackupHandler(store, identities, security, stateDir, func(context.Context) ([]host.LinkStatus, error) { return []host.LinkStatus{}, nil })
	handler := newHandler(func(context.Context) ([]host.LinkStatus, error) { return []host.LinkStatus{}, nil },
		newSavedHandler(store, func(context.Context) ([]host.LinkStatus, error) { return []host.LinkStatus{}, nil },
			func() (host.RealizerFacts, error) { return host.RealizerFacts{}, nil }, http.NotFoundHandler(), backups))
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		request := rootRequest(http.MethodPost, backupPath)
		request.Header.Set("Content-Type", "application/json")
		request.Body = io.NopCloser(strings.NewReader(`{"includeSecrets":false}`))
		handler.ServeHTTP(response, request)
		result <- response
	}()

	saved := configCall(handler, http.MethodPost, "/api/v1/config/zones", `{"name":"captured"}`, "")
	if saved.Code != http.StatusCreated || saved.Header().Get("Andaru-Saved-Version") != "1" {
		lock.Close()
		t.Fatalf("concurrent save: %d %s", saved.Code, saved.Body)
	}
	id, err := generation.Create(generationDir, generation.Manifest{SavedVersion: "1", CreatedAt: time.Now().UTC()}, map[string][]byte{})
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	pointers := []byte(`{"confirmed":1,"lastKnownGood":1,"pending":null,"live":1,"takeover":false}`)
	if id != 1 {
		lock.Close()
		t.Fatalf("generation id = %d", id)
	}
	if err := os.WriteFile(filepath.Join(generationDir, "pointers.json"), pointers, 0o600); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	response := <-result
	if response.Code != http.StatusOK {
		t.Fatalf("backup: %d %s", response.Code, response.Body)
	}
	archive, err := backup.Decode(context.Background(), response.Body.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	if archive.Manifest.SavedVersion != "1" || archive.Manifest.Network.Live == nil || *archive.Manifest.Network.Live != 1 || archive.Manifest.Network.LiveSavedVersion != "1" {
		t.Fatalf("mixed capture provenance: %+v", archive.Manifest)
	}
}

func TestBackupAPIDeniesNonRootBeforeReadingBodies(t *testing.T) {
	root := traversablePeerDir(t)
	socketDir := filepath.Join(root, "run")
	socket := filepath.Join(socketDir, "engine.sock")
	startTestEngine(t, socket, nil)
	if err := os.Chmod(socketDir, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0o666); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chmod(socket, 0o600)
		_ = os.Chmod(socketDir, 0o700)
	}()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "backup-peer-helper")
	target, err := os.OpenFile(child, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		source.Close()
		target.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(child, "--backup-peer-helper", socket)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}}}
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "403 FORBIDDEN" {
		t.Fatalf("non-root backup request was not denied: %q %v", output, err)
	}
}

func TestCredentialBackupRequiresHumanReauthenticationAndEncryption(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()
	stateDir := filepath.Join(privateTestDir(t), "state")
	configuration, err := config.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer configuration.Close()
	identities, err := identity.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer identities.Close()
	account, _, err := identities.BootstrapAdmin(ctx, "backupadmin", "Backup administrator", []byte("backup-admin-password-2026"))
	if err != nil {
		t.Fatal(err)
	}
	audits, err := audit.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer audits.Close()
	security := newSecurityCoordinator(identities, audits, stateDir)
	handler := newBackupHandler(configuration, identities, security, stateDir, func(context.Context) ([]host.LinkStatus, error) { return []host.LinkStatus{}, nil })
	principal := identity.Principal{Account: account, Actor: authz.Actor{Kind: authz.ActorAccount, ID: account.ID}, Capabilities: []authz.Capability{authz.BackupsCreate, authz.BackupsExportSecrets}}
	for _, tc := range []struct {
		name, password string
		status         int
	}{
		{"wrong password", "wrong-password-value", http.StatusForbidden},
		{"correct password", "backup-admin-password-2026", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, backupPath, strings.NewReader(fmt.Sprintf(`{"includeSecrets":false,"includeCredentials":true,"passphrase":"credential-archive-passphrase","password":%q}`, tc.password)))
			request.Header.Set("Content-Type", "application/json")
			request = request.WithContext(withVerifiedPrincipal(request.Context(), principal, "192.0.2.10"))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if tc.status != http.StatusOK {
				return
			}
			raw := response.Body.Bytes()
			if !bytes.HasPrefix(raw, []byte("age-encryption.org/")) {
				t.Fatal("credential archive was not whole-archive encrypted")
			}
			decoded, err := backup.Decode(ctx, raw, "credential-archive-passphrase")
			if err != nil || decoded.Documents.Users == nil || decoded.Documents.UserCredentials == nil || decoded.Documents.ConfigurationSecrets != nil {
				t.Fatalf("credential selection=%+v err=%v", decoded.Manifest.Scopes, err)
			}
		})
	}
}

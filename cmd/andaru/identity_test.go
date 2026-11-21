package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func asRootIdentityCLI(t *testing.T) {
	t.Helper()
	old := identityEUID
	identityEUID = func() int { return 0 }
	t.Cleanup(func() { identityEUID = old })
}

func TestIdentityCLIListAndCreateUseBoundedAPIContract(t *testing.T) {
	asRootIdentityCLI(t)
	var calls int
	socket := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch calls {
		case 1:
			if r.Method != http.MethodGet || r.URL.RequestURI() != "/api/v1/users?limit=2" {
				t.Fatalf("list request = %s %s", r.Method, r.URL.RequestURI())
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"12345678-1234-4234-8234-123456789abc","login":"rootadmin","displayName":"Root","kind":"human","enabled":true,"roleIds":["administrator"],"revision":7,"createdAt":1,"updatedAt":1}]}`))
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/users" || r.Header.Get("Idempotency-Key") == "" || r.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("create request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["login"] != "service01" || body["kind"] != "service" || body["enabled"] != true || body["expectedRevision"] != float64(7) {
				t.Fatalf("create body = %#v err=%v", body, err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"operationId":"cli-create","result":"success","target":"22345678-1234-4234-8234-123456789abc","metadata":{}}`))
		default:
			t.Fatalf("unexpected request %d", calls)
		}
	}))
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"ctl", "--socket", socket, "users", "list", "--limit", "2"}, strings.NewReader("ignored"), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"revision":7`) || stderr.Len() != 0 {
		t.Fatalf("list code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := run(context.Background(), []string{"ctl", "--socket", socket, "users", "create", "--login", "service01", "--kind", "service", "--revision", "7", "--roles", "read-only"}, strings.NewReader("ignored"), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"result":"success"`) || stderr.Len() != 0 {
		t.Fatalf("create code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestIdentityCLINoExpiryIsExplicitAndWarned(t *testing.T) {
	base := []string{"tokens", "create", "--account-id", "12345678-1234-4234-8234-123456789abc", "--name", "automation", "--scope", "status.read"}
	defaultExpiry, err := parseIdentityCommand(base)
	if err != nil || defaultExpiry.warning != "" || bytes.Contains(defaultExpiry.payload, []byte(`"noExpiry"`)) {
		t.Fatalf("default token request=%s warning=%q err=%v", defaultExpiry.payload, defaultExpiry.warning, err)
	}
	noExpiry, err := parseIdentityCommand(append(base, "--no-expiry"))
	if err != nil || noExpiry.warning == "" || !bytes.Contains(noExpiry.payload, []byte(`"noExpiry":true`)) {
		t.Fatalf("no-expiry token request=%s warning=%q err=%v", noExpiry.payload, noExpiry.warning, err)
	}
}

func TestIdentityCLIRequiresExplicitCASAndHasNoPasswordOption(t *testing.T) {
	asRootIdentityCLI(t)
	for _, args := range [][]string{
		{"users", "create", "--login", "service01", "--kind", "service"},
		{"users", "update", "12345678-1234-4234-8234-123456789abc", "--display-name", "Changed"},
		{"users", "disable", "12345678-1234-4234-8234-123456789abc"},
		{"users", "delete", "12345678-1234-4234-8234-123456789abc"},
		{"users", "password-reset", "12345678-1234-4234-8234-123456789abc", "--revision", "1", "--password", "forbidden"},
		{"users", "bootstrap-admin", "--login", "rootadmin", "--password", "forbidden"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runIdentity(context.Background(), "/does/not/exist", args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Run 'andaru ctl users --help'") {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestIdentityCLIMutationTransportFailureIsTruthful(t *testing.T) {
	asRootIdentityCLI(t)
	var stdout, stderr bytes.Buffer
	code := runIdentity(context.Background(), "/does/not/exist", []string{"users", "delete", "12345678-1234-4234-8234-123456789abc", "--revision", "7"}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "outcome may be unknown") || !strings.Contains(stderr.String(), "inspect current identity state") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestIdentityCLIRejectsUntrustedSuccessResponse(t *testing.T) {
	asRootIdentityCLI(t)
	socket := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"items":[],"credential":"leak"}`))
	}))
	var stdout, stderr bytes.Buffer
	if code := runIdentity(context.Background(), socket, []string{"users", "list"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid users response") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

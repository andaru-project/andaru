package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func replySocket(t *testing.T, handler http.Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return path
}

func TestCtlDoesNotPrintUntrustedReplies(t *testing.T) {
	for _, tc := range []struct {
		name, operation, body string
		status                int
	}{
		{"API failure", "links", `{"error":{"code":"INVENTORY_UNAVAILABLE","message":"Unavailable","details":[]}}`, 503},
		{"invalid JSON", "links", "not-json", 200},
		{"invalid UTF-8", "links", "{\"items\":[],\"note\":\"bad\xff\"}", 200},
		{"truncated JSON", "links", `{"items":[`, 200},
		{"null snapshot", "links", `null`, 200},
		{"null items", "links", `{"items":null}`, 200},
		{"wrong items shape", "links", `{"items":{}}`, 200},
		{"wrong contract envelope", "openapi", `{"items":[]}`, 200},
		{"empty contract version", "openapi", `{"openapi":"","paths":{}}`, 200},
		{"null contract paths", "openapi", `{"openapi":"3.1.1","paths":null}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			var out, diagnostics bytes.Buffer
			code := run(context.Background(), []string{"ctl", "--socket", path, tc.operation}, strings.NewReader(""), &out, &diagnostics)
			if code != 1 || out.Len() != 0 || diagnostics.Len() == 0 {
				t.Fatalf("failed response became successful/partial output: exit=%d stdout=%q stderr=%q", code, out.String(), diagnostics.String())
			}
		})
	}
}

func TestCtlRejectsOversizedReplyBeforePrinting(t *testing.T) {
	path := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[]}`)
		_, _ = io.WriteString(w, strings.Repeat(" ", 8*1024*1024))
	}))
	var out, diagnostics bytes.Buffer
	if code := run(context.Background(), []string{"ctl", "--socket", path, "links"}, strings.NewReader(""), &out, &diagnostics); code != 1 || out.Len() != 0 {
		t.Fatalf("oversize reply became success: exit=%d stdout bytes=%d", code, out.Len())
	}
}

func TestCtlDoesNotFollowRedirects(t *testing.T) {
	var requests atomic.Int32
	path := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	var out, diagnostics bytes.Buffer
	code := run(context.Background(), []string{"ctl", "--socket", path, "links"}, strings.NewReader(""), &out, &diagnostics)
	if code != 1 || out.Len() != 0 || requests.Load() != 1 {
		t.Fatalf("redirect escaped the selected API operation: exit=%d requests=%d stdout=%q", code, requests.Load(), out.String())
	}
}

func TestCtlTransportFailureHasNoSuccessOutput(t *testing.T) {
	var out, diagnostics bytes.Buffer
	path := filepath.Join(t.TempDir(), "missing.sock")
	code := run(context.Background(), []string{"ctl", "--socket", path, "links"}, strings.NewReader(""), &out, &diagnostics)
	if code != 1 || out.Len() != 0 || diagnostics.Len() == 0 {
		t.Fatalf("missing engine became success: exit=%d stdout=%q stderr=%q", code, out.String(), diagnostics.String())
	}
}

func TestEnvelopeAcceptsEmptyCollections(t *testing.T) {
	for operation, body := range map[string]string{
		"links":   `{"items":[]}`,
		"openapi": `{"openapi":"3.1.1","paths":{}}`,
	} {
		if !validEnvelope(operation, []byte(body)) {
			t.Fatalf("%s rejected a valid empty collection", operation)
		}
	}
}

func TestInvalidArgumentsAreUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil, {"unknown"}, {"engine", "extra"}, {"engine", "--socket", "relative"},
		{"ctl"}, {"ctl", "unknown"}, {"ctl", "links", "extra"}, {"ctl", "--socket", "relative", "links"},
		{"engine", "--state-dir", "relative"}, {"engine", "--state-dir", "/tmp/../tmp"},
		{"engine", "--state-dir"}, {"ctl", "--state-dir", "/tmp", "links"},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &diagnostics); code != 2 || out.Len() != 0 || diagnostics.Len() == 0 {
			t.Fatalf("%q: exit=%d stdout=%q stderr=%q", args, code, out.String(), diagnostics.String())
		}
	}
}

func TestCtlConfigurationUsageErrors(t *testing.T) {
	id := "zon_00000000-0000-4000-8000-000000000001"
	for _, args := range [][]string{
		{"config"}, {"config", "replace", "zones", id}, {"config", "delete", "--version", "*", "zones", id},
		{"config", "list", "unknown"}, {"config", "get", "zones", "../bad"}, {"versions", "list", "--limit", "101"},
		{"versions", "get", "1e3"}, {"config", "changes", "extra"},
	} {
		var out, diag bytes.Buffer
		argv := append([]string{"ctl", "--socket", filepath.Join(t.TempDir(), "missing.sock")}, args...)
		if code := run(context.Background(), argv, strings.NewReader("{}"), &out, &diag); code != 2 || out.Len() != 0 {
			t.Fatalf("usage %v exit=%d stdout=%s", args, code, out.String())
		}
	}
}

func TestCtlConfigurationReplyBoundary(t *testing.T) {
	path := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"state":"saved","savedVersion":"0","value":null,"unusedSecrets":[]}`)
	}))
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"ctl", "--socket", path, "config", "show"}, strings.NewReader(""), &out, &diag); code != 1 || out.Len() != 0 {
		t.Fatal("invalid saved snapshot became success")
	}
	input := strings.NewReader(strings.Repeat("x", 2*1024*1024))
	out.Reset()
	diag.Reset()
	if code := run(context.Background(), []string{"ctl", "--socket", filepath.Join(t.TempDir(), "missing.sock"), "config", "create", "zones"}, input, &out, &diag); code != 1 || out.Len() != 0 || input.Len() < 1024*1024-1 {
		t.Fatal("stdin was not bounded before transmission")
	}
}

func TestCtlPreviewUsageAndReplyBoundary(t *testing.T) {
	for _, args := range [][]string{
		{"preview"}, {"preview", "other"}, {"preview", "network", "extra"},
		{"preview", "network", "--saved-version", "x"}, {"preview", "network", "--saved-version", "01"}, {"preview", "network", "--version", "1"},
		// An unset shell variable must not silently preview the head.
		{"preview", "network", "--saved-version", ""}, {"preview", "network", "--saved-version="},
	} {
		var out, diag bytes.Buffer
		argv := append([]string{"ctl", "--socket", filepath.Join(t.TempDir(), "missing.sock")}, args...)
		if code := run(context.Background(), argv, strings.NewReader(""), &out, &diag); code != 2 || out.Len() != 0 {
			t.Fatalf("usage %v exit=%d stdout=%s", args, code, out.String())
		}
	}
	valid := `{"state":"preview","savedVersion":"3","realizable":true,"resolved":[],"files":[],"findings":[]}`
	for name, body := range map[string]string{
		"saved state passed off as a preview": `{"state":"saved","savedVersion":"3","realizable":true,"resolved":[],"files":[],"findings":[]}`,
		"null files":                          `{"state":"preview","savedVersion":"3","realizable":true,"resolved":[],"files":null,"findings":[]}`,
		"findings not a list":                 `{"state":"preview","savedVersion":"3","realizable":true,"resolved":[],"files":[],"findings":{}}`,
		"no verdict":                          `{"state":"preview","savedVersion":"3","resolved":[],"files":[],"findings":[]}`,
		"bad version":                         `{"state":"preview","savedVersion":"x","realizable":true,"resolved":[],"files":[],"findings":[]}`,
		"":                                    valid,
	} {
		var query string
		path := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RequestURI()
			_, _ = io.WriteString(w, body)
		}))
		var out, diag bytes.Buffer
		code := run(context.Background(), []string{"ctl", "--socket", path, "preview", "network", "--saved-version", "3"}, strings.NewReader(""), &out, &diag)
		if query != "/api/v1/preview/network?savedVersion=3" {
			t.Fatalf("wrong request: %s", query)
		}
		if name == "" {
			if code != 0 || strings.TrimSpace(out.String()) != valid {
				t.Fatalf("valid preview rejected: exit=%d %s %s", code, out.String(), diag.String())
			}
		} else if code != 1 || out.Len() != 0 {
			t.Fatalf("%s: became output: exit=%d %s", name, code, out.String())
		}
	}
}

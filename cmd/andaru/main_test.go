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
			code := run(context.Background(), []string{"ctl", "--socket", path, tc.operation}, &out, &diagnostics)
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
	if code := run(context.Background(), []string{"ctl", "--socket", path, "links"}, &out, &diagnostics); code != 1 || out.Len() != 0 {
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
	code := run(context.Background(), []string{"ctl", "--socket", path, "links"}, &out, &diagnostics)
	if code != 1 || out.Len() != 0 || requests.Load() != 1 {
		t.Fatalf("redirect escaped the selected API operation: exit=%d requests=%d stdout=%q", code, requests.Load(), out.String())
	}
}

func TestCtlTransportFailureHasNoSuccessOutput(t *testing.T) {
	var out, diagnostics bytes.Buffer
	path := filepath.Join(t.TempDir(), "missing.sock")
	code := run(context.Background(), []string{"ctl", "--socket", path, "links"}, &out, &diagnostics)
	if code != 1 || out.Len() != 0 || diagnostics.Len() == 0 {
		t.Fatalf("missing engine became success: exit=%d stdout=%q stderr=%q", code, out.String(), diagnostics.String())
	}
}

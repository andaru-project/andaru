package config

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"syscall"
	"testing"
)

type hostileError struct {
	msg   string
	code  int
	cause error
}

func (h hostileError) Error() string { return h.msg }
func (h hostileError) Code() int     { return h.code }
func (h hostileError) Unwrap() error { return h.cause }

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestDiagnosticLogsOnlySafeCauseFields(t *testing.T) {
	const sentinel = "SENTINEL-password-hunter2-/secret/path"
	buf := captureLog(t)
	f := diagnostic("load-config-scan", fmt.Errorf("wrap %s: %w", sentinel, hostileError{sentinel, 13, syscall.ENOSPC}))
	if f.Code != "STORE_UNAVAILABLE" {
		t.Fatalf("fault = %q", f.Code)
	}
	out := buf.String()
	if strings.Contains(out, sentinel) || strings.Contains(out, "hunter2") {
		t.Fatalf("raw error leaked: %s", out)
	}
	for _, want := range []string{"stage=load-config-scan", "sqlite_code=13", "errno=28"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}

func TestDiagnosticClassifiesContextAndNilCause(t *testing.T) {
	buf := captureLog(t)
	diagnostic("read-config-connect", fmt.Errorf("SENTINEL: %w", context.DeadlineExceeded))
	diagnostic("load-config-hash", nil)
	out := buf.String()
	if strings.Contains(out, "SENTINEL") {
		t.Fatalf("raw error leaked: %s", out)
	}
	for _, want := range []string{"stage=read-config-connect", "context=deadline", "stage=load-config-hash"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}

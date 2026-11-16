package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestWebCommandIsDefaultOffAndRequiresCompleteConfiguration(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--listen", "192.0.2.1:8443"},
		{"--listen", "192.0.2.1:8443", "--tls-cert", "/cert", "--tls-key", "/key", "--origin", "https://router.example", "--backend-socket", "/socket"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage: andaru-web") {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestWebCommandHelpAndCompleteFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "--backend-socket") || stderr.Len() != 0 {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	args := []string{
		"--listen", "192.0.2.1:8443", "--tls-cert", "/cert", "--tls-key", "/key",
		"--origin", "https://router.example:8443", "--backend-socket", "/run/andaru-web/engine.sock", "--assets-dir", "/usr/share/andaru/web",
	}
	if code := run(context.Background(), args, &stdout, &stderr); code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "usage: andaru-web") {
		t.Fatalf("complete flags were not accepted: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

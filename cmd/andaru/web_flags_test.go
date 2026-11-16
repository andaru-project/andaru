package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestEngineWebSocketFlagsAreExplicitAndAllOrNothing(t *testing.T) {
	for _, args := range [][]string{
		{"engine", "--web-socket", "/run/andaru-web/engine.sock"},
		{"engine", "--web-uid", "123"},
		{"engine", "--web-gid", "123"},
		{"engine", "--web-origin", "https://router.example"},
		{"engine", "--web-socket", "/run/andaru-web/engine.sock", "--web-uid", "123", "--web-gid", "123"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "supplied together") {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestRepeatedFlagsRejectEmptyValues(t *testing.T) {
	var values repeatedFlags
	if err := values.Set(""); err == nil {
		t.Fatal("empty repeated flag accepted")
	}
	if err := values.Set("https://router.example"); err != nil || values.String() != "https://router.example" {
		t.Fatalf("valid repeated flag=%q err=%v", values.String(), err)
	}
}

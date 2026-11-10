package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCtlBlockedStdinHonorsCancellation(t *testing.T) {
	reader, writer, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	defer writer.Close() // never written: the read blocks until cancellation
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	var out, diag bytes.Buffer
	go func() {
		code <- run(ctx, []string{"ctl", "--socket", filepath.Join(t.TempDir(), "missing.sock"), "config", "create", "zones"}, reader, &out, &diag)
	}()
	select {
	case <-code:
		t.Fatal("command returned before cancellation")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case c := <-code:
		if c != 1 || out.Len() != 0 {
			t.Fatalf("cancelled stdin read exit=%d stdout=%q", c, out.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked stdin read ignored context cancellation")
	}
}

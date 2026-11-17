package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/kept"
)

// keptTestRoot is the test seam. Production leaves it empty and posts to the engine.
var keptTestRoot string

func storeRecovery(ctx context.Context, socket, stateDir string, args []string, stdout, stderr io.Writer, load func() ([]byte, map[string]string, error)) int {
	if len(args) == 1 && args[0] == "store-recovery" {
		args = nil
	}
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: andaru ctl kept store-recovery")
		return 2
	}
	if load != nil {
		return writeTestRecovery(ctx, stateDir, stdout, stderr, load)
	}
	client, transport := unixHTTPClient(socket, 10*time.Second)
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/api/v1/kept/recovery", nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		fmt.Fprintln(stderr, strings.TrimSpace(string(msg)))
		return 1
	}
	fmt.Fprintln(stdout, "stored")
	return 0
}

func writeTestRecovery(ctx context.Context, stateDir string, stdout, stderr io.Writer, load func() ([]byte, map[string]string, error)) int {
	decl, secrets, err := load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	body, err := json.Marshal(struct {
		Declaration json.RawMessage   `json:"declaration"`
		Secrets     map[string]string `json:"secrets"`
	}{decl, secrets})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if keptTestRoot == "" {
		fmt.Fprintln(stderr, "kept test root is unset")
		return 1
	}
	store, err := kept.OpenIn(keptTestRoot, stateDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = store.Put(ctx, "recovery", body, true); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "stored")
	return 0
}

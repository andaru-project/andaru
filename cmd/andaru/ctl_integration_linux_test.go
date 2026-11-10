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

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/engine"
)

func TestCtlConfigurationRoundTrip(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("integration suite requires root")
	}
	root, e := os.MkdirTemp("/var/tmp", "andaru-cli-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "run", "engine.sock")
	state := filepath.Join(root, "state")
	var cancel context.CancelFunc
	var done chan error
	start := func() {
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		done = make(chan error, 1)
		finished := done
		go func() { finished <- engine.Serve(ctx, socket, state) }()
		ready, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		for {
			var out, diag bytes.Buffer
			code := run(ready, []string{"ctl", "--socket", socket, "config", "show"}, strings.NewReader(""), &out, &diag)
			if code == 0 {
				return
			}
			if ready.Err() != nil {
				t.Fatalf("CLI readiness failed: %s", diag.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	stop := func() {
		if done == nil {
			return
		}
		cancel()
		finished := done
		done = nil
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if done != nil {
			cancel()
			<-done
		}
	}()
	start()
	call := func(input string, args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		argv := append([]string{"ctl", "--socket", socket}, args...)
		if code := run(context.Background(), argv, strings.NewReader(input), &out, &diag); code != 0 || diag.Len() != 0 {
			t.Fatalf("CLI %v exit=%d diagnostic=%s", args, code, diag.String())
		}
		if !json.Valid(out.Bytes()) || bytes.Contains(out.Bytes(), []byte("TEST_ONLY_CLI")) {
			t.Fatal("CLI emitted invalid JSON or secret")
		}
		return out.Bytes()
	}
	object := func(body []byte) config.ObjectView {
		t.Helper()
		var v config.ObjectView
		if e := json.Unmarshal(body, &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	identifier := func(v config.ObjectView) string {
		t.Helper()
		var x struct {
			ID string `json:"id"`
		}
		if e := json.Unmarshal(v.Value, &x); e != nil {
			t.Fatal(e)
		}
		return x.ID
	}
	zone := object(call(`{"name":"lan"}`, "config", "create", "zones"))
	zid := identifier(zone)
	port := object(call(`{"name":"port","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01"}`, "config", "create", "links"))
	pid := identifier(port)
	call(fmt.Sprintf(`{"name":"lan","link":%q,"zone":%q,"addressing":{"ipv4":{"mode":"static","addresses":["192.0.2.7/24"]},"ipv6":{"mode":"none"}}}`, pid, zid), "config", "create", "segments")
	edited := object(call(`{"name":"renamed"}`, "config", "replace", "--version", string(zone.Version), "zones", zid))
	var out, diag bytes.Buffer
	if code := run(context.Background(), []string{"ctl", "--socket", socket, "config", "replace", "--version", string(zone.Version), "zones", zid}, strings.NewReader(`{"name":"lost"}`), &out, &diag); code != 1 || out.Len() != 0 {
		t.Fatal("stale CLI write became success")
	}
	call(fmt.Sprintf(`{"name":"ppp","kind":"pppoe","mtu":1492,"enabled":true,"parent":%q,"username":"TEST_ONLY","password":{"secret":"WAN_PASSWORD","set":"TEST_ONLY_CLI","expectedVersion":"0"}}`, pid), "config", "create", "links")
	call(fmt.Sprintf(`{"operations":[{"op":"replace","collection":"zones","id":%q,"expectedVersion":%q,"value":{"name":"batch-name"}}]}`, zid, edited.Version), "config", "changes")
	call("", "versions", "list", "--limit", "2")
	old := call("", "versions", "get", "1")
	if !bytes.Contains(old, []byte(`"name":"lan"`)) {
		t.Fatal("history did not retain old declaration")
	}
	stop()
	start()
	current := object(call("", "config", "get", "zones", zid))
	if !bytes.Contains(current.Value, []byte(`"name":"batch-name"`)) {
		t.Fatal("restart lost saved edit")
	}
	operations := []map[string]any{}
	for _, collection := range []string{"links", "segments", "zones"} {
		raw := call("", "config", "list", collection)
		var list config.CollectionView
		if e := json.Unmarshal(raw, &list); e != nil {
			t.Fatal(e)
		}
		for _, v := range list.Items {
			operations = append(operations, map[string]any{"op": "delete", "collection": collection, "id": identifier(v), "expectedVersion": v.Version})
		}
	}
	payload, _ := json.Marshal(map[string]any{"operations": operations})
	call(string(payload), "config", "changes")
	raw := call("", "config", "show")
	var snapshot struct {
		Value struct{ Links, Segments, Zones []json.RawMessage } `json:"value"`
	}
	if e := json.Unmarshal(raw, &snapshot); e != nil || len(snapshot.Value.Links) != 0 || len(snapshot.Value.Segments) != 0 || len(snapshot.Value.Zones) != 0 {
		t.Fatal("cleanup did not leave an empty saved topology")
	}
}

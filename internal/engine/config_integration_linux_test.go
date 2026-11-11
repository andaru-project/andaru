//go:build integration

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func configurationFixture(t *testing.T) (http.Handler, *config.Store) {
	t.Helper()
	requireRoot(t)
	s, e := config.Open(context.Background(), filepath.Join(privateTestDir(t), "state"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return newHandler(host.ReadLinks, newSavedHandler(s, host.ReadLinks, host.ReadRealizerFacts, http.NotFoundHandler())), s
}
func configCall(h http.Handler, method, path, body, tag string) *httptest.ResponseRecorder {
	r := rootRequest(method, path)
	r.Body = io.NopCloser(strings.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", "application/json")
	if tag != "" {
		r.Header.Set("If-Match", tag)
	}
	r.Header.Set("X-Actor", "forged")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func apiObject(t *testing.T, w *httptest.ResponseRecorder) config.ObjectView {
	t.Helper()
	var v config.ObjectView
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func valueID(t *testing.T, v config.ObjectView) string {
	t.Helper()
	var m map[string]any
	if e := json.Unmarshal(v.Value, &m); e != nil {
		t.Fatal(e)
	}
	id, ok := m["id"].(string)
	if !ok {
		t.Fatal("no generated ID")
	}
	return id
}
func TestConfigurationHTTPContract(t *testing.T) {
	h, _ := configurationFixture(t)
	created := configCall(h, "POST", "/api/v1/config/zones", `{"name":"lan"}`, "")
	if created.Code != 201 || created.Header().Get("ETag") != "" || created.Header().Get("Andaru-Saved-Version") != "1" {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	v := apiObject(t, created)
	id := valueID(t, v)
	path := "/api/v1/config/zones/" + id
	read := configCall(h, "GET", path, "", "")
	tag := `"` + id + `.1"`
	if read.Code != 200 || read.Header().Get("ETag") != tag {
		t.Fatalf("object: %d %v", read.Code, read.Header())
	}
	updated := configCall(h, "PUT", path, `{"name":"renamed"}`, tag)
	if updated.Code != 200 || updated.Header().Get("ETag") != "" {
		t.Fatalf("update: %d %s", updated.Code, updated.Body)
	}
	newtag := `"` + id + `.2"`
	noop := configCall(h, "PUT", path, `{"name":"renamed"}`, newtag)
	if noop.Code != 200 || noop.Header().Get("Andaru-Saved-Version") != "2" {
		t.Fatal("no-op published a version")
	}
	assertAPIError(t, configCall(h, "PUT", path, `{"name":"stale"}`, tag), 412, "PRECONDITION_FAILED")
	assertAPIError(t, configCall(h, "PUT", path, `{"name":"missing"}`, ""), 428, "PRECONDITION_REQUIRED")
	assertAPIError(t, configCall(h, "PUT", path, `{`, ""), 400, "INVALID_REQUEST")
	assertAPIError(t, configCall(h, "PUT", path, strings.Repeat(" ", 1024*1024+1), ""), 413, "REQUEST_TOO_LARGE")
	assertAPIError(t, configCall(h, "PUT", path, `{"name":"blind"}`, "*"), 400, "INVALID_REQUEST")
	assertAPIError(t, configCall(h, "POST", "/api/v1/config/zones", `{"name":"renamed"}`, ""), 422, "VALIDATION_FAILED")
	history := configCall(h, "GET", "/api/v1/versions?limit=2", "", "")
	var hv config.HistoryView
	if e := json.Unmarshal(history.Body.Bytes(), &hv); e != nil || len(hv.Items) != 2 || hv.Items[0].ID != "2" || hv.Items[0].Actor != "unix:uid:0" {
		t.Fatalf("history: %s %v", history.Body, e)
	}
	head := configCall(h, "HEAD", path, "", "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != newtag {
		t.Fatal("HEAD contract")
	}
	assertAPIError(t, configCall(h, "PATCH", path, `{}`, ""), 405, "METHOD_NOT_ALLOWED")
	contract := configCall(h, "GET", "/api/v1/openapi.json", "", "")
	var document struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if e := json.Unmarshal(contract.Body.Bytes(), &document); e != nil {
		t.Fatal(e)
	}
	expected := map[string][]string{"/api/v1/status/links": {"get", "head"}, "/api/v1/openapi.json": {"get", "head"}, "/api/v1/schema": {"get", "head"}, "/api/v1/config": {"get", "head"}, "/api/v1/config/{collection}": {"get", "head", "post"}, "/api/v1/config/{collection}/{id}": {"get", "head", "put", "delete"}, "/api/v1/config/changes": {"post"}, "/api/v1/versions": {"get", "head"}, "/api/v1/versions/{id}": {"get", "head"}, "/api/v1/preview/network": {"get", "head"},
		"/api/v1/applies": {"get", "head", "post"}, "/api/v1/applies/{id}": {"get", "head"}, "/api/v1/applies/{id}/confirmation": {"post"}, "/api/v1/applies/{id}/rollback": {"post"}}
	if len(document.Paths) != len(expected) {
		t.Fatalf("unimplemented or missing contract paths: %d", len(document.Paths))
	}
	for p, methods := range expected {
		for _, m := range methods {
			if document.Paths[p][m] == nil {
				t.Fatalf("missing contract %s %s", p, m)
			}
		}
	}
	rawSchema := configCall(h, "GET", "/api/v1/schema", "", "")
	instance, e := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema.Body.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	compiler.AssertFormat()
	if e = compiler.AddResource("https://andaru.invalid/api/v1/schema", instance); e != nil {
		t.Fatal(e)
	}
	for suffix, reply := range map[string]*httptest.ResponseRecorder{"snapshotView": configCall(h, "GET", "/api/v1/config", "", ""), "collectionView": configCall(h, "GET", "/api/v1/config/zones", "", ""), "objectView": read} {
		sch, e := compiler.Compile("https://andaru.invalid/api/v1/schema#/$defs/" + suffix)
		if e != nil {
			t.Fatal(e)
		}
		v, e := jsonschema.UnmarshalJSON(bytes.NewReader(reply.Body.Bytes()))
		if e != nil {
			t.Fatal(e)
		}
		if e = sch.Validate(v); e != nil {
			t.Fatalf("served %s violated schema: %v", suffix, e)
		}
	}
	deleted := configCall(h, "DELETE", path, "", newtag)
	if deleted.Code != 200 {
		t.Fatal(deleted.Body.String())
	}
	assertAPIError(t, configCall(h, "GET", path, "", ""), 404, "NOT_FOUND")
	assertAPIError(t, configCall(h, "DELETE", path, "", newtag), 412, "PRECONDITION_FAILED")
}

func TestConfigurationHTTPPreconditionEdges(t *testing.T) {
	h, _ := configurationFixture(t)
	id := valueID(t, apiObject(t, configCall(h, "POST", "/api/v1/config/zones", `{"name":"A"}`, "")))
	path := "/api/v1/config/zones/" + id
	original := `"` + id + `.1"`
	if w := configCall(h, "PUT", path, `{"name":"B"}`, original); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := configCall(h, "PUT", path, `{"name":"A"}`, `"`+id+`.2"`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Content is back to the original, but the original version is stale.
	assertAPIError(t, configCall(h, "PUT", path, `{"name":"A"}`, original), 412, "PRECONDITION_FAILED")
	assertAPIError(t, configCall(h, "DELETE", path, "", original), 412, "PRECONDITION_FAILED")

	for _, repeated := range [][]string{{`"` + id + `.3"`, `"` + id + `.3"`}, {`"` + id + `.3"`, ""}} {
		r := rootRequest("PUT", path)
		r.Body = io.NopCloser(strings.NewReader(`{"name":"C"}`))
		for _, tag := range repeated {
			r.Header.Add("If-Match", tag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertAPIError(t, w, 400, "INVALID_REQUEST")
	}
	if w := configCall(h, "GET", path, "", ""); !strings.Contains(w.Body.String(), `"name":"A"`) {
		t.Fatal("rejected write changed saved state")
	}

	missing := "zon_00000000-0000-4000-8000-000000000001"
	batch := fmt.Sprintf(`{"operations":[{"op":"replace","collection":"zones","id":%q,"expectedVersion":"1","value":{"name":"stale"}},{"op":"replace","collection":"zones","id":%q,"expectedVersion":"1","value":{"name":"gone"}}]}`, id, missing)
	w := configCall(h, "POST", "/api/v1/config/changes", batch, "")
	assertAPIError(t, w, 404, "NOT_FOUND")
	if !strings.Contains(w.Body.String(), "/operations/1/id") {
		t.Fatalf("404 did not point at the missing operation: %s", w.Body)
	}
}

func TestConfigurationHTTPSecretNameSchemaFailureIsRedacted(t *testing.T) {
	h, _ := configurationFixture(t)
	const sentinel = "TEST_ONLY-BAD-NAME-SENTINEL"
	link := `{"name":"ppp","kind":"pppoe","mtu":1492,"enabled":true,"parent":"lnk_00000000-0000-4000-8000-000000000001","username":"TEST_ONLY","password":{"secret":"` + sentinel + `","set":"` + sentinel + `","expectedVersion":"0"}}`
	for path, body := range map[string]string{
		"/api/v1/config/changes": `{"operations":[{"op":"create","collection":"links","value":` + link + `}]}`,
		"/api/v1/config/links":   link,
	} {
		w := configCall(h, "POST", path, body, "")
		assertAPIError(t, w, 422, "VALIDATION_FAILED")
		if strings.Contains(w.Body.String(), sentinel) {
			t.Fatalf("%s failure exposed secret material: %s", path, w.Body)
		}
	}
}

func TestConfigurationHTTPConcurrencyAndSecrets(t *testing.T) {
	h, _ := configurationFixture(t)
	raw := `{"operations":[{"op":"create","collection":"links","localId":"port","value":{"name":"port","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01"}},{"op":"create","collection":"links","localId":"ppp","value":{"name":"ppp","kind":"pppoe","mtu":1492,"enabled":true,"parent":"@port","username":"TEST_ONLY","password":{"secret":"WAN_PASSWORD","set":"TEST_ONLY_HTTP_SECRET","expectedVersion":"0"}}}]}`
	saved := configCall(h, "POST", "/api/v1/config/changes", raw, "")
	if saved.Code != 200 || strings.Contains(saved.Body.String(), "TEST_ONLY_HTTP_SECRET") {
		t.Fatalf("batch: %d %s", saved.Code, saved.Body)
	}
	var result config.SaveResult
	if e := json.Unmarshal(saved.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	id := result.Created["ppp"]
	path := "/api/v1/config/links/" + id
	read := configCall(h, "GET", path, "", "")
	v := apiObject(t, read)
	var body map[string]any
	if e := json.Unmarshal(v.Value, &body); e != nil {
		t.Fatal(e)
	}
	body["password"] = map[string]any{"secret": "WAN_PASSWORD", "set": "\x00TEST_ONLY_DO_NOT_LOG", "expectedVersion": "1"}
	invalid, _ := json.Marshal(body)
	failed := configCall(h, "PUT", path, string(invalid), read.Header().Get("ETag"))
	if failed.Code != 422 || strings.Contains(failed.Body.String(), "TEST_ONLY_DO_NOT_LOG") {
		t.Fatalf("unsafe secret failure: %d %s", failed.Code, failed.Body)
	}
	for _, p := range []string{path, "/api/v1/config", "/api/v1/versions/1"} {
		reply := configCall(h, "GET", p, "", "")
		if reply.Code != 200 || strings.Contains(reply.Body.String(), "TEST_ONLY_HTTP_SECRET") {
			t.Fatal("read exposed secret or lost unchanged saved state")
		}
	}
	results := make(chan int, 2)
	var requests sync.WaitGroup
	for i := range 2 {
		requests.Add(1)
		go func(i int) {
			defer requests.Done()
			var candidate map[string]any
			if err := json.Unmarshal(v.Value, &candidate); err != nil {
				results <- 0
				return
			}
			candidate["name"] = fmt.Sprintf("concurrent-%d", i)
			candidate["password"] = map[string]any{"secret": "WAN_PASSWORD"}
			payload, _ := json.Marshal(candidate)
			results <- configCall(h, "PUT", path, string(payload), read.Header().Get("ETag")).Code
		}(i)
	}
	requests.Wait()
	close(results)
	wins, stale := 0, 0
	for status := range results {
		if status == 200 {
			wins++
		} else if status == 412 {
			stale++
		}
	}
	if wins != 1 || stale != 1 {
		t.Fatalf("HTTP race wins=%d stale=%d", wins, stale)
	}
	second := configCall(h, "POST", "/api/v1/config/links", `{"name":"second","kind":"pppoe","mtu":1492,"enabled":true,"parent":"`+result.Created["port"]+`","username":"TEST_ONLY","password":{"secret":"WAN_PASSWORD"}}`, "")
	if second.Code != 201 {
		t.Fatal(second.Body.String())
	}
	secondID := valueID(t, apiObject(t, second))
	current := configCall(h, "GET", path, "", "")
	var candidate map[string]any
	currentView := apiObject(t, current)
	if err := json.Unmarshal(currentView.Value, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate["password"] = map[string]any{"secret": "WAN_PASSWORD", "set": "TEST_ONLY_SHARED_NEW", "expectedVersion": "1"}
	payload, _ := json.Marshal(candidate)
	changed := configCall(h, "PUT", path, string(payload), current.Header().Get("ETag"))
	if changed.Code != 200 {
		t.Fatal(changed.Body.String())
	}
	for _, target := range []string{id, secondID} {
		ref := configCall(h, "GET", "/api/v1/config/links/"+target, "", "")
		view := apiObject(t, ref)
		if string(view.Version) != changed.Header().Get("Andaru-Saved-Version") || strings.Contains(ref.Body.String(), "TEST_ONLY_SHARED_NEW") {
			t.Fatal("shared HTTP referrer did not version safely")
		}
	}
	missing := rootRequest("POST", "/api/v1/config/links")
	missing = missing.WithContext(context.Background())
	missing.Body = io.NopCloser(strings.NewReader("bad"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, missing)
	assertAPIError(t, w, 403, "FORBIDDEN")
}

func TestEngineStoreFailureExit(t *testing.T) {
	requireRoot(t)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("unshare", "--mount", "--propagation", "private", exe, "--engine-enosp")
	cmd.Env = os.Environ()
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("actual engine fencing proof: %v\n%s", e, out)
	}
}
func engineENOSP() error {
	bin := os.Getenv("ANDARU_TEST_BINARY")
	if !filepath.IsAbs(bin) {
		return errors.New("ANDARU_TEST_BINARY is required")
	}
	root, e := os.MkdirTemp("/var/tmp", "andaru-api-full-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(root)
	disk := filepath.Join(root, "disk")
	if e = os.Mkdir(disk, 0700); e != nil {
		return e
	}
	if e = syscall.Mount("tmpfs", disk, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=8m,mode=0700"); e != nil {
		return e
	}
	defer syscall.Unmount(disk, 0)
	sock := filepath.Join(root, "api", "engine.sock")
	state := filepath.Join(disk, "state")
	var diagnostics bytes.Buffer
	start := func() (*exec.Cmd, error) {
		cmd := exec.Command(bin, "engine", "--socket", sock, "--state-dir", state)
		cmd.Stderr = &diagnostics
		if e := cmd.Start(); e != nil {
			return nil, e
		}
		return cmd, nil
	}
	child, e := start()
	if e != nil {
		return e
	}
	defer func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	}()
	client := unixTestClient(sock)
	ready := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			r, _ := http.NewRequestWithContext(ctx, "GET", "http://unix/api/v1/config", nil)
			response, e := client.Do(r)
			if e == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode == 200 {
					return nil
				}
			}
			if ctx.Err() != nil {
				return fmt.Errorf("engine not ready: %s", diagnostics.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if e = ready(); e != nil {
		return e
	}
	filler := filepath.Join(disk, "filler")
	f, e := os.Create(filler)
	if e != nil {
		return e
	}
	chunk := make([]byte, 65536)
	for {
		if _, e = f.Write(chunk); e != nil {
			break
		}
	}
	f.Close()
	request, _ := http.NewRequest("POST", "http://unix/api/v1/config/zones", strings.NewReader(`{"name":"full"}`))
	response, e := client.Do(request)
	if e != nil {
		return fmt.Errorf("failing response was lost: %w", e)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 503 {
		return fmt.Errorf("full store returned %d: %s", response.StatusCode, body)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case e = <-done:
		var status *exec.ExitError
		if !errors.As(e, &status) || status.ExitCode() != 1 {
			return fmt.Errorf("fenced engine did not exit 1: %v", e)
		}
	case <-time.After(5 * time.Second):
		child.Process.Kill()
		<-done
		return errors.New("fenced engine failed to drain")
	}
	if _, e = os.Lstat(sock); !os.IsNotExist(e) {
		return errors.New("fenced engine left its socket")
	}
	os.Remove(filler)
	diagnostics.Reset()
	child, e = start()
	if e != nil {
		return e
	}
	if e = ready(); e != nil {
		return e
	}
	child.Process.Signal(syscall.SIGTERM)
	if e = child.Wait(); e != nil {
		return e
	}
	fmt.Println("engine 503, drained exit 1, complete reopen PASS")
	return nil
}

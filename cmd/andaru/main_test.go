package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/andaru-project/andaru/internal/config"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andaru-project/andaru/internal/generation"
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

func TestRealizeUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"realize"}, {"realize", "unknown"}, {"realize", "activate"}, {"realize", "activate", "0"},
		{"realize", "activate", "01"}, {"realize", "activate", "abc"}, {"realize", "activate", "1", "extra"},
		{"realize", "rollback", "3"}, {"realize", "confirm"}, {"realize", "boot", "1"},
		{"realize", "--state-dir", "relative", "boot"},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &diagnostics); code != 2 || out.Len() != 0 || diagnostics.Len() == 0 {
			t.Fatalf("%q: exit=%d stdout=%q stderr=%q", args, code, out.String(), diagnostics.String())
		}
	}
	var diagnostics bytes.Buffer
	run(context.Background(), nil, strings.NewReader(""), io.Discard, &diagnostics)
	if !strings.Contains(diagnostics.String(), "andaru realize") {
		t.Fatalf("usage omits realize: %q", diagnostics.String())
	}
}

type fakeVerbs struct {
	err   error
	calls []string
}

func (f *fakeVerbs) Activate(_ context.Context, id int) error {
	f.calls = append(f.calls, fmt.Sprint("activate ", id))
	return f.err
}
func (f *fakeVerbs) Rollback(_ context.Context, id int, reason string) error {
	f.calls = append(f.calls, fmt.Sprint("rollback ", id, " ", reason))
	return f.err
}
func (f *fakeVerbs) Confirm(_ context.Context, id int) error {
	f.calls = append(f.calls, fmt.Sprint("confirm ", id))
	return f.err
}
func (f *fakeVerbs) Boot(context.Context) error { f.calls = append(f.calls, "boot"); return f.err }
func (f *fakeVerbs) SyncLeases(context.Context) error {
	f.calls = append(f.calls, "leases")
	return f.err
}

func TestRealizeExitCodes(t *testing.T) {
	for _, row := range []struct {
		args []string
		err  error
		call string
		code int
	}{
		{[]string{"activate", "7"}, nil, "activate 7", 0},
		{[]string{"activate", "7"}, errors.New("verification failed"), "activate 7", 1},
		{[]string{"confirm", "7"}, nil, "confirm 7", 0},
		// A timer firing after a confirmation must not leave a failed unit behind.
		{[]string{"rollback", "7", "deadline"}, fmt.Errorf("roll back 7: %w", generation.ErrWrongState), "rollback 7 deadline", 0},
		{[]string{"rollback", "7", "requested"}, errors.New("disk"), "rollback 7 requested", 1},
		// Boot must never hold up sysinit.target.
		{[]string{"boot"}, errors.New("nothing usable"), "boot", 0},
		// A failed lease sync shows on its unit; the path unit runs it again at the next lease event.
		{[]string{"leases"}, errors.New("Kea not answering"), "leases", 1},
	} {
		verbs := &fakeVerbs{err: row.err}
		var diagnostics bytes.Buffer
		var dir string
		code := realizeCommand(context.Background(), append([]string{"--state-dir", "/srv/state"}, row.args...), &diagnostics, func(stateDir string) (realizeVerbs, error) {
			dir = stateDir
			return verbs, nil
		})
		if code != row.code || !slices.Equal(verbs.calls, []string{row.call}) || dir != "/srv/state" || (row.err != nil) != (diagnostics.Len() > 0) {
			t.Fatalf("%q: exit=%d calls=%q dir=%q stderr=%q", row.args, code, verbs.calls, dir, diagnostics.String())
		}
	}
	code := realizeCommand(context.Background(), []string{"boot"}, io.Discard, func(string) (realizeVerbs, error) { return nil, errors.New("not root") })
	if code != 1 {
		t.Fatalf("a refused privilege check exited %d", code)
	}
}

func TestCtlApplyUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"apply"}, {"apply", "firewall", "--saved-version", "7"}, {"apply", "network"},
		// An unset shell variable must not apply anything.
		{"apply", "network", "--saved-version", ""}, {"apply", "network", "--saved-version", "07"},
		{"apply", "network", "--saved-version", "7", "--confirm-timeout", "abc"}, {"apply", "network", "--saved-version", "7", "extra"},
		{"applies"}, {"applies", "get"}, {"applies", "confirm", "x"}, {"applies", "confirm", "0"}, {"applies", "rollback", "01"},
		{"applies", "list", "extra"}, {"applies", "unknown", "1"}, {"applies", "confirm", "1", "extra"},
	} {
		var out, diag bytes.Buffer
		argv := append([]string{"ctl", "--socket", filepath.Join(t.TempDir(), "missing.sock")}, args...)
		if code := run(context.Background(), argv, strings.NewReader(""), &out, &diag); code != 2 || out.Len() != 0 {
			t.Fatalf("usage %v exit=%d stdout=%s", args, code, out.String())
		}
	}
}

func TestCtlApplyAndApplies(t *testing.T) {
	const awaiting = `{"id":"3","savedVersion":"7","takeover":true,"state":"awaiting_confirmation","previous":null,"deadline":"2026-11-11T12:02:00Z","reason":"","createdAt":"2026-11-11T12:00:00Z","confirmTimeoutSeconds":60,"files":[]}`
	const failed = `{"id":"3","savedVersion":"7","takeover":true,"state":"failed","previous":null,"reason":"eth0 lost its IPv4 address","createdAt":"2026-11-11T12:00:00Z","confirmTimeoutSeconds":60,"files":[]}`
	const pending = `{"id":"3","savedVersion":"7","takeover":true,"state":"confirmed","previous":null,"reason":"takeover not yet permanent: disk; completes at the next boot","createdAt":"2026-11-11T12:00:00Z","confirmTimeoutSeconds":60,"files":[]}`
	const list = `{"pointers":{"confirmed":null,"lastKnownGood":null,"pending":"3","live":"3"},"items":[]}`
	for _, row := range []struct {
		name         string
		args         []string
		reply        string
		method, path string
		body         string
		code         int
		stdout       bool
		stderrHas    string
	}{
		{"apply", []string{"apply", "network", "--saved-version", "7", "--takeover", "--confirm-timeout", "60"}, awaiting,
			"POST", "/api/v1/applies", `{"savedVersion":"7","takeover":true,"confirmTimeoutSeconds":60}`, 0, true,
			"Live until 2026-11-11T12:02:00Z. From a NEW session, run: andaru ctl applies confirm 3\n"},
		{"apply defaults", []string{"apply", "network", "--saved-version", "7"}, awaiting,
			"POST", "/api/v1/applies", `{"savedVersion":"7","takeover":false,"confirmTimeoutSeconds":120}`, 0, true, "applies confirm 3"},
		{"apply failed", []string{"apply", "network", "--saved-version", "7"}, failed,
			"POST", "/api/v1/applies", `{"savedVersion":"7","takeover":false,"confirmTimeoutSeconds":120}`, 1, true, "apply failed: eth0 lost its IPv4 address\n"},
		{"confirm", []string{"applies", "confirm", "3"}, strings.Replace(awaiting, "awaiting_confirmation", "confirmed", 1), "POST", "/api/v1/applies/3/confirmation", "", 0, true, ""},
		{"confirm not yet permanent", []string{"applies", "confirm", "3"}, pending, "POST", "/api/v1/applies/3/confirmation", "", 0, true, "completes at the next boot"},
		{"rollback", []string{"applies", "rollback", "3"}, strings.Replace(awaiting, "awaiting_confirmation", "rolled_back", 1), "POST", "/api/v1/applies/3/rollback", "", 0, true, ""},
		{"get", []string{"applies", "get", "3"}, awaiting, "GET", "/api/v1/applies/3", "", 0, true, ""},
		{"list", []string{"applies", "list"}, list, "GET", "/api/v1/applies", "", 0, true, ""},
		{"list with null items", []string{"applies", "list"}, `{"pointers":{},"items":null}`, "GET", "/api/v1/applies", "", 1, false, "invalid"},
		{"apply without a state", []string{"applies", "get", "3"}, `{"id":"3","files":[]}`, "GET", "/api/v1/applies/3", "", 1, false, "invalid"},
		{"apply with null files", []string{"applies", "get", "3"}, strings.Replace(awaiting, `"files":[]`, `"files":null`, 1), "GET", "/api/v1/applies/3", "", 1, false, "invalid"},
	} {
		t.Run(row.name, func(t *testing.T) {
			var method, path, body string
			socket := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				method, path, body = r.Method, r.URL.RequestURI(), string(data)
				_, _ = io.WriteString(w, row.reply)
			}))
			var out, diag bytes.Buffer
			code := run(context.Background(), append([]string{"ctl", "--socket", socket}, row.args...), strings.NewReader("ignored"), &out, &diag)
			if method != row.method || path != row.path || body != row.body {
				t.Fatalf("request = %s %s %s", method, path, body)
			}
			if code != row.code || (out.Len() > 0) != row.stdout || !strings.Contains(diag.String(), row.stderrHas) || (row.stderrHas == "" && diag.Len() != 0) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), diag.String())
			}
			if row.stdout && strings.TrimSpace(out.String()) != row.reply {
				t.Fatalf("stdout = %q", out.String())
			}
		})
	}
	var out, diag bytes.Buffer
	missing := filepath.Join(t.TempDir(), "missing.sock")
	if code := run(context.Background(), []string{"ctl", "--socket", missing, "applies", "confirm", "3"}, strings.NewReader(""), &out, &diag); code != 1 || !strings.Contains(diag.String(), "andaru ctl applies list") {
		t.Fatalf("exit=%d stderr=%q", code, diag.String())
	}
}

func TestConfigOrder(t *testing.T) {
	a, b := "rul_00000001-0000-4000-8000-000000000001", "rul_00000002-0000-4000-8000-000000000002"
	req, err := ctlOperation([]string{"config", "order", "forward", "--version", "7", b, a}, io.Discard)
	if err != nil || req.method != "PUT" || req.path != "/api/v1/config/policy/forward/order" || req.tag != `"order.7"` || string(req.payload) != `["`+b+`","`+a+`"]` {
		t.Fatalf("request %+v %v", req, err)
	}
	for _, args := range [][]string{
		{"config", "order", "sideways", "--version", "7", a},
		{"config", "order", "forward", a},
		{"config", "order", "forward", "--version", "7", "zon_00000001-0000-4000-8000-000000000001"},
	} {
		if _, err := ctlOperation(args, io.Discard); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

func TestConfigFilterCollections(t *testing.T) {
	for args, want := range map[string]ctlRequest{
		"config list policy/input":                                     {method: "GET", path: "/api/v1/config/policy/input", kind: "collection"},
		"config create addresses":                                      {method: "POST", path: "/api/v1/config/addresses", kind: "object"},
		"config get management":                                        {method: "GET", path: "/api/v1/config/management", kind: "singleton"},
		"config replace --version 0 dns":                               {method: "PUT", path: "/api/v1/config/dns", kind: "singleton", tag: `"dns.0"`},
		"config create dhcp":                                           {method: "POST", path: "/api/v1/config/dhcp", kind: "object"},
		"config get dhcp dhp_00000001-0000-4000-8000-000000000001":     {method: "GET", path: "/api/v1/config/dhcp/dhp_00000001-0000-4000-8000-000000000001", kind: "object"},
		"config replace --version 0 nat/outbound":                      {method: "PUT", path: "/api/v1/config/nat/outbound", kind: "singleton", tag: `"nat.outbound.0"`},
		"config get services svc_00000001-0000-4000-8000-000000000001": {method: "GET", path: "/api/v1/config/services/svc_00000001-0000-4000-8000-000000000001", kind: "object"},
	} {
		req, err := ctlOperation(strings.Fields(args), io.Discard)
		if err != nil || req.method != want.method || req.path != want.path || req.kind != want.kind || req.tag != want.tag {
			t.Fatalf("%s: %+v %v", args, req, err)
		}
	}
}

func TestSaveEnvelopeAcceptsFilterResults(t *testing.T) {
	rule := `{"id":"rul_00000001-0000-4000-8000-000000000001","name":"r","action":"accept"}`
	save := `{"state":"saved","savedVersion":"4","created":{},"items":[
		{"collection":"policy.forward","id":"rul_00000001-0000-4000-8000-000000000001","value":` + rule + `,"version":"4"},
		{"collection":"policy.forward","id":"policy.forward","version":"4"},
		{"collection":"management","id":"management","value":{"segment":"seg_00000001-0000-4000-8000-000000000001"},"version":"4"},
		{"collection":"nat.outbound","id":"nat.outbound","deleted":true}]}`
	if !validEnvelope("save", []byte(save)) {
		t.Fatal("save reply with filter items rejected")
	}
	for name, bad := range map[string]string{
		"slash collection":   `{"collection":"policy/forward","id":"policy.forward","version":"4"}`,
		"singleton bad id":   `{"collection":"management","id":"mgmt","value":{},"version":"4"}`,
		"order with no vers": `{"collection":"policy.input","id":"policy.input"}`,
	} {
		if validEnvelope("save", []byte(`{"state":"saved","savedVersion":"4","created":{},"items":[`+bad+`]}`)) {
			t.Fatalf("%s accepted", name)
		}
	}
	order := `{"state":"saved","savedVersion":"5","version":"5","order":["rul_00000001-0000-4000-8000-000000000001"]}`
	if !validEnvelope("order", []byte(order)) {
		t.Fatal("order reply rejected")
	}
}

func TestConfigNATLists(t *testing.T) {
	a, b := "pfw_00000001-0000-4000-8000-000000000001", "pfw_00000002-0000-4000-8000-000000000002"
	for args, want := range map[string]ctlRequest{
		"config list nat/port-forwards":     {method: "GET", path: "/api/v1/config/nat/port-forwards", kind: "collection"},
		"config create nat/one-to-one":      {method: "POST", path: "/api/v1/config/nat/one-to-one", kind: "object"},
		"config get nat/port-forwards " + a: {method: "GET", path: "/api/v1/config/nat/port-forwards/" + a, kind: "object"},
	} {
		req, err := ctlOperation(strings.Fields(args), io.Discard)
		if err != nil || req.method != want.method || req.path != want.path || req.kind != want.kind {
			t.Fatalf("%s: %+v %v", args, req, err)
		}
	}
	req, err := ctlOperation([]string{"config", "order", "nat/port-forwards", "--version", "7", b, a}, io.Discard)
	if err != nil || req.path != "/api/v1/config/nat/port-forwards/order" || string(req.payload) != `["`+b+`","`+a+`"]` {
		t.Fatalf("order %+v %v", req, err)
	}
	if _, err := ctlOperation([]string{"config", "order", "nat/port-forwards", "--version", "7", "rul_00000001-0000-4000-8000-000000000001"}, io.Discard); err == nil {
		t.Fatal("a rule ID ordered a NAT list")
	}
	if !validChanged(config.ChangedView{Collection: "nat.portForwards", ID: "nat.portForwards", Version: "4"}) {
		t.Fatal("a NAT list order result was refused")
	}
}

func TestConfigDHCPv6(t *testing.T) {
	id := "dh6_00000001-0000-4000-8000-000000000001"
	for args, want := range map[string]ctlRequest{
		"config list dhcp6":      {method: "GET", path: "/api/v1/config/dhcp6", kind: "collection"},
		"config create dhcp6":    {method: "POST", path: "/api/v1/config/dhcp6", kind: "object"},
		"config get dhcp6 " + id: {method: "GET", path: "/api/v1/config/dhcp6/" + id, kind: "object"},
	} {
		req, err := ctlOperation(strings.Fields(args), io.Discard)
		if err != nil || req.method != want.method || req.path != want.path || req.kind != want.kind {
			t.Fatalf("%s: %+v %v", args, req, err)
		}
	}
	if !validChanged(config.ChangedView{Collection: "dhcp6", ID: id, Deleted: true}) {
		t.Fatal("a DHCPv6 result was refused")
	}
}

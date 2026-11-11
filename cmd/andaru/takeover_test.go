package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/config"
)

// labProposal is what the engine proposes for the static lab VM, plus an address-less second adapter.
func labProposal(t *testing.T) proposal {
	t.Helper()
	raw := `{"state":"ready","savedVersion":"9","adapters":[
		{"name":"eth0","permanentMac":"bc:24:11:89:01:10","summary":"IPv4 static 10.0.1.19/16, IPv6 SLAAC, default route","addressed":true},
		{"name":"eth1","permanentMac":"02:00:00:00:00:02","summary":"IPv4 DHCP, IPv6 none"}],
	"findings":[],"operations":[
		{"op":"create","collection":"zones","localId":"wan","value":{"name":"wan"}},
		{"op":"create","collection":"zones","localId":"lan","value":{"name":"lan"}},
		{"op":"create","collection":"links","localId":"link1","value":{"enabled":true,"kind":"ethernet","mtu":1500,"name":"eth0","permanentMac":"bc:24:11:89:01:10"}},
		{"op":"create","collection":"links","localId":"link2","value":{"enabled":true,"kind":"ethernet","mtu":1500,"name":"eth1","permanentMac":"02:00:00:00:00:02"}},
		{"op":"create","collection":"segments","localId":"seg1","value":{"addressing":{"dns":{"searchDomains":["lab.example"],"servers":["172.16.53.53"]},"ipv4":{"addresses":["10.0.1.19/16"],"gateway":"10.0.0.1","mode":"static"},"ipv6":{"mode":"slaac"}},"link":"@link1","name":"eth0","zone":"@wan"}},
		{"op":"create","collection":"segments","localId":"seg2","value":{"addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"none"}},"link":"@link2","name":"eth1","zone":"@lan"}},
		{"op":"create","collection":"services","localId":"ping","value":{"name":"ping","protocol":"icmp","types":["echo-request"]}},
		{"op":"create","collection":"policy.input","localId":"lanToFirewall","value":{"action":"accept","enabled":true,"from":["@lan"],"log":false,"name":"lan to firewall","services":["@ping"],"to":[]}},
		{"op":"create","collection":"policy.forward","localId":"lanOut","value":{"action":"accept","enabled":true,"from":["@lan"],"log":false,"name":"lan to any","services":[],"to":[]}},
		{"op":"replace","collection":"nat.outbound","expectedVersion":"0","value":{"mode":"automatic"}},
		{"op":"replace","collection":"management","expectedVersion":"0","value":{"segment":"@seg1"}}]}`
	var p proposal
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func opsJSON(t *testing.T, ops []config.Operation) string {
	t.Helper()
	raw, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDefaultAnswersReproduceTheProposal(t *testing.T) {
	p := labProposal(t)
	ops, err := batch(p, defaultAnswers(p))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := opsJSON(t, ops), opsJSON(t, p.Operations); got != want {
		t.Fatalf("accepting every detected value changed the batch:\n%s\nwant\n%s", got, want)
	}
}

func TestAnswersChangeTheBatch(t *testing.T) {
	p := labProposal(t)
	a := defaultAnswers(p)
	up := a.Adapters["link1"]
	up.IPv4 = family{Mode: "dhcp"}
	up.IPv6Mode = "none"
	a.Adapters["link1"] = up
	a.DNS = nil // use what DHCP offers
	drop := a.Adapters["link2"]
	drop.Keep = false
	a.Adapters["link2"] = drop
	ops, err := batch(p, a)
	if err != nil {
		t.Fatal(err)
	}
	got := opsJSON(t, ops)
	for _, want := range []string{`"localId":"wan"`, `"ipv4":{"mode":"dhcp"}`, `"ipv6":{"mode":"none"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	for _, gone := range []string{`"eth1"`, `"localId":"lan"`, `"dns"`, `10.0.1.19`} {
		if strings.Contains(got, gone) {
			t.Fatalf("%s should be gone from %s", gone, got)
		}
	}

	a = defaultAnswers(p)
	a.DNS = &dnsAnswer{Servers: []string{"192.0.2.53"}}
	ops, _ = batch(p, a)
	if got := opsJSON(t, ops); !strings.Contains(got, `"dns":{"servers":["192.0.2.53"]}`) || strings.Count(got, `"dns"`) != 1 {
		t.Fatalf("DNS = %s", got)
	}

	for name := range a.Adapters {
		x := a.Adapters[name]
		x.Keep = false
		a.Adapters[name] = x
	}
	if _, err := batch(p, a); err == nil {
		t.Fatal("a batch with no adapter was built")
	}
}

type fakeClient struct {
	calls    []string
	pending  string
	draftErr error
	draft    draft
	saveErr  error
	apply    applyReply
	applyErr error
	saved    []config.Operation
	head     config.Version
}

func (f *fakeClient) Proposal(context.Context) (proposal, error) {
	f.calls = append(f.calls, "proposal")
	return proposal{}, nil
}
func (f *fakeClient) Draft(_ context.Context, ops []config.Operation) (draft, error) {
	f.calls = append(f.calls, "draft")
	return f.draft, f.draftErr
}
func (f *fakeClient) Save(_ context.Context, ops []config.Operation, head config.Version) (config.Version, error) {
	f.calls = append(f.calls, "save")
	f.saved, f.head = ops, head
	return "10", f.saveErr
}
func (f *fakeClient) Applies(context.Context) (string, error) {
	f.calls = append(f.calls, "applies")
	return f.pending, nil
}
func (f *fakeClient) Apply(_ context.Context, saved config.Version, timeout int) (applyReply, error) {
	f.calls = append(f.calls, "apply "+string(saved))
	return f.apply, f.applyErr
}

func TestCommit(t *testing.T) {
	p := labProposal(t)
	live := applyReply{ID: "1", State: "awaiting_confirmation", Deadline: "2026-11-11T12:02:00Z", Files: []json.RawMessage{}}
	for name, tc := range map[string]struct {
		client    fakeClient
		code      int
		calls     []string
		outputHas string
	}{
		"live": {fakeClient{apply: live}, 0, []string{"applies", "draft", "save", "apply 10"}, "andaru ctl applies confirm 1"},
		// The preview flags netplan's files; the apply gates know a takeover replaces them.
		"netplan files replaced": {fakeClient{apply: live, draft: draft{Findings: []finding{{Severity: "error", Code: "FOREIGN_CONFIGURATION", Message: "x"}}}}, 0, []string{"applies", "draft", "save", "apply 10"}, "andaru ctl applies confirm 1"},
		"pending apply":          {fakeClient{pending: "4"}, 1, []string{"applies"}, "Apply 4 is awaiting confirmation"},
		"draft refused":          {fakeClient{draft: draft{ApplyFindings: []finding{{Severity: "error", Code: "WOULD_UNCONFIGURE", Path: "eth0", Message: "x"}}}}, 1, []string{"applies", "draft"}, "WOULD_UNCONFIGURE"},
		"head moved":             {fakeClient{saveErr: &apiError{Status: 412, Code: "PRECONDITION_FAILED"}}, 1, []string{"applies", "draft", "save"}, "saved meanwhile"},
		"apply refused":          {fakeClient{apply: live, applyErr: &apiError{Status: 422, Code: "APPLY_REFUSED"}}, 1, []string{"applies", "draft", "save", "apply 10"}, "andaru ctl apply network --saved-version 10 --takeover"},
		"apply unreachable":      {fakeClient{applyErr: errors.New("could not reach the local engine")}, 1, []string{"applies", "draft", "save", "apply 10"}, "whether the apply started is unknown"},
		"activation fails":       {fakeClient{apply: applyReply{ID: "1", State: "failed", Reason: "eth0 lost its IPv4 address", Files: []json.RawMessage{}}}, 1, []string{"applies", "draft", "save", "apply 10"}, "saved as version 10 but not live"},
	} {
		t.Run(name, func(t *testing.T) {
			c := tc.client
			var out bytes.Buffer
			held := false
			code := commit(context.Background(), &c, p, p.Operations, 120, &out, func() { held = true })
			if code != tc.code || !slices.Equal(c.calls, tc.calls) || !strings.Contains(out.String(), tc.outputHas) {
				t.Fatalf("code %d calls %q output:\n%s", code, c.calls, out.String())
			}
			if slices.Contains(c.calls, "save") {
				if !held || c.head != "9" || opsJSON(t, c.saved) != opsJSON(t, p.Operations) {
					t.Fatalf("save: held=%v head=%q", held, c.head)
				}
			}
			if slices.Contains(c.calls, "save") && !strings.Contains(out.String(), "andaru ctl applies list") {
				t.Fatalf("no advice for a dropped session before the save:\n%s", out.String())
			}
		})
	}
}

func TestTakeoverCommandModes(t *testing.T) {
	var out, diag bytes.Buffer
	// No terminal and no --accept-detected: nothing to ask on, so say what to do instead.
	if code := run(context.Background(), []string{"ctl", "--socket", "/nonexistent/andaru.sock", "takeover"}, strings.NewReader(""), &out, &diag); code != 2 || !strings.Contains(diag.String(), "--accept-detected") {
		t.Fatalf("exit %d: %s", code, diag.String())
	}
	for _, args := range [][]string{{"takeover", "extra"}, {"takeover", "--confirm-timeout", "abc"}, {"takeover", "--accept-detected", "--confirm-timeout", "10"}} {
		diag.Reset()
		if code := run(context.Background(), append([]string{"ctl", "--socket", "/nonexistent/andaru.sock"}, args...), strings.NewReader(""), &out, &diag); code != 2 {
			t.Fatalf("%q: exit %d", args, code)
		}
	}
}

func TestAcceptDetectedRefusesWithoutWriting(t *testing.T) {
	p := labProposal(t)
	for name, change := range map[string]func(*proposal){
		"taken over":    func(p *proposal) { p.State = "taken-over" },
		"unsupported":   func(p *proposal) { p.State = "unsupported" },
		"error finding": func(p *proposal) { p.Findings = []finding{{Severity: "error", Code: "X", Message: "m"}} },
	} {
		t.Run(name, func(t *testing.T) {
			q := p
			change(&q)
			c := &fakeClient{}
			var out bytes.Buffer
			if code := acceptDetected(context.Background(), c, q, 120, &out, func() {}); code != 1 || len(c.calls) != 0 {
				t.Fatalf("code %d calls %q", code, c.calls)
			}
		})
	}
	c := &fakeClient{apply: applyReply{ID: "1", State: "awaiting_confirmation", Deadline: "d", Files: []json.RawMessage{}}}
	if code := acceptDetected(context.Background(), c, p, 120, &bytes.Buffer{}, func() {}); code != 0 || opsJSON(t, c.saved) != opsJSON(t, p.Operations) {
		t.Fatalf("code %d", code)
	}
	if !errors.Is(&apiError{Status: 412}, errHeadMoved) {
		t.Fatal("a 412 must read as the head having moved")
	}
}

// The real client against a socket that decodes requests as the engine does.
func TestHTTPTakeoverClientSpeaksTheEngineContract(t *testing.T) {
	p := labProposal(t)
	var saves int
	socket := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/config/changes":
			changes, err := config.DecodeChanges(body)
			if err != nil || changes.ExpectedSavedVersion == nil || *changes.ExpectedSavedVersion != "9" || len(changes.Operations) != len(p.Operations) {
				t.Errorf("save body %s: %v", body, err)
			}
			if saves++; saves == 1 {
				w.WriteHeader(http.StatusPreconditionFailed)
				io.WriteString(w, `{"error":{"code":"PRECONDITION_FAILED","message":"stale","details":[]}}`)
				return
			}
			io.WriteString(w, `{"savedVersion":"10"}`)
		case "POST /api/v1/applies":
			var apply struct {
				SavedVersion          string `json:"savedVersion"`
				Takeover              bool   `json:"takeover"`
				ConfirmTimeoutSeconds int    `json:"confirmTimeoutSeconds"`
			}
			d := json.NewDecoder(bytes.NewReader(body))
			d.DisallowUnknownFields()
			if err := d.Decode(&apply); err != nil || apply.SavedVersion != "10" || !apply.Takeover || apply.ConfirmTimeoutSeconds != 90 {
				t.Errorf("apply body %s: %v", body, err)
			}
			io.WriteString(w, `{"id":"1","state":"awaiting_confirmation","deadline":"d","files":[]}`)
		case "GET /api/v1/applies":
			io.WriteString(w, `{"pointers":{"confirmed":null,"lastKnownGood":null,"pending":"4","live":"4"},"items":[]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	c := &httpTakeoverClient{socket: socket}
	ctx := context.Background()
	if _, err := c.Save(ctx, p.Operations, p.SavedVersion); !errors.Is(err, errHeadMoved) {
		t.Fatalf("a 412 reads as %v", err)
	}
	if v, err := c.Save(ctx, p.Operations, p.SavedVersion); err != nil || v != "10" {
		t.Fatalf("save = %q, %v", v, err)
	}
	if reply, err := c.Apply(ctx, "10", 90); err != nil || reply.State != "awaiting_confirmation" {
		t.Fatalf("apply = %+v, %v", reply, err)
	}
	if pending, err := c.Applies(ctx); err != nil || pending != "4" {
		t.Fatalf("pending = %q, %v", pending, err)
	}
}

// ifupdown is masked for every interface it configures, so the assistant must keep each one.
func TestAdapterRequiredCannotBeLeftOut(t *testing.T) {
	p := labProposal(t)
	p.Adapters[1].Required = true
	s := newFormState(p, defaultAnswers(p))
	if err := s.checkKept([]string{"link1"}); err == nil || !strings.Contains(err.Error(), "ifupdown") {
		t.Fatalf("leaving out a required adapter: %v", err)
	}
	if err := s.checkKept([]string{"link1", "link2"}); err != nil {
		t.Fatal(err)
	}
}

// The operator's management choice and zone changes carry into the batch; rules for a zone that is gone go too.
func TestAnswersMoveManagementAndDropOrphanRules(t *testing.T) {
	p := labProposal(t)
	a := defaultAnswers(p)
	if a.Management != "link1" {
		t.Fatalf("management answer %q", a.Management)
	}
	a.Management = "link2"
	ops, err := batch(p, a)
	if err != nil || !strings.Contains(opsJSON(t, ops), `"value":{"segment":"@seg2"}`) {
		t.Fatalf("management not moved: %v %s", err, opsJSON(t, ops))
	}
	lan := a.Adapters["link2"]
	lan.Zone = "wan"
	a.Adapters["link2"] = lan
	ops, _ = batch(p, a)
	if got := opsJSON(t, ops); strings.Contains(got, `"@lan"`) || !strings.Contains(got, `"nat.outbound"`) {
		t.Fatalf("rules for a removed zone kept: %s", got)
	}
	gone := a.Adapters["link2"]
	gone.Keep = false
	a.Adapters["link2"] = gone
	if _, err := batch(p, a); err == nil {
		t.Fatal("management on an adapter left out was accepted")
	}
}

func TestProposalCarriesTheSessionAddress(t *testing.T) {
	var asked string
	socket := replySocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RequestURI()
		io.WriteString(w, `{"state":"ready","savedVersion":"9","adapters":[],"findings":[],"operations":[]}`)
	}))
	c := &httpTakeoverClient{socket: socket, via: "fe80::1%eth0"}
	if _, err := c.Proposal(context.Background()); err != nil || asked != "/api/v1/takeover/proposal?via=fe80%3A%3A1%25eth0" {
		t.Fatalf("asked %q: %v", asked, err)
	}
}

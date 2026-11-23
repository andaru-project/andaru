package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/identity"
)

const uplinkDeclaration = `{"schemaVersion":4,"links":[{"id":"lnk_00000001-0000-4000-8000-000000000001","name":"wan0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:aa:bb:01"}],
"segments":[{"id":"seg_00000001-0000-4000-8000-000000000001","name":"wan","link":"lnk_00000001-0000-4000-8000-000000000001","zone":"zon_00000001-0000-4000-8000-000000000001","addressing":{"ipv4":{"mode":"dhcp"},"ipv6":{"mode":"none"}}}],
"zones":[{"id":"zon_00000001-0000-4000-8000-000000000001","name":"wan"}]}`

// applyBench is a plain host with one NIC as the handler and realize see it, without touching this machine.
type applyBench struct {
	t           *testing.T
	h           http.Handler
	state, root string
	declaration string
	head        config.Version
	identities  *applyIdentityState
	coordinator *securityCoordinator
	mu          sync.Mutex
	calls       []string
	reads       int
	armErr      error
	skip        bool          // Realize fails before the verb runs
	block       chan struct{} // Realize waits here when set
	beforeVerb  func()
	afterArm    func()
	unbound     bool // networkd never binds the new files
	hostDown    bool
	stuck       bool // activation fails and cannot restore: the generation stays pending
	facts       host.RealizerFacts
	noBootUnit  bool
}

type applyIdentityState struct {
	mu       sync.Mutex
	revision int64
	accounts map[string]identity.Account
}

func (s *applyIdentityState) Revision(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision, nil
}

func (s *applyIdentityState) GetAccount(_ context.Context, id string) (identity.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[id]
	if !ok {
		return identity.Account{}, identity.ErrNotFound
	}
	return account, nil
}

func (b *applyBench) note(format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, fmt.Sprintf(format, args...))
}

func (b *applyBench) netFiles(pattern string) []string {
	paths, _ := filepath.Glob(filepath.Join(b.root, networkDir, pattern))
	for i, p := range paths {
		paths[i] = strings.TrimPrefix(p, b.root)
	}
	return paths
}

func (b *applyBench) managed() []host.Managed {
	if b.unbound {
		return []host.Managed{}
	}
	if found := b.netFiles("50-andaru-eth0.network"); len(found) == 1 {
		return []host.Managed{{Name: "eth0", NetworkFile: found[0], State: "configured"}}
	}
	return []host.Managed{}
}

// tool answers the commands realize runs.
func (b *applyBench) tool(_ context.Context, name string, args ...string) ([]byte, error) {
	switch filepath.Base(name) + " " + strings.Join(args, " ") {
	case "networkctl list --json=short":
		type iface struct{ Name, AdministrativeState, NetworkFile string }
		reply := struct{ Interfaces []iface }{[]iface{}}
		for _, m := range b.managed() {
			reply.Interfaces = append(reply.Interfaces, iface{m.Name, m.State, m.NetworkFile})
		}
		return json.Marshal(reply)
	case "ip -d -j address show":
		return []byte(`[{"ifindex":2,"ifname":"eth0","link_type":"ether","operstate":"UP","mtu":1500,"flags":[],"address":"52:54:00:aa:bb:01","addr_info":[{"family":"inet","local":"10.0.1.19","prefixlen":20,"scope":"global"}]}]`), nil
	case "ip -j route show default":
		return []byte(`[{"dst":"default","dev":"eth0"}]`), nil
	case "nft -j list table inet andaru":
		// No test here loads a filter.
		return nil, errors.New("nft [-j list table inet andaru]: exit status 1: Error: No such file or directory")
	}
	return nil, nil
}

func (b *applyBench) realizer() generation.Realizer {
	return generation.Realizer{
		StateDir: b.state, Root: b.root, Run: b.tool, Now: time.Now,
		Chown:      func(string, int, int) error { return nil },
		VerifyWait: 50 * time.Millisecond, Poll: 5 * time.Millisecond, LockWait: time.Second,
	}
}

func (b *applyBench) Arm(_ context.Context, id, seconds int) error {
	b.note("arm %d %d", id, seconds)
	if b.afterArm != nil {
		b.afterArm()
	}
	return b.armErr
}

func (b *applyBench) Disarm(_ context.Context, id int) { b.note("disarm %d", id) }

func (b *applyBench) Realize(ctx context.Context, verb string, id int, extra ...string) error {
	b.note("%s", strings.Join(append([]string{verb, fmt.Sprint(id)}, extra...), " "))
	if b.beforeVerb != nil {
		b.beforeVerb()
	}
	if b.block != nil {
		<-b.block
	}
	// A real runner starts a process under the request's context; a cancelled one never runs the verb.
	if err := ctx.Err(); err != nil || b.skip {
		return errors.Join(err, errors.New("the unit did not run"))
	}
	if b.stuck && verb == "activate" {
		dir := filepath.Join(generation.Dir(b.state), fmt.Sprint(id))
		_ = os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"state":"activating","previous":null}`), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "..", "pointers.json"), fmt.Appendf(nil, `{"confirmed":null,"lastKnownGood":null,"pending":%d,"live":null}`, id), 0o600)
		return errors.New("restoring the previous files failed")
	}
	var err error
	switch r := b.realizer(); verb {
	case "activate":
		err = r.Activate(ctx, id)
	case "confirm":
		err = r.Confirm(ctx, id)
	case "rollback":
		err = r.Rollback(ctx, id, extra[0])
	}
	if errors.Is(err, generation.ErrWrongState) {
		return nil // as the command exits
	}
	return err
}

func newApplyBench(t *testing.T) *applyBench {
	t.Helper()
	b := &applyBench{t: t, state: t.TempDir(), root: t.TempDir(), declaration: uplinkDeclaration, head: "7"}
	if err := os.Chmod(b.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := initializeAuthorizationState(t.Context(), b.state); err != nil {
		t.Fatal(err)
	}
	down := func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.reads++
		if b.hostDown {
			return errors.New("host unreadable")
		}
		return nil
	}
	read := hostReader{
		Links: func(context.Context) ([]host.LinkStatus, error) {
			return []host.LinkStatus{{IfIndex: 2, Name: "eth0", LinkType: "ether", PermanentAddress: "52:54:00:aa:bb:01", LinkAddress: "52:54:00:aa:bb:01",
				Addresses: []host.AddressStatus{{Family: "ipv4", Address: "10.0.1.19", PrefixLength: 20, Scope: "global"}}}}, down()
		},
		Facts:    func() (host.RealizerFacts, error) { return b.facts, nil },
		Managed:  func(context.Context) ([]host.Managed, error) { return b.managed(), nil },
		BootUnit: func() bool { return !b.noBootUnit },
	}
	input := func(_ context.Context, version *config.Version) (config.RealizationInput, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.reads++
		if version != nil {
			t.Errorf("apply read version %q instead of the head", *version)
		}
		snapshot, err := authz.NewSnapshot(nil)
		if err != nil {
			return config.RealizationInput{}, err
		}
		return config.RealizationInput{SavedVersion: b.head, Value: json.RawMessage(b.declaration), Authorization: snapshot}, nil
	}
	b.identities = &applyIdentityState{revision: 1, accounts: map[string]identity.Account{}}
	b.coordinator = &securityCoordinator{stateDir: b.state}
	applies := newAppliesHandler(input, read, b, b.state, b.identities, b.coordinator)
	b.h = newHandler(read.Links, newSavedHandler(nil, read.Links, read.Facts, applies, nil))
	return b
}

type applyReply struct {
	ID                    string     `json:"id"`
	SavedVersion          string     `json:"savedVersion"`
	State                 string     `json:"state"`
	Previous              *string    `json:"previous"`
	Deadline              *time.Time `json:"deadline"`
	Reason                string     `json:"reason"`
	ConfirmTimeoutSeconds int        `json:"confirmTimeoutSeconds"`
	Files                 []struct {
		Path, Mode string
		Content    *string
	} `json:"files"`
}

func (b *applyBench) call(method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := rootRequest(method, path)
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(r.Context())
	}
	b.h.ServeHTTP(w, r)
	return w
}

func (b *applyBench) apply(body string) applyReply {
	b.t.Helper()
	return b.decode(b.call(http.MethodPost, "/api/v1/applies", body), http.StatusOK)
}

func (b *applyBench) decode(w *httptest.ResponseRecorder, status int) applyReply {
	b.t.Helper()
	var reply applyReply
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &reply) != nil {
		b.t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body)
	}
	return reply
}

func (b *applyBench) numbered() []string {
	entries, _ := os.ReadDir(generation.Dir(b.state))
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "baseline" {
			out = append(out, e.Name())
		}
	}
	return out
}

const applyBody = `{"savedVersion":"7"}`

func TestApplyHappyPath(t *testing.T) {
	b := newApplyBench(t)
	before := time.Now()
	w := b.call(http.MethodPost, "/api/v1/applies", applyBody)
	got := b.decode(w, http.StatusOK)
	if got.ID != "1" || got.SavedVersion != "7" || got.State != generation.Awaiting || got.Previous != nil || got.ConfirmTimeoutSeconds != 120 {
		t.Fatalf("apply = %+v", got)
	}
	if got.Deadline == nil || got.Deadline.Before(before.Add(119*time.Second)) || got.Deadline.After(time.Now().Add(121*time.Second)) {
		t.Fatalf("deadline = %v", got.Deadline)
	}
	if len(got.Files) != 3 || got.Files[1].Path != uplinkFile || got.Files[1].Mode != "0644" || got.Files[1].Content != nil || strings.Contains(w.Body.String(), "sha256") {
		t.Fatalf("files = %s", w.Body)
	}
	if !slices.Equal(b.calls, []string{"arm 1 180", "activate 1"}) {
		t.Fatalf("calls = %q; the timer must be armed before anything changes", b.calls)
	}
	if again := b.decode(b.call(http.MethodGet, "/api/v1/applies/1", ""), http.StatusOK); again.State != got.State || !again.Deadline.Equal(*got.Deadline) {
		t.Fatalf("read back = %+v", again)
	}
	var list struct {
		Pointers map[string]*string `json:"pointers"`
		Items    []applyReply       `json:"items"`
	}
	w = b.call(http.MethodGet, "/api/v1/applies", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != "1" {
		t.Fatalf("list = %s", w.Body)
	}
	if p := list.Pointers; p["pending"] == nil || *p["pending"] != "1" || p["confirmed"] != nil || len(p) != 4 {
		t.Fatalf("pointers = %s", w.Body)
	}
	if b.call(http.MethodHead, "/api/v1/applies", "").Body.Len() != 0 {
		t.Fatal("HEAD returned a body")
	}
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies", applyBody), http.StatusConflict, "APPLY_IN_PROGRESS")
}

func TestApplyStagesInitialIdentityRevision(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("authorization pending requires root")
	}
	b := newApplyBench(t)
	b.identities.revision = 0
	got := b.decode(b.call(http.MethodPost, "/api/v1/applies", applyBody), http.StatusOK)
	if got.ID != "1" || got.State != generation.Awaiting {
		t.Fatalf("apply = %+v", got)
	}
	lock, err := authz.Acquire(t.Context(), b.state)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if pending, found, err := lock.Pending(); err != nil || found {
		t.Fatalf("activation left authorization pending %+v err=%v", pending, err)
	}
	g, err := generation.Read(generation.Dir(b.state), 1)
	if err != nil || g.IdentityRevision != 0 {
		t.Fatalf("generation revision=%d err=%v", g.IdentityRevision, err)
	}
}

func TestApplyRequestValidation(t *testing.T) {
	b := newApplyBench(t)
	for _, body := range []string{
		``, `{`, `[]`, `{"takeover":true}`, `{"savedVersion":7}`, `{"savedVersion":"07"}`, `{"savedVersion":""}`,
		`{"savedVersion":"7","confirmTimeoutSeconds":29}`, `{"savedVersion":"7","confirmTimeoutSeconds":601}`,
		`{"savedVersion":"7","confirmTimeoutSeconds":"60"}`, `{"savedVersion":"7","force":true}`,
		`{"savedVersion":"7"} {}`, `{"savedVersion":"7","pad":"` + strings.Repeat("x", 4096) + `"}`, `{"savedVersion":"7","takeover":false}`,
	} {
		assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies", body), http.StatusBadRequest, "INVALID_REQUEST")
	}
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies", `{"savedVersion":"6"}`), http.StatusPreconditionFailed, "PRECONDITION_FAILED")
	if len(b.calls) != 0 || len(b.numbered()) != 0 {
		t.Fatalf("a rejected request reached the host: %q %q", b.calls, b.numbered())
	}
	if got := b.apply(`{"savedVersion":"7","confirmTimeoutSeconds":30}`); got.ConfirmTimeoutSeconds != 30 || b.calls[0] != "arm 1 90" {
		t.Fatalf("apply = %+v, calls = %q", got, b.calls)
	}
}

func TestApplyRefusedStoresNothing(t *testing.T) {
	b := newApplyBench(t)
	b.noBootUnit = true
	w := b.call(http.MethodPost, "/api/v1/applies", applyBody)
	assertAPIError(t, w, http.StatusUnprocessableEntity, "APPLY_REFUSED")
	var body struct {
		Error struct {
			Findings []struct{ Severity, Code, Path, Message string } `json:"findings"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Error.Findings) != 1 || body.Error.Findings[0].Code != "BOOT_UNIT_DISABLED" {
		t.Fatalf("findings = %s", w.Body)
	}
	if len(b.calls) != 0 || len(b.numbered()) != 0 {
		t.Fatalf("a refused apply left %q %q", b.calls, b.numbered())
	}
}

func TestApplyWhenRealizeCannotRun(t *testing.T) {
	b := newApplyBench(t)
	b.armErr = errors.New("systemd-run failed")
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies", applyBody), http.StatusServiceUnavailable, "REALIZE_UNAVAILABLE")
	if !slices.Equal(b.calls, []string{"arm 1 180"}) {
		t.Fatalf("calls = %q; nothing may change without a timer", b.calls)
	}
	if got := b.decode(b.call(http.MethodGet, "/api/v1/applies/1", ""), 200); got.State != generation.Failed || got.Reason != "never started" {
		t.Fatalf("apply = %+v", got)
	}

	b.armErr, b.skip, b.calls = nil, true, nil
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies", applyBody), http.StatusServiceUnavailable, "REALIZE_UNAVAILABLE")
	// Whether the unit ran is unknown here, so the timer stays: it does nothing if there is nothing to undo.
	if !slices.Equal(b.calls, []string{"arm 2 180", "activate 2"}) {
		t.Fatalf("calls = %q", b.calls)
	}

	b.hostDown, b.calls = true, nil
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies", applyBody), http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE")
	if len(b.calls) != 0 {
		t.Fatalf("calls = %q", b.calls)
	}
}

func TestApplyFailedActivationDisarms(t *testing.T) {
	b := newApplyBench(t)
	// The gates read the host before the change; only afterwards does networkd fail to bind.
	b.beforeVerb = func() { b.unbound = true }
	got := b.apply(applyBody)
	if got.State != generation.Failed || got.Reason == "" || got.Deadline != nil {
		t.Fatalf("apply = %+v", got)
	}
	if !slices.Equal(b.calls, []string{"arm 1 180", "activate 1", "disarm 1"}) {
		t.Fatalf("calls = %q", b.calls)
	}
	b.unbound = false
	if files := b.netFiles("*"); len(files) != 0 {
		t.Fatalf("the generation's files were left: %v", files)
	}
}

func TestApplySurvivesClientDisconnect(t *testing.T) {
	b := newApplyBench(t)
	b.block = make(chan struct{})
	ctx, cancel := context.WithCancel(rootRequest(http.MethodPost, "/").Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/applies", strings.NewReader(applyBody)).WithContext(ctx)
		b.h.ServeHTTP(httptest.NewRecorder(), r)
	}()
	for {
		b.mu.Lock()
		started := len(b.calls) == 2
		b.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel() // the SSH session carrying the CLI drops
	close(b.block)
	<-done
	if got := b.decode(b.call(http.MethodGet, "/api/v1/applies/1", ""), 200); got.State != generation.Awaiting {
		t.Fatalf("the apply died with its client: %+v", got)
	}
}

func TestConfirmAndLateTimer(t *testing.T) {
	b := newApplyBench(t)
	b.apply(applyBody)
	got := b.decode(b.call(http.MethodPost, "/api/v1/applies/1/confirmation", ""), 200)
	if got.State != generation.Confirmed || got.Reason != "" {
		t.Fatalf("apply = %+v", got)
	}
	if !slices.Equal(b.calls[2:], []string{"confirm 1", "disarm 1"}) {
		t.Fatalf("calls = %q", b.calls)
	}
	// The timer could not be stopped and fires anyway.
	if err := b.Realize(t.Context(), "rollback", 1, "deadline"); err != nil {
		t.Fatal(err)
	}
	if got := b.decode(b.call(http.MethodGet, "/api/v1/applies/1", ""), 200); got.State != generation.Confirmed {
		t.Fatalf("a late timer undid a confirmation: %+v", got)
	}
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies/1/confirmation", ""), http.StatusConflict, "APPLY_NOT_AWAITING")
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies/1/rollback", ""), http.StatusConflict, "APPLY_NOT_AWAITING")

	// The next apply runs on top of the confirmed generation and names it as previous.
	second := b.apply(applyBody)
	if second.ID != "2" || second.State != generation.Awaiting || second.Previous == nil || *second.Previous != "1" {
		t.Fatalf("second apply = %+v", second)
	}
}

func TestEarlyRollback(t *testing.T) {
	b := newApplyBench(t)
	b.apply(applyBody)
	got := b.decode(b.call(http.MethodPost, "/api/v1/applies/1/rollback", ""), 200)
	if got.State != generation.RolledBack || got.Reason != "requested" {
		t.Fatalf("apply = %+v", got)
	}
	if !slices.Equal(b.calls[2:], []string{"rollback 1 requested", "disarm 1"}) {
		t.Fatalf("calls = %q", b.calls)
	}
	if files := b.netFiles("*"); len(files) != 0 {
		t.Fatalf("files = %v", files)
	}
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies/1/confirmation", ""), http.StatusConflict, "APPLY_NOT_AWAITING")
}

func TestAppliesRouting(t *testing.T) {
	b := newApplyBench(t)
	b.apply(applyBody)
	for _, path := range []string{"/0", "/-1", "/+1", "/01", "/abc", "/99999999999999999999", "/1/unknown", "/2", "/1/confirmation/x", "/"} {
		assertAPIError(t, b.call(http.MethodGet, "/api/v1/applies"+path, ""), http.StatusNotFound, "NOT_FOUND")
	}
	assertAPIError(t, b.call(http.MethodPost, "/api/v1/applies/2/confirmation", ""), http.StatusNotFound, "NOT_FOUND")
	for _, route := range []struct{ method, path, allow string }{
		{http.MethodDelete, "/api/v1/applies", "GET, HEAD, POST"},
		{http.MethodPost, "/api/v1/applies/1", "GET, HEAD"},
		{http.MethodGet, "/api/v1/applies/1/confirmation", "POST"},
		{http.MethodPut, "/api/v1/applies/1/rollback", "POST"},
	} {
		w := b.call(route.method, route.path, "")
		assertAPIError(t, w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		if w.Header().Get("Allow") != route.allow {
			t.Fatalf("%s %s: Allow = %q", route.method, route.path, w.Header().Get("Allow"))
		}
	}
}

func TestAppliesRequireRoot(t *testing.T) {
	b := newApplyBench(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/applies"}, {http.MethodGet, "/api/v1/applies"}, {http.MethodGet, "/api/v1/applies/1"},
		{http.MethodPost, "/api/v1/applies/1/confirmation"}, {http.MethodPost, "/api/v1/applies/1/rollback"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(route.method, route.path, strings.NewReader(applyBody))
		b.h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 1000, verified: true})))
		assertAPIError(t, w, http.StatusForbidden, "FORBIDDEN")
	}
	if b.reads != 0 || len(b.calls) != 0 || len(b.numbered()) != 0 {
		t.Fatalf("an unauthorized peer reached the store, host or runner: reads=%d calls=%q", b.reads, b.calls)
	}
}

func TestSystemdRunnerUnitNamesAreDistinct(t *testing.T) {
	var argv [][]string
	runner := systemdRunner{stateDir: "/srv/state", busSocket: "/run/andaru/bus.sock", exec: func(_ context.Context, name string, args ...string) error {
		argv = append(argv, append([]string{name}, args...))
		return nil
	}}
	if err := runner.Arm(t.Context(), 3, 180); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"activate", "confirm"} {
		if err := runner.Realize(t.Context(), verb, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := runner.Realize(t.Context(), "rollback", 3, "requested"); err != nil {
		t.Fatal(err)
	}
	runner.Disarm(t.Context(), 3)
	units := map[string]bool{}
	for _, call := range argv[:4] {
		// Each verb, the timer's rollback too, tells the bus of the apply's new state.
		bus := slices.Index(call, "--bus-socket")
		if call[0] != "/usr/bin/systemd-run" || !slices.Contains(call, "--collect") || !slices.Contains(call, "RuntimeMaxSec=180") || bus < 0 || call[bus+1] != "/run/andaru/bus.sock" {
			t.Fatalf("argv = %q", call)
		}
		i := slices.IndexFunc(call, func(a string) bool { return strings.HasPrefix(a, "--unit=") })
		// systemd refuses a transient unit whose name is loaded: the armed timer's service must not block an early rollback.
		if i < 0 || units[call[i]] {
			t.Fatalf("unit name missing or reused: %q", call)
		}
		units[call[i]] = true
	}
	arm := strings.Join(argv[0], " ")
	// A rollback that fails once (lock held, /run full) must be tried again: nothing else will undo the change.
	if !strings.Contains(arm, "-p Restart=on-failure -p RestartSec=10") || !strings.Contains(arm, "--unit=andaru-rollback-3 --on-active=180 --timer-property=AccuracySec=1s") || !strings.HasSuffix(arm, "/usr/bin/andaru realize --state-dir /srv/state --bus-socket /run/andaru/bus.sock rollback 3 deadline") {
		t.Fatalf("arm = %q", arm)
	}
	if early := strings.Join(argv[3], " "); !strings.Contains(early, " --wait ") || !strings.HasSuffix(early, "/usr/bin/andaru realize --state-dir /srv/state --bus-socket /run/andaru/bus.sock rollback 3 requested") {
		t.Fatalf("early rollback = %q", early)
	}
	if !slices.Equal(argv[4], []string{"/usr/bin/systemctl", "stop", "andaru-rollback-3.timer"}) {
		t.Fatalf("disarm = %q", argv[4])
	}
}

func TestOpenAPIDescribesApplies(t *testing.T) {
	var doc struct {
		Info  struct{ Version string }
		Paths map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(openAPIDocument, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Info.Version != "0.4.0-dev" {
		t.Fatalf("version = %q", doc.Info.Version)
	}
	for path, methods := range map[string][]string{
		"/api/v1/applies": {"get", "post"}, "/api/v1/applies/{id}": {"get"},
		"/api/v1/applies/{id}/confirmation": {"post"}, "/api/v1/applies/{id}/rollback": {"post"},
	} {
		for _, method := range methods {
			if doc.Paths[path][method] == nil {
				t.Fatalf("%s %s is not described", method, path)
			}
		}
	}
}

func TestApplyKeepsTimerWhileActivationIsUnresolved(t *testing.T) {
	b := newApplyBench(t)
	// Activation failed and could not put the old files back: the generation stays pending and only the timer can undo it.
	b.stuck = true
	got := b.apply(applyBody)
	if got.State != generation.Activating {
		t.Fatalf("apply = %+v", got)
	}
	if !slices.Equal(b.calls, []string{"arm 1 180", "activate 1"}) {
		t.Fatalf("calls = %q; the timer is the only thing left that can restore the host", b.calls)
	}
}

func (b *applyBench) callPrincipal(method, path string, principal identity.Principal) *httptest.ResponseRecorder {
	b.t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request = request.WithContext(withVerifiedPrincipal(request.Context(), principal, "192.0.2.2"))
	response := httptest.NewRecorder()
	b.h.ServeHTTP(response, request)
	return response
}

func (b *applyBench) authorizationState() authz.State {
	b.t.Helper()
	if os.Geteuid() != 0 {
		b.t.Skip("authorization state requires root")
	}
	lock, err := authz.Acquire(b.t.Context(), b.state)
	if err != nil {
		b.t.Fatal(err)
	}
	defer lock.Close()
	state, err := lock.Read()
	if err != nil {
		b.t.Fatal(err)
	}
	return state
}

func TestUnchangedAuthorizationAllowsTokenConfirmation(t *testing.T) {
	b := newApplyBench(t)
	first := b.apply(applyBody)
	b.decode(b.call(http.MethodPost, appliesPath+"/"+first.ID+"/confirmation", ""), http.StatusOK)
	second := b.apply(applyBody)
	token := identity.Principal{
		Account: identity.Account{ID: "00000000-0000-4000-8000-000000000001", Kind: identity.Service, Enabled: true},
		Actor:   authz.Actor{Kind: authz.ActorToken, ID: strings.Repeat("A", 22)}, Capabilities: []authz.Capability{authz.ConfigurationConfirm},
	}
	response := b.callPrincipal(http.MethodPost, appliesPath+"/"+second.ID+"/confirmation", token)
	if response.Code != http.StatusOK {
		t.Fatalf("unchanged authorization token confirmation status=%d body=%s", response.Code, response.Body)
	}
}

// filterDeclaration is the uplink with a starter rule, outbound NAT and management on its segment.
var filterDeclaration = strings.TrimSuffix(uplinkDeclaration, "}") + `,
"aliases":{"addresses":[],"ports":[{"id":"svc_00000001-0000-4000-8000-000000000001","name":"ping","protocol":"icmp","types":["echo-request"]}]},
"rules":[{"id":"rul_00000001-0000-4000-8000-000000000001","name":"ping","enabled":true,"action":"accept","from":{"zones":["zon_00000001-0000-4000-8000-000000000001"],"addresses":[]},"to":{"zones":["zon_firewall"],"addresses":[]},"ports":["svc_00000001-0000-4000-8000-000000000001"],"log":false}],
"nat":{"outbound":{"mode":"automatic"}},
"management":{"segment":"seg_00000001-0000-4000-8000-000000000001"}}`

func TestFilterGates(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(b *applyBench)
		code string
	}{
		"ufw.service enabled": {func(b *applyBench) { b.facts.FilterUnits = []string{"ufw.service"} }, "FOREIGN_FILTER"},
		// Nothing masks nftables.service: enabled, it would flush Andaru's filter.
		"nftables.service enabled": {func(b *applyBench) { b.facts.FilterUnits = []string{"nftables.service"} }, "FOREIGN_FILTER"},
		"nft missing":              {func(b *applyBench) { b.facts.NftTool = false }, "TOOL_MISSING"},
		"no management": {func(b *applyBench) {
			b.declaration = strings.Replace(b.declaration, `,
"management":{"segment":"seg_00000001-0000-4000-8000-000000000001"}`, "", 1)
		}, "MANAGEMENT_REQUIRED"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newApplyBench(t)
			b.declaration, b.facts.NftTool = filterDeclaration, true
			tc.edit(b)
			w := b.call(http.MethodPost, "/api/v1/applies", applyBody)
			var body struct {
				Error struct {
					Findings []struct{ Code string } `json:"findings"`
				} `json:"error"`
			}
			if w.Code != http.StatusUnprocessableEntity || json.Unmarshal(w.Body.Bytes(), &body) != nil ||
				!slices.ContainsFunc(body.Error.Findings, func(f struct{ Code string }) bool { return f.Code == tc.code }) {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if len(b.calls) != 0 || len(b.numbered()) != 0 {
				t.Fatalf("a refused apply left %q %q", b.calls, b.numbered())
			}
		})
	}
}

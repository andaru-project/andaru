package reset

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/identity"
	"github.com/andaru-project/andaru/internal/kept"
)

// box is a host with Andaru's old state, its kept copies, and a fake systemd, dpkg and engine.
type box struct {
	t        *testing.T
	env      Env
	keptRoot string
	upgrades string
	mu       sync.Mutex
	commands []string
	// applied are the saved versions the fake engine was asked to apply; up says whether each comes up.
	applied []string
	up      func(n int) bool
	// pending makes apply n reach awaiting confirmation and stay there, as when the confirm is lost.
	pending func(n int) bool
	// inactive are units systemctl reports inactive before the reset.
	inactive []string
	// onStop runs at the first systemctl stop, to look at the tree then.
	onStop func()
	// fail makes commands with this prefix fail, once each time it is set.
	fail string
	// applyErr makes apply n report an error after it did its work, as when the confirm's answer is lost.
	applyErr func(n int) bool
}

func newBox(t *testing.T) *box {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("the reset requires root")
	}
	base := t.TempDir()
	b := &box{t: t, keptRoot: filepath.Join(base, "andaru-kept"), upgrades: filepath.Join(base, "andaru-upgrades"), up: func(int) bool { return true }}
	state := filepath.Join(base, "andaru")
	b.env = Env{
		StateDir: state, Dir: filepath.Join(base, "andaru-reset"), KeptRoot: b.keptRoot,
		Realizer: generation.Realizer{StateDir: state, Root: filepath.Join(base, "root"), Run: b.run, Now: time.Now, LockWait: time.Second},
		Run:      b.run, LockWait: time.Second,
		Apply: b.apply,
	}
	if err := os.MkdirAll(filepath.Join(base, "root", "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	b.oldState()
	return b
}

func (b *box) run(_ context.Context, name string, args ...string) ([]byte, error) {
	b.mu.Lock()
	command := filepath.Base(name) + " " + strings.Join(args, " ")
	b.commands = append(b.commands, command)
	stop := b.onStop
	if strings.HasPrefix(command, "systemctl stop") {
		b.onStop = nil
	}
	fail := b.fail != "" && strings.HasPrefix(command, b.fail)
	if fail {
		b.fail = ""
	}
	b.mu.Unlock()
	if fail {
		return nil, errors.New("injected failure: " + command)
	}
	if stop != nil && strings.HasPrefix(command, "systemctl stop") {
		stop()
	}
	if strings.HasPrefix(command, "systemctl is-active --quiet ") && slices.Contains(b.inactive, args[len(args)-1]) {
		return nil, errors.New("inactive")
	}
	if strings.HasPrefix(command, "systemctl show") {
		return []byte("ActiveState=inactive\nUnitFileState=static\n"), nil
	}
	return nil, nil
}

// apply is the engine: it makes the saved version a confirmed, live generation when it comes up.
func (b *box) apply(_ context.Context, saved string) (bool, error) {
	b.applied = append(b.applied, saved)
	pending := b.pending != nil && b.pending(len(b.applied))
	if !b.up(len(b.applied)) && !pending {
		return false, nil
	}
	dir := generation.Dir(b.env.StateDir)
	id, err := generation.Create(dir, generation.Manifest{SavedVersion: saved, ConfirmTimeoutSeconds: 120}, nil)
	if err != nil {
		return false, err
	}
	var p generation.Pointers
	data, err := os.ReadFile(filepath.Join(dir, "pointers.json"))
	if err == nil {
		err = json.Unmarshal(data, &p)
	}
	if err != nil {
		return false, err
	}
	if pending {
		p.Pending, p.Live = &id, &id
		write(b.t, filepath.Join(dir, itoa(id), "state.json"), `{"state":"awaiting","previous":null}`)
	} else {
		p.Confirmed, p.Live = &id, &id
	}
	data, _ = json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "pointers.json"), data, 0o600); err != nil {
		return false, err
	}
	if b.applyErr != nil && b.applyErr(len(b.applied)) {
		return false, errors.New("the confirm's answer was lost")
	}
	return !pending, nil
}

// network is the fixture declaration with its LAN as the management segment.
func network(t *testing.T) (map[string]any, map[string]string) {
	t.Helper()
	data, err := os.ReadFile("../config/testdata/network.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err = json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	lan := d["segments"].([]any)[1].(map[string]any)["id"]
	d["management"] = map[string]any{"segment": lan}
	return d, map[string]string{"WAN_PASSWORD": "TEST_ONLY_SECRET"}
}

// oldState is a taken-over host: configuration, an old administrator, a live takeover, unrelated files.
func (b *box) oldState() {
	t, ctx := b.t, context.Background()
	cs, err := config.Open(ctx, b.env.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	cs.Close()
	ids, err := identity.Open(ctx, b.env.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ids.BootstrapAdmin(ctx, "olduser", "", []byte("an-old-admin-password-41")); err != nil {
		t.Fatal(err)
	}
	ids.Close()
	dir := generation.Dir(b.env.StateDir)
	if err := os.MkdirAll(filepath.Join(dir, "baseline"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "pointers.json"), `{"confirmed":null,"lastKnownGood":null,"pending":null,"live":null,"takeover":true}`)
	write(t, filepath.Join(dir, "baseline", "manifest.json"), `{"kind":"netplan","files":[],"nftablesMasked":false}`)
	write(t, filepath.Join(b.env.StateDir, "addons", "plugin", "settings.json"), `{}`)
	if err := os.Symlink(b.upgrades, filepath.Join(b.env.StateDir, "link-out")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(b.upgrades, "recovery", "state.json"), `{"untouched":true}`)
	d, secrets := network(t)
	value, _ := json.Marshal(d)
	closure, err := config.LANClosure(value, secrets)
	if err != nil {
		t.Fatal(err)
	}
	store, err := kept.OpenIn(b.keptRoot, b.env.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Put(ctx, "last-lan", closure, true); err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"kind":"lan-closure","first":true}`)
	if err = store.Put(ctx, "first", first, false); err != nil {
		t.Fatal(err)
	}
}

// storeRecovery keeps the whole fixture, WAN included, as the post-reset network.
func (b *box) storeRecovery() {
	d, secrets := network(b.t)
	body, _ := json.Marshal(map[string]any{"declaration": d, "secrets": secrets})
	store, err := kept.OpenIn(b.keptRoot, b.env.StateDir)
	if err != nil {
		b.t.Fatal(err)
	}
	if err = store.Put(context.Background(), "recovery", body, true); err != nil {
		b.t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (b *box) plan(password string) Operation {
	b.t.Helper()
	verifier, err := identity.HashResetPassword(context.Background(), []byte(password))
	if err != nil {
		b.t.Fatal(err)
	}
	op, err := Plan(context.Background(), b.env, verifier, false, false)
	if err != nil {
		b.t.Fatal(err)
	}
	return op
}

// accounts lists the logins left in the identity store.
func (b *box) accounts() []string {
	b.t.Helper()
	ids, err := identity.Open(context.Background(), b.env.StateDir)
	if err != nil {
		b.t.Fatal(err)
	}
	defer ids.Close()
	page, err := ids.ListAccounts(context.Background(), "", 100)
	if err != nil {
		b.t.Fatal(err)
	}
	var out []string
	for _, a := range page.Items {
		out = append(out, a.Login)
	}
	return out
}

// head is the saved declaration the fresh store holds now.
func (b *box) head() map[string]any {
	b.t.Helper()
	cs, err := config.Open(context.Background(), b.env.StateDir)
	if err != nil {
		b.t.Fatal(err)
	}
	defer cs.Close()
	docs, err := cs.Export(context.Background(), false)
	if err != nil {
		b.t.Fatal(err)
	}
	var d map[string]any
	if err = json.Unmarshal(docs.Configuration, &d); err != nil {
		b.t.Fatal(err)
	}
	return d
}

func (b *box) ran(prefix string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.ContainsFunc(b.commands, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// wantDone checks what every finished reset leaves.
func (b *box) wantDone(wan bool) {
	t := b.t
	t.Helper()
	if _, err := os.Stat(journalPath(b.env.Dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.env.StateDir, MarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker left: %v", err)
	}
	if got := b.accounts(); !slices.Equal(got, []string{"admin"}) {
		t.Fatalf("accounts = %v", got)
	}
	for _, gone := range []string{"addons", "link-out"} {
		if _, err := os.Lstat(filepath.Join(b.env.StateDir, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived the wipe", gone)
		}
	}
	if data, err := os.ReadFile(filepath.Join(b.upgrades, "recovery", "state.json")); err != nil || string(data) != `{"untouched":true}` {
		t.Fatalf("the upgrade copy changed: %q %v", data, err)
	}
	store, _ := kept.OpenIn(b.keptRoot, b.env.StateDir)
	if data, err := store.Read("first"); err != nil || string(data) != `{"kind":"lan-closure","first":true}` {
		t.Fatalf("first/ changed: %q %v", data, err)
	}
	p, _, err := generation.List(generation.Dir(b.env.StateDir))
	if err != nil || !p.Takeover {
		t.Fatalf("pointers = %+v, %v", p, err)
	}
	if kind, _, err := generation.ReadBaselineKind(generation.Dir(b.env.StateDir)); err != nil || kind != "netplan" {
		t.Fatalf("ownership = %q, %v", kind, err)
	}
	head := b.head()
	_, hasWAN := head["nat"]
	segments := len(head["segments"].([]any))
	if wan != (segments == 2) {
		t.Fatalf("saved head has %d segments (nat %v), want WAN %v", segments, hasWAN, wan)
	}
	if b.ran("systemctl reboot") || !b.ran("systemctl start andaru-engine.service") {
		t.Fatalf("units = %q", b.commands)
	}
}

func TestResetOnRecovery(t *testing.T) {
	b := newBox(t)
	b.storeRecovery()
	b.onStop = func() {
		if !kept.LiveTreeUsable(b.env.StateDir) {
			t.Error("the tree was wiped before Andaru stopped")
		}
	}
	b.plan("")
	op, err := Resume(context.Background(), b.env)
	if err != nil || op.Phase != Up {
		t.Fatalf("phase %s: %v", op.Phase, err)
	}
	if len(b.applied) != 1 {
		t.Fatalf("applied %v", b.applied)
	}
	b.wantDone(true)
	ids, _ := identity.Open(context.Background(), b.env.StateDir)
	defer ids.Close()
	page, _ := ids.ListAccounts(context.Background(), "", 10)
	if ok, err := ids.VerifyPassword(context.Background(), page.Items[0].ID, []byte("admin")); err != nil || !ok {
		t.Fatalf("admin/admin: %v %v", ok, err)
	}
}

func TestRecoveryFailsToLAN(t *testing.T) {
	b := newBox(t)
	b.storeRecovery()
	b.up = func(n int) bool { return n == 2 }
	b.plan("")
	op, err := Resume(context.Background(), b.env)
	if err != nil || op.Phase != Up || len(b.applied) != 2 {
		t.Fatalf("phase %s, applied %v: %v", op.Phase, b.applied, err)
	}
	b.wantDone(false)
}

func TestNoRecovery(t *testing.T) {
	b := newBox(t)
	b.plan("")
	op, err := Resume(context.Background(), b.env)
	if err != nil || op.Phase != Up || len(b.applied) != 1 {
		t.Fatalf("phase %s, applied %v: %v", op.Phase, b.applied, err)
	}
	b.wantDone(false)
}

func TestConsoleRequired(t *testing.T) {
	b := newBox(t)
	b.storeRecovery()
	b.up = func(int) bool { return false }
	b.plan("")
	op, err := Resume(context.Background(), b.env)
	if op.Phase != ConsoleRequired || err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") {
		t.Fatalf("phase %s: %v", op.Phase, err)
	}
	if got := b.accounts(); !slices.Equal(got, []string{"admin"}) {
		t.Fatalf("accounts = %v", got)
	}
	if p, _, _ := generation.List(generation.Dir(b.env.StateDir)); p.Confirmed != nil {
		t.Fatalf("a generation was confirmed: %+v", p)
	}
	if !b.ran("nft -f") {
		t.Fatalf("deny-all not loaded: %q", b.commands)
	}
	if _, err := os.Stat(journalPath(b.env.Dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal left: %v", err)
	}
}

func TestNoTakeoverAppliesNothing(t *testing.T) {
	b := newBox(t)
	write(t, filepath.Join(generation.Dir(b.env.StateDir), "pointers.json"), `{"confirmed":null,"lastKnownGood":null,"pending":null,"live":null,"takeover":false}`)
	b.storeRecovery()
	b.plan("")
	op, err := Resume(context.Background(), b.env)
	if err != nil || op.Phase != Up || len(b.applied) != 0 {
		t.Fatalf("phase %s, applied %v: %v", op.Phase, b.applied, err)
	}
}

func TestEnteredPasswordAndReboot(t *testing.T) {
	b := newBox(t)
	verifier, err := identity.HashResetPassword(context.Background(), []byte("unique-reset-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Plan(context.Background(), b.env, verifier, true, true); err != nil {
		t.Fatal(err)
	}
	journal, _ := os.ReadFile(journalPath(b.env.Dir))
	if strings.Contains(string(journal), "unique-reset-password-41") {
		t.Fatal("the journal holds the password")
	}
	if _, err = Resume(context.Background(), b.env); err != nil {
		t.Fatal(err)
	}
	if !b.ran("systemctl reboot") {
		t.Fatalf("no reboot: %q", b.commands)
	}
}

func TestPlanRefuses(t *testing.T) {
	cases := map[string]func(b *box){
		"unusable tree": func(b *box) { os.RemoveAll(filepath.Join(b.env.StateDir, "configuration")) },
		"pending apply": func(b *box) {
			write(b.t, filepath.Join(generation.Dir(b.env.StateDir), "pointers.json"), `{"confirmed":null,"lastKnownGood":null,"pending":1,"live":null,"takeover":true}`)
		},
		"dpkg": func(b *box) {
			b.env.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "dpkg" {
					return []byte("The following packages are only half configured\n"), nil
				}
				return b.run(ctx, name, args...)
			}
		},
		"existing journal": func(b *box) { b.plan("") },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			b := newBox(t)
			breakIt(b)
			before, _ := os.ReadFile(journalPath(b.env.Dir))
			verifier, _ := identity.HashResetPassword(context.Background(), nil)
			if _, err := Plan(context.Background(), b.env, verifier, false, false); err == nil {
				t.Fatal("planned")
			}
			after, _ := os.ReadFile(journalPath(b.env.Dir))
			if string(before) != string(after) || b.ran("systemctl stop") {
				t.Fatalf("the refusal changed something: %q", b.commands)
			}
		})
	}
}

func TestResumeRechecksAtPlanned(t *testing.T) {
	b := newBox(t)
	b.plan("")
	write(t, filepath.Join(generation.Dir(b.env.StateDir), "pointers.json"), `{"confirmed":null,"lastKnownGood":null,"pending":1,"live":null,"takeover":true}`)
	if _, err := Resume(context.Background(), b.env); err == nil {
		t.Fatal("resumed over a pending apply")
	}
	if b.ran("systemctl stop") {
		t.Fatalf("stopped units: %q", b.commands)
	}
	if active, _, _ := Active(b.env.Dir); active {
		t.Fatal("the refused reset left its journal")
	}
	if !kept.LiveTreeUsable(b.env.StateDir) {
		t.Fatal("the refused reset touched the tree")
	}
}

func TestAbortOnlyAtPlanned(t *testing.T) {
	b := newBox(t)
	b.plan("")
	if err := Abort(b.env); err != nil {
		t.Fatal(err)
	}
	if active, _, _ := Active(b.env.Dir); active {
		t.Fatal("journal left after abort")
	}
	op := b.plan("")
	op.Phase = Stopped
	if err := writeJournal(b.env.Dir, op); err != nil {
		t.Fatal(err)
	}
	if err := Abort(b.env); err == nil {
		t.Fatal("aborted a reset that had stopped Andaru")
	}
}

func TestMarkerRequired(t *testing.T) {
	cases := map[string]func(b *box){
		"missing mount": func(b *box) { os.RemoveAll(b.env.StateDir) },
		"other reset":   func(b *box) { write(b.t, filepath.Join(b.env.StateDir, MarkerName), "another") },
		"symlink": func(b *box) {
			os.Remove(filepath.Join(b.env.StateDir, MarkerName))
			os.Symlink(journalPath(b.env.Dir), filepath.Join(b.env.StateDir, MarkerName))
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			b := newBox(t)
			b.plan("")
			breakIt(b)
			if _, err := Resume(context.Background(), b.env); err == nil {
				t.Fatal("resumed on the wrong tree")
			}
			if b.ran("systemctl stop") {
				t.Fatalf("stopped units: %q", b.commands)
			}
			if name == "missing mount" {
				if _, err := os.Stat(b.env.StateDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("the missing tree was created")
				}
			}
		})
	}
}

func TestMarkerOrder(t *testing.T) {
	b := newBox(t)
	b.env.failAt = func(step string) error {
		if step == "plan:journal" {
			return errors.New("crash")
		}
		return nil
	}
	verifier, _ := identity.HashResetPassword(context.Background(), nil)
	if _, err := Plan(context.Background(), b.env, verifier, false, false); err == nil {
		t.Fatal("no crash")
	}
	if active, _, _ := Active(b.env.Dir); active {
		t.Fatal("journal written before the marker synced")
	}
	b.env.failAt = func(step string) error {
		if step == "drop:marker" {
			return errors.New("crash")
		}
		return nil
	}
	b.plan("")
	if err := Abort(b.env); err == nil {
		t.Fatal("no crash")
	}
	if active, _, _ := Active(b.env.Dir); active {
		t.Fatal("journal left behind its marker")
	}
	b.env.failAt = nil
	op := b.plan("")
	if err := checkMarker(b.env.StateDir, op.ID); err != nil {
		t.Fatalf("the next plan did not replace the stale marker: %v", err)
	}
}

// TestResumeAtEveryStep crashes once at each step, then resumes, and expects call 8's outcome.
func TestResumeAtEveryStep(t *testing.T) {
	steps := map[string]bool{ // step: whether the outcome is still the recovery copy
		string(Stopped): true, string(TornDown): true, "wipe:entry": true, string(Wiped): true, "fresh:audit": true,
		string(Fresh): true, string(ApplyingRecovery): true, "apply:recovery": false, string(Up): true,
		"finish:drop": true,
	}
	for step, recovery := range steps {
		t.Run(step, func(t *testing.T) {
			b := newBox(t)
			b.storeRecovery()
			b.plan("")
			crashed := false
			b.env.failAt = func(s string) error {
				if s == step && !crashed {
					crashed = true
					return errors.New("crash at " + s)
				}
				return nil
			}
			if _, err := Resume(context.Background(), b.env); err == nil || !crashed {
				t.Fatalf("no crash at %s: %v", step, err)
			}
			if _, err := Resume(context.Background(), b.env); err != nil {
				t.Fatalf("resume: %v", err)
			}
			b.wantDone(recovery)
		})
	}
}

func TestWipeKeepsMountPoint(t *testing.T) {
	b := newBox(t)
	op := b.plan("")
	before, _ := os.Stat(b.env.StateDir)
	if err := wipe(b.env.StateDir, MarkerName, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(b.env.StateDir)
	if err != nil || after.Mode() != before.Mode() {
		t.Fatalf("state directory %v: %v", after, err)
	}
	names, _ := os.ReadDir(b.env.StateDir)
	if len(names) != 1 || names[0].Name() != MarkerName {
		t.Fatalf("left %v", names)
	}
	if err := checkMarker(b.env.StateDir, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.upgrades, "recovery", "state.json")); err != nil {
		t.Fatalf("the symlink's target went: %v", err)
	}
}

func itoa(n int) string { data, _ := json.Marshal(n); return string(data) }

// A recovery apply whose confirm never happens is rolled back, so the last LAN can be applied after it.
func TestUnconfirmedRecoveryRollsBack(t *testing.T) {
	b := newBox(t)
	b.storeRecovery()
	b.pending = func(n int) bool { return n == 1 }
	b.plan("")
	op, err := Resume(context.Background(), b.env)
	if err != nil || op.Phase != Up || len(b.applied) != 2 {
		t.Fatalf("phase %s, applied %v: %v", op.Phase, b.applied, err)
	}
	p, _, _ := generation.List(generation.Dir(b.env.StateDir))
	if p.Pending != nil {
		t.Fatalf("the unconfirmed recovery is still pending: %+v", p)
	}
	b.wantDone(false)
}

func TestAbortRefusedWhileRunning(t *testing.T) {
	b := newBox(t)
	b.plan("")
	fence, err := lockFile(filepath.Join(b.env.Dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	if err := Abort(b.env); err == nil {
		t.Fatal("aborted a running reset")
	}
	if active, _, _ := Active(b.env.Dir); !active {
		t.Fatal("the journal went")
	}
}

func TestOnlyActiveUnitsStart(t *testing.T) {
	b := newBox(t)
	b.inactive = []string{"andaru-web.service"}
	b.plan("")
	if _, err := Resume(context.Background(), b.env); err != nil {
		t.Fatal(err)
	}
	if b.ran("systemctl start andaru-web.service") || !b.ran("systemctl start andaru-engine.service") {
		t.Fatalf("units = %q", b.commands)
	}
}

func TestAbortRefusedOnceStopping(t *testing.T) {
	b := newBox(t)
	b.plan("")
	b.fail = "systemctl stop andaru-leases.path"
	if _, err := Resume(context.Background(), b.env); err == nil {
		t.Fatal("no failure")
	}
	if err := Abort(b.env); err == nil {
		t.Fatal("aborted a reset that had stopped the web service")
	}
}

// The units that ran are sampled once: a retry after a stop failed restarts them, not what was left running.
func TestUnitSampleSurvivesRetry(t *testing.T) {
	b := newBox(t)
	b.plan("")
	b.fail = "systemctl stop andaru-leases.path"
	if _, err := Resume(context.Background(), b.env); err == nil {
		t.Fatal("no failure")
	}
	b.inactive = []string{"andaru-web.service"} // stopped by the attempt that failed
	if _, err := Resume(context.Background(), b.env); err != nil {
		t.Fatal(err)
	}
	if !b.ran("systemctl start andaru-web.service") {
		t.Fatalf("web stayed down: %q", b.commands)
	}
}

func TestApplyPendingWhileStoppingDropsReset(t *testing.T) {
	b := newBox(t)
	b.plan("")
	b.onStop = func() {
		write(b.t, filepath.Join(generation.Dir(b.env.StateDir), "pointers.json"), `{"confirmed":null,"lastKnownGood":null,"pending":1,"live":null,"takeover":true}`)
	}
	if _, err := Resume(context.Background(), b.env); err == nil {
		t.Fatal("wiped over an apply that went pending")
	}
	if active, _, _ := Active(b.env.Dir); active {
		t.Fatal("the journal stayed")
	}
	if !kept.LiveTreeUsable(b.env.StateDir) || !b.ran("systemctl start andaru-web.service") {
		t.Fatalf("tree touched or units left stopped: %q", b.commands)
	}
}

func TestLostConfirmAnswerStillUp(t *testing.T) {
	b := newBox(t)
	b.storeRecovery()
	b.applyErr = func(n int) bool { return n == 1 }
	b.plan("")
	op, err := Resume(context.Background(), b.env)
	if err != nil || op.Phase != Up || len(b.applied) != 1 {
		t.Fatalf("phase %s, applied %v: %v", op.Phase, b.applied, err)
	}
	b.wantDone(true)
}

func TestResumeNeedsDenyAll(t *testing.T) {
	b := newBox(t)
	b.plan("")
	b.env.failAt = func(s string) error {
		if s == "fresh:audit" {
			return errors.New("crash")
		}
		return nil
	}
	if _, err := Resume(context.Background(), b.env); err == nil {
		t.Fatal("no crash")
	}
	b.env.failAt = nil
	b.fail = "nft -f"
	if _, err := Resume(context.Background(), b.env); err == nil {
		t.Fatal("resumed without deny-all")
	}
	if _, phase, _ := Active(b.env.Dir); phase != Wiped {
		t.Fatalf("phase %s", phase)
	}
	if _, err := Resume(context.Background(), b.env); err != nil {
		t.Fatal(err)
	}
}

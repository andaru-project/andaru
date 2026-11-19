package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/identity"
	"github.com/andaru-project/andaru/internal/reset"
)

type resetBench struct {
	mu       sync.Mutex
	commands []string
	env      reset.Env
}

func (b *resetBench) ran(prefix string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.ContainsFunc(b.commands, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// newResetBench points the reset at a temporary tree with a fake systemd.
func newResetBench(t *testing.T) *resetBench {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("the reset requires root")
	}
	base := t.TempDir()
	b := &resetBench{}
	oldDir, oldState, oldSeam := resetDir, resetStateDir, resetEnvSeam
	resetDir, resetStateDir = filepath.Join(base, "andaru-reset"), filepath.Join(base, "andaru")
	if err := os.MkdirAll(filepath.Join(resetStateDir, "configuration"), 0o700); err != nil {
		t.Fatal(err)
	}
	b.env = reset.Env{StateDir: resetStateDir, Dir: resetDir, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.commands = append(b.commands, filepath.Base(name)+" "+strings.Join(args, " "))
		return nil, nil
	}}
	resetEnvSeam = func(string) reset.Env { return b.env }
	t.Cleanup(func() { resetDir, resetStateDir, resetEnvSeam = oldDir, oldState, oldSeam })
	return b
}

func (b *resetBench) plan(t *testing.T, phase reset.Phase, live bool) {
	t.Helper()
	verifier, _ := identity.HashResetPassword(context.Background(), nil)
	if _, err := reset.Plan(context.Background(), b.env, verifier, false, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(resetDir, "operation.json")
	data, _ := os.ReadFile(path)
	var op map[string]any
	json.Unmarshal(data, &op)
	op["phase"] = phase
	op["ownership"] = map[string]any{"live": live}
	op["ownershipKnown"] = phase != reset.Planned
	data, _ = json.Marshal(op)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResetUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"run", "--yes"}, {"go"}, {"run", "now"}} {
		var stderr bytes.Buffer
		if code := runFactoryReset(context.Background(), "/run/x.sock", args, &bytes.Buffer{}, &stderr); code != 2 {
			t.Fatalf("%q: code %d %s", args, code, stderr.String())
		}
	}
}

func TestFenceDuringReset(t *testing.T) {
	b := newResetBench(t)
	b.plan(t, reset.TornDown, true)
	for _, verb := range []string{"leases", "prefixes", "activate 3", "confirm 3", "rollback 3 timer"} {
		verbs := &fakeVerbs{}
		var stderr bytes.Buffer
		if code := realizeCommand(context.Background(), strings.Fields(verb), &stderr, func(string) (realizeVerbs, error) { return verbs, nil }); code != 0 || len(verbs.calls) != 0 {
			t.Fatalf("%s ran during the wipe: code %d calls %v", verb, code, verbs.calls)
		}
	}
	verbs := &fakeVerbs{}
	realizeCommand(context.Background(), []string{"boot"}, &bytes.Buffer{}, func(string) (realizeVerbs, error) { return verbs, nil })
	if !slices.Equal(verbs.calls, []string{"fence true"}) {
		t.Fatalf("boot during the wipe: %v", verbs.calls)
	}
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"engine", "--socket", filepath.Join(t.TempDir(), "e.sock"), "--state-dir", resetStateDir}, nil, &bytes.Buffer{}, &stderr); code != 0 || !strings.Contains(stderr.String(), "factory reset is in progress") {
		t.Fatalf("engine during the wipe: code %d: %s", code, stderr.String())
	}
}

func TestFenceOpensOnFreshState(t *testing.T) {
	b := newResetBench(t)
	b.plan(t, reset.ApplyingRecovery, true)
	verbs := &fakeVerbs{}
	realizeCommand(context.Background(), []string{"boot"}, &bytes.Buffer{}, func(string) (realizeVerbs, error) { return verbs, nil })
	realizeCommand(context.Background(), []string{"leases"}, &bytes.Buffer{}, func(string) (realizeVerbs, error) { return verbs, nil })
	if !slices.Equal(verbs.calls, []string{"boot", "leases"}) {
		t.Fatalf("calls on the fresh state: %v", verbs.calls)
	}
}

// answer types each line at its prompt on the test terminal and returns the CLI's exit code.
func answer(t *testing.T, args []string, lines ...[2]string) (int, string) {
	t.Helper()
	master, _, restore := withBackupPTY(t)
	defer restore()
	code := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() { code <- runFactoryReset(context.Background(), "/run/none.sock", args, &stdout, &stderr) }()
	var transcript bytes.Buffer
	for _, l := range lines {
		awaitBackupPromptOrExit(t, master, l[0], &transcript, code)
		master.Write([]byte(l[1] + "\n"))
	}
	select {
	case c := <-code:
		return c, stdout.String() + stderr.String()
	case <-time.After(10 * time.Second):
		t.Fatal("the CLI did not finish")
		return 0, ""
	}
}

func TestResetPrompts(t *testing.T) {
	b := newResetBench(t)
	code, out := answer(t, []string{"run"},
		[2]string{"New admin password", ""}, [2]string{"portable backup", ""}, [2]string{"Reboot", "n"}, [2]string{"FACTORY RESET", "FACTORY RESET"})
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	if active, phase, _ := reset.Active(resetDir); !active || phase != reset.Planned {
		t.Fatalf("journal: %v %s", active, phase)
	}
	if !b.ran("systemctl start --no-block andaru-factory-reset.service") || b.ran("systemctl stop") {
		t.Fatalf("commands: %q", b.commands)
	}
	data, _ := os.ReadFile(filepath.Join(resetDir, "operation.json"))
	var op reset.Operation
	json.Unmarshal(data, &op)
	if op.Reboot || op.Backup || len(op.Verifier) == 0 {
		t.Fatalf("operation = %+v", op)
	}
}

func TestResetCancelled(t *testing.T) {
	cases := map[string][][2]string{
		"wrong phrase":       {{"New admin password", ""}, {"portable backup", ""}, {"Reboot", ""}, {"FACTORY RESET", "factory reset"}},
		"mismatch":           {{"New admin password", "unique-reset-password-41"}, {"Repeat", "unique-reset-password-42"}},
		"weak":               {{"New admin password", "short"}, {"Repeat", "short"}},
		"backup in the tree": {{"New admin password", ""}, {"portable backup", "/var/lib/andaru-kept/backup.zip"}},
		"bad answer":         {{"New admin password", ""}, {"portable backup", ""}, {"Reboot", "maybe"}},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			b := newResetBench(t)
			if code, out := answer(t, []string{"run"}, lines...); code == 0 {
				t.Fatalf("not cancelled: %s", out)
			}
			if active, _, _ := reset.Active(resetDir); active || b.ran("systemctl") {
				t.Fatalf("the cancelled reset left work: %q", b.commands)
			}
		})
	}
}

// A host whose network Andaru never ran still gets a filter: on the image its factory network runs behind it.
func TestFenceFiltersAHostAndaruNeverRan(t *testing.T) {
	b := newResetBench(t)
	b.plan(t, reset.Wiped, false)
	verbs := &fakeVerbs{}
	realizeCommand(context.Background(), []string{"boot"}, &bytes.Buffer{}, func(string) (realizeVerbs, error) { return verbs, nil })
	if !slices.Equal(verbs.calls, []string{"fence false"}) {
		t.Fatalf("boot during the wipe of a host Andaru never ran: %v", verbs.calls)
	}
}

// A planned reset has stopped nothing: an apply's timer and the engine still run.
func TestPlannedResetFencesNothing(t *testing.T) {
	b := newResetBench(t)
	b.plan(t, reset.Planned, true)
	verbs := &fakeVerbs{}
	realizeCommand(context.Background(), []string{"rollback", "3", "timer"}, &bytes.Buffer{}, func(string) (realizeVerbs, error) { return verbs, nil })
	if !slices.Equal(verbs.calls, []string{"rollback 3 timer"}) {
		t.Fatalf("calls %v", verbs.calls)
	}
}

// Once the reset journals that it begins stopping Andaru, nothing may start the engine or an apply behind it.
func TestStoppingResetFences(t *testing.T) {
	b := newResetBench(t)
	b.plan(t, reset.Planned, true)
	path := filepath.Join(resetDir, "operation.json")
	data, _ := os.ReadFile(path)
	var op map[string]any
	json.Unmarshal(data, &op)
	op["stopping"] = true
	data, _ = json.Marshal(op)
	os.WriteFile(path, data, 0o600)
	verbs := &fakeVerbs{}
	realizeCommand(context.Background(), []string{"activate", "3"}, &bytes.Buffer{}, func(string) (realizeVerbs, error) { return verbs, nil })
	if len(verbs.calls) != 0 {
		t.Fatalf("an apply ran behind a stopping reset: %v", verbs.calls)
	}
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"engine", "--socket", filepath.Join(t.TempDir(), "e.sock"), "--state-dir", resetStateDir}, nil, &bytes.Buffer{}, &stderr); code != 0 || !strings.Contains(stderr.String(), "factory reset is in progress") {
		t.Fatalf("engine behind a stopping reset: code %d: %s", code, stderr.String())
	}
}

// A reset that began stopping but has not read ownership yet cannot say Andaru never ran the network: boot shuts it.
func TestUnknownOwnershipDeniesAll(t *testing.T) {
	b := newResetBench(t)
	b.plan(t, reset.Planned, false)
	path := filepath.Join(resetDir, "operation.json")
	data, _ := os.ReadFile(path)
	var op map[string]any
	json.Unmarshal(data, &op)
	op["stopping"] = true
	data, _ = json.Marshal(op)
	os.WriteFile(path, data, 0o600)
	verbs := &fakeVerbs{}
	realizeCommand(context.Background(), []string{"boot"}, &bytes.Buffer{}, func(string) (realizeVerbs, error) { return verbs, nil })
	if !slices.Equal(verbs.calls, []string{"fence true"}) {
		t.Fatalf("boot with unknown ownership: %v", verbs.calls)
	}
}

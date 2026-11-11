// Bubbletea v1.3.6 closes its input on Ctrl+C while its read loop still uses it, which the race
// detector reports; these tests run in the plain `go test ./...` instead.
//go:build !race

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/andaru-project/andaru/internal/config"
)

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"eth0 10.0.1.19/16": "eth0 10.0.1.19/16",
		"évian ünïcode":     "évian ünïcode",
		"a\x1b[2Jb":         `a\x1b[2Jb`,
		"tab\there\n":       `tab\x09here\x0a`,
		"bidi\u202ecode":    `bidi\u202ecode`,
		"bad\xffbyte":       `bad\ufffdbyte`,
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}

// screen is a terminal the assistant runs on, with its output collected for markers.
type screen struct {
	t             *testing.T
	master        *os.File
	mu            sync.Mutex
	out           bytes.Buffer
	seen          int
	stop, stopped chan struct{}
}

// collect polls rather than blocks: a read blocked on the master would keep it open past hangup.
func (s *screen) collect() {
	defer close(s.stopped)
	buf := make([]byte, 4096)
	fds := []unix.PollFd{{Fd: int32(s.master.Fd()), Events: unix.POLLIN}}
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		if n, _ := unix.Poll(fds, 50); n <= 0 {
			continue
		}
		n, err := unix.Read(int(fds[0].Fd), buf)
		if err != nil || n == 0 {
			return
		}
		s.mu.Lock()
		s.out.Write(buf[:n])
		s.mu.Unlock()
	}
}

// hangup closes the terminal as a dropped session would.
func (s *screen) hangup() {
	close(s.stop)
	<-s.stopped
	s.master.Close()
}

func (s *screen) await(marker string) {
	s.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		s.mu.Lock()
		i := strings.Index(s.out.String()[s.seen:], marker)
		if i >= 0 {
			s.seen += i + len(marker)
		}
		s.mu.Unlock()
		if i >= 0 {
			time.Sleep(50 * time.Millisecond) // let the screen settle before typing
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.t.Fatalf("never saw %q in:\n%s", marker, s.out.String())
}

func (s *screen) press(keys ...string) {
	for _, k := range keys {
		if _, err := s.master.WriteString(k); err != nil {
			s.t.Fatal(err)
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// runAssistant starts the assistant on a fresh pseudo-terminal and returns its screen and exit code.
func runAssistant(t *testing.T, c *fakeClient) (*screen, <-chan int) {
	return runAssistantSized(t, c, &pty.Winsize{Rows: 60, Cols: 200})
}

// runAssistantSized starts the assistant on a terminal of the given size; nil leaves it 0x0, as a serial console reports.
func runAssistantSized(t *testing.T, c *fakeClient, size *pty.Winsize) (*screen, <-chan int) {
	t.Setenv("TERM", "xterm-256color") // huh falls back to numbered prompts on a dumb terminal
	master, tty, err := pty.Open()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	if size != nil {
		if err := pty.Setsize(master, size); err != nil {
			t.Fatal(err)
		}
	}
	s := &screen{t: t, master: master, stop: make(chan struct{}), stopped: make(chan struct{})}
	go s.collect()
	done := make(chan int, 1)
	p := labProposal(t)
	go func() {
		done <- interactive(context.Background(), c, p, 120, tty, tty, func() {})
		tty.Close()
	}()
	t.Cleanup(func() {
		select {
		case <-s.stop:
		default:
			s.hangup()
		}
	})
	return s, done
}

func exit(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("the assistant did not return")
		return -1
	}
}

const enter, down, ctrlC = "\r", "\x1b[B", "\x03"

// acceptAll walks every screen of the lab proposal, accepting the pre-filled values.
var acceptAll = []struct{ marker, keys []string }{
	{[]string{"Network adapters to take over"}, []string{enter}},
	{[]string{"eth0: zone"}, []string{enter, enter}},
	{[]string{"eth0: IPv4 addresses"}, []string{enter, enter}},
	{[]string{"eth0: IPv6"}, []string{enter}},
	{[]string{"eth1: zone"}, []string{enter, enter}},
	{[]string{"eth1: IPv6"}, []string{enter}},
	{[]string{"DNS servers for this host"}, []string{enter}},
	{[]string{"Search domains"}, []string{enter, enter}},
	{[]string{"Seconds to confirm"}, []string{enter}},
}

func walk(s *screen, upTo int) {
	for _, step := range acceptAll[:upTo] {
		s.await(step.marker[0])
		s.press(step.keys...)
	}
}

func TestAssistantAppliesExactlyTheDetectedBatch(t *testing.T) {
	c := &fakeClient{apply: applyReply{ID: "1", State: "awaiting_confirmation", Deadline: "d"}}
	s, done := runAssistant(t, c)
	walk(s, len(acceptAll))
	s.await("What now?")
	s.press(down, down, enter)
	if code := exit(t, done); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if want := []string{"draft", "applies", "draft", "save", "apply 10"}; !slices.Equal(c.calls, want) {
		t.Fatalf("calls = %v, want %v", c.calls, want)
	}
	if got, want := opsJSON(t, c.saved), opsJSON(t, labProposal(t).Operations); got != want {
		t.Fatalf("saved\n%s\nwant\n%s", got, want)
	}
	if c.head != "9" {
		t.Fatalf("save guarded by %q, want the proposal's version 9", c.head)
	}
}

func TestAssistantWorksOnATerminalWithoutSize(t *testing.T) {
	c := &fakeClient{apply: applyReply{ID: "1", State: "awaiting_confirmation", Deadline: "d"}}
	s, done := runAssistantSized(t, c, nil)
	walk(s, len(acceptAll))
	s.await("What now?")
	s.press(down, down, enter)
	if code := exit(t, done); code != 0 || !slices.Contains(c.calls, "save") {
		t.Fatalf("exit %d, calls %v", code, c.calls)
	}
}

func TestAssistantWritesNothingUnlessApplyIsChosen(t *testing.T) {
	cases := map[string]func(*screen){
		"enter at review goes back": func(s *screen) {
			walk(s, len(acceptAll))
			s.await("What now?")
			s.press(enter)
			s.await("Network adapters to take over")
			s.press(ctrlC)
		},
		"quit at review": func(s *screen) {
			walk(s, len(acceptAll))
			s.await("What now?")
			s.press(down, enter)
		},
	}
	for i, step := range acceptAll {
		cases["interrupt at "+step.marker[0]] = func(s *screen) {
			walk(s, i)
			s.await(step.marker[0])
			s.press(ctrlC)
		}
	}
	for name, drive := range cases {
		t.Run(name, func(t *testing.T) {
			c := &fakeClient{}
			s, done := runAssistant(t, c)
			drive(s)
			if code := exit(t, done); code == 0 {
				t.Fatal("exit 0 without applying")
			}
			if slices.ContainsFunc(c.calls, func(call string) bool { return call == "save" || strings.HasPrefix(call, "apply") }) {
				t.Fatalf("wrote: %v", c.calls)
			}
		})
	}
}

func TestAssistantOffersNoApplyWhenTheDraftIsRefused(t *testing.T) {
	c := &fakeClient{draft: draft{ApplyFindings: []finding{{Severity: "error", Code: "WOULD_UNCONFIGURE", Path: "eth0", Message: "x"}}}}
	s, done := runAssistant(t, c)
	walk(s, len(acceptAll))
	s.await("What now?")
	s.press(down, enter)
	if code := exit(t, done); code == 0 || slices.Contains(c.calls, "save") {
		t.Fatalf("exit %d, calls %v", code, c.calls)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if out := s.out.String(); !strings.Contains(out, "WOULD_UNCONFIGURE") || strings.Contains(out, "Apply:") {
		t.Fatalf("want the refusal shown and no Apply offered:\n%s", out)
	}
}

// recordingClient marks every write in a file, so a killed process leaves evidence.
type recordingClient struct {
	fakeClient
	marks string
}

func (r *recordingClient) Save(ctx context.Context, ops []config.Operation, head config.Version) (config.Version, error) {
	os.WriteFile(r.marks, []byte("save"), 0o600)
	return r.fakeClient.Save(ctx, ops, head)
}

// TestAssistantHelper is the assistant run as its own process by TestClosingTheTerminalWritesNothing.
func TestAssistantHelper(t *testing.T) {
	marks := os.Getenv("ANDARU_ASSISTANT_MARKS")
	if marks == "" {
		t.Skip("helper process only")
	}
	c := &recordingClient{marks: marks}
	os.Exit(interactive(context.Background(), c, labProposal(t), 120, os.Stdin, os.Stdout, func() {}))
}

func TestClosingTheTerminalWritesNothing(t *testing.T) {
	marks := filepath.Join(t.TempDir(), "marks")
	// The deadline kills a helper that ignores the hangup, so a regression fails here instead of stalling the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAssistantHelper$")
	cmd.Env = append(os.Environ(), "ANDARU_ASSISTANT_MARKS="+marks, "TERM=xterm-256color")
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 60, Cols: 200})
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	s := &screen{t: t, master: master, stop: make(chan struct{}), stopped: make(chan struct{})}
	go s.collect()
	t.Cleanup(func() {
		select {
		case <-s.stop:
		default:
			s.hangup()
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	walk(s, len(acceptAll))
	s.await("What now?")
	s.hangup()
	err = cmd.Wait()
	var exitErr *exec.ExitError
	// The hangup ends it either by SIGHUP or, when the read sees the closed terminal first, by a failing exit.
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("want the assistant ended by the hangup, got %v", err)
	}
	if _, err := os.Stat(marks); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("something was saved after the terminal closed")
	}
}

// huh turns a dumb terminal into line prompts whose EOF reads as Back, which would loop forever.
func TestTakeoverRefusesADumbTerminal(t *testing.T) {
	_, tty, err := pty.Open()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	defer tty.Close()
	t.Setenv("TERM", "dumb")
	var diag bytes.Buffer
	if code := runTakeover(context.Background(), filepath.Join(t.TempDir(), "absent.sock"), nil, tty, tty, &diag); code != 2 || !strings.Contains(diag.String(), "--accept-detected") {
		t.Fatalf("exit %d: %s", code, diag.String())
	}
}

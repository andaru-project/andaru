package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"

	"golang.org/x/sys/unix"

	"github.com/andaru-project/andaru/internal/config"
)

type adapter struct {
	Name, PermanentMac, Summary string
	Addressed                   bool
	// Required: ifupdown configures it, and the takeover masks ifupdown for every interface.
	Required bool
}

type finding struct{ Severity, Code, Path, Message string }

type proposal struct {
	State        string             `json:"state"`
	SavedVersion config.Version     `json:"savedVersion"`
	Operations   []config.Operation `json:"operations"`
	Adapters     []adapter          `json:"adapters"`
	Findings     []finding          `json:"findings"`
}

type draft struct {
	SavedVersion config.Version
	Files        []struct{ Path, Mode, Content string }
	Findings     []finding
	// ApplyFindings is what the apply gates would refuse, including the preview's errors that a takeover does not resolve.
	ApplyFindings []finding
	Rules         []struct {
		Chain, Name, Action string
		Position            int
		From, To, Services  []string
	}
}

var errHeadMoved = errors.New("the saved configuration changed")

type apiError struct {
	Status        int
	Code, Message string
	Details       []config.FieldError
	Findings      []finding
}

func (e *apiError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", e.Code, e.Message)
	for _, d := range e.Details {
		fmt.Fprintf(&b, "\n  %s: %s", printable(d.Path), printable(d.Message))
	}
	for _, f := range e.Findings {
		fmt.Fprintf(&b, "\n  %s", findingLine(f))
	}
	return b.String()
}

func (e *apiError) Is(target error) bool {
	return target == errHeadMoved && e.Status == http.StatusPreconditionFailed
}

// printable escapes what a host or engine string could use to drive the terminal.
func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == unicode.ReplacementChar:
			b.WriteString(`\ufffd`)
		case unicode.IsPrint(r) || r == ' ':
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

func findingLine(f finding) string {
	line := f.Severity + " " + f.Code
	if f.Path != "" {
		line += " " + printable(f.Path)
	}
	return line + ": " + printable(f.Message)
}

func errorFindings(list []finding) []finding {
	var out []finding
	for _, f := range list {
		if f.Severity == "error" {
			out = append(out, f)
		}
	}
	return out
}

// shownFindings are the gates' refusals and the preview's warnings; the preview's errors reach the gates.
func (d draft) shownFindings() []finding {
	return append(slices.DeleteFunc(slices.Clone(d.Findings), func(f finding) bool { return f.Severity == "error" }), d.ApplyFindings...)
}

type takeoverClient interface {
	Proposal(ctx context.Context) (proposal, error)
	Draft(ctx context.Context, ops []config.Operation) (draft, error)
	Save(ctx context.Context, ops []config.Operation, head config.Version) (config.Version, error)
	Applies(ctx context.Context) (pendingID string, err error)
	Apply(ctx context.Context, saved config.Version, timeout int) (applyReply, error)
}

// commit saves the batch, guarded by the head the proposal was built on, and applies it as a takeover.
// hold runs just before the save: from there the process must outlive a dropped session.
func commit(ctx context.Context, c takeoverClient, p proposal, ops []config.Operation, timeout int, out io.Writer, hold func()) int {
	pending, err := c.Applies(ctx)
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	if pending != "" {
		fmt.Fprintf(out, "Apply %s is awaiting confirmation; confirm it or roll it back first.\n", printable(pending))
		return 1
	}
	d, err := c.Draft(ctx, ops)
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	if refused := errorFindings(d.ApplyFindings); len(refused) > 0 {
		fmt.Fprintln(out, "This cannot be applied:")
		for _, f := range refused {
			fmt.Fprintln(out, "  "+findingLine(f))
		}
		return 1
	}
	fmt.Fprintln(out, "Saving and applying. If this session drops, reconnect and run: andaru ctl applies list")
	hold()
	saved, err := c.Save(ctx, ops, p.SavedVersion)
	if errors.Is(err, errHeadMoved) {
		fmt.Fprintln(out, "The configuration was saved meanwhile from another session; nothing changed. Run andaru ctl takeover again.")
		return 1
	}
	var refused *apiError
	if err != nil {
		fmt.Fprintln(out, err)
		if !errors.As(err, &refused) {
			fmt.Fprintln(out, "The save may have happened anyway; check with: andaru ctl versions list")
		}
		return 1
	}
	reply, err := c.Apply(ctx, saved, timeout)
	if err != nil && !errors.As(err, &refused) {
		fmt.Fprintf(out, "The configuration is saved as version %s; whether the apply started is unknown: %s\nCheck with: andaru ctl applies list\n", saved, printable(err.Error()))
		return 1
	}
	if err != nil || reply.State != "awaiting_confirmation" {
		reason := reply.Reason
		if err != nil {
			reason = err.Error()
		}
		fmt.Fprintf(out, "The configuration is saved as version %s but not live: %s\nRetry with: andaru ctl apply network --saved-version %s --takeover\n", saved, printable(reason), saved)
		return 1
	}
	fmt.Fprintf(out, "Live until %s. From a NEW session, run: andaru ctl applies confirm %s\n", printable(reply.Deadline), printable(reply.ID))
	return 0
}

// acceptDetected takes the host over with exactly what was detected, asking nothing.
func acceptDetected(ctx context.Context, c takeoverClient, p proposal, timeout int, out io.Writer, hold func()) int {
	if p.State != "ready" {
		fmt.Fprintln(out, stateAdvice(p.State))
		for _, f := range p.Findings {
			fmt.Fprintln(out, "  "+findingLine(f))
		}
		return 1
	}
	if refused := errorFindings(p.Findings); len(refused) > 0 {
		for _, f := range refused {
			fmt.Fprintln(out, "  "+findingLine(f))
		}
		return 1
	}
	return commit(ctx, c, p, p.Operations, timeout, out, hold)
}

func stateAdvice(state string) string {
	switch state {
	case "taken-over":
		return "This host is already taken over; change it with andaru ctl config and andaru ctl apply network."
	case "saved-not-applied":
		return "A network configuration is already saved but not applied; apply it with andaru ctl apply network --takeover, or delete it with andaru ctl config, then run andaru ctl takeover again."
	}
	return "This host cannot be taken over as it stands:"
}

func isTerminal(stdin io.Reader, stdout io.Writer) bool {
	for _, s := range []any{stdin, stdout} {
		f, ok := s.(*os.File)
		if !ok {
			return false
		}
		if _, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS); err != nil {
			return false
		}
	}
	return true
}

func runTakeover(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("takeover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accept := fs.Bool("accept-detected", false, "take over with the detected settings, without questions")
	timeout := fs.Int("confirm-timeout", 120, "seconds until the change is rolled back unless confirmed (30-600)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *timeout < 30 || *timeout > 600 {
		fmt.Fprintln(stderr, "usage: andaru ctl takeover [--accept-detected] [--confirm-timeout S]")
		return 2
	}
	// huh would fall back to line prompts on a dumb terminal, where end of input reads as Back forever.
	if !*accept && (!isTerminal(stdin, stdout) || os.Getenv("TERM") == "dumb") {
		fmt.Fprintln(stderr, "andaru ctl takeover asks questions and needs a terminal; use --accept-detected to take over with the detected settings.")
		return 2
	}
	// The local end of the SSH session tells the engine which segment the operator manages the host through.
	via := ""
	if f := strings.Fields(os.Getenv("SSH_CONNECTION")); len(f) == 4 {
		via = f[2]
	}
	c := &httpTakeoverClient{socket: socket, via: via}
	p, err := c.Proposal(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	hold := func() { signal.Ignore(syscall.SIGHUP, syscall.SIGINT) }
	if *accept {
		return acceptDetected(ctx, c, p, *timeout, stdout, hold)
	}
	return interactive(ctx, c, p, *timeout, stdin, stdout, hold)
}

type httpTakeoverClient struct{ socket, via string }

func (c *httpTakeoverClient) do(ctx context.Context, method, path string, body any, timeout time.Duration, out any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("could not reach the local engine: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit {
		return errors.New("engine response exceeded its read or size budget")
	}
	if response.StatusCode != http.StatusOK {
		var envelope struct{ Error apiError }
		if json.Unmarshal(data, &envelope) != nil || envelope.Error.Code == "" {
			return fmt.Errorf("engine returned HTTP %d", response.StatusCode)
		}
		envelope.Error.Status = response.StatusCode
		return &envelope.Error
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("engine returned an invalid response")
	}
	return nil
}

func (c *httpTakeoverClient) Proposal(ctx context.Context) (p proposal, err error) {
	path := "/api/v1/takeover/proposal"
	if c.via != "" {
		path += "?via=" + url.QueryEscape(c.via)
	}
	err = c.do(ctx, http.MethodGet, path, nil, 15*time.Second, &p)
	return
}

func (c *httpTakeoverClient) Draft(ctx context.Context, ops []config.Operation) (d draft, err error) {
	err = c.do(ctx, http.MethodPost, "/api/v1/preview/network/draft", map[string]any{"operations": ops}, 10*time.Second, &d)
	return
}

func (c *httpTakeoverClient) Save(ctx context.Context, ops []config.Operation, head config.Version) (config.Version, error) {
	var reply struct{ SavedVersion config.Version }
	err := c.do(ctx, http.MethodPost, "/api/v1/config/changes", map[string]any{"expectedSavedVersion": head, "operations": ops}, 10*time.Second, &reply)
	return reply.SavedVersion, err
}

func (c *httpTakeoverClient) Applies(ctx context.Context) (string, error) {
	var reply struct{ Pointers struct{ Pending *string } }
	if err := c.do(ctx, http.MethodGet, "/api/v1/applies", nil, 5*time.Second, &reply); err != nil {
		return "", err
	}
	if reply.Pointers.Pending == nil {
		return "", nil
	}
	return *reply.Pointers.Pending, nil
}

func (c *httpTakeoverClient) Apply(ctx context.Context, saved config.Version, timeout int) (reply applyReply, err error) {
	body := map[string]any{"savedVersion": saved, "takeover": true, "confirmTimeoutSeconds": timeout}
	err = c.do(ctx, http.MethodPost, "/api/v1/applies", body, 160*time.Second, &reply)
	return
}

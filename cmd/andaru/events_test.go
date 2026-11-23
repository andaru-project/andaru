package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
)

// A verb on a generation tells the bus, whether or not it succeeded; without the bus it only warns.
func TestRealizeTellsTheBusOfEachApply(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "bus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	events, err := bus.Open(bus.Options{
		Path: filepath.Join(dir, "bus.db"), Grants: map[string]bus.Grants{"realize": {Publish: []string{"apply/+"}}},
		Authenticate: func(_, _ []byte) bool { return true }, Listeners: map[string]net.Listener{"root": listener},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	got := make(chan string, 4)
	if _, err = events.Subscribe("apply/+", func(topic string, payload []byte) {
		var event bus.Event
		_ = json.Unmarshal(payload, &event)
		got <- topic + " " + event.Type + " " + event.Source
	}); err != nil {
		t.Fatal(err)
	}
	realize := func(args string, verbs *fakeVerbs) (int, string) {
		var stderr bytes.Buffer
		code := realizeCommand(context.Background(), strings.Fields(args), nil, &stderr, func(string) (realizeVerbs, error) { return verbs, nil })
		return code, stderr.String()
	}
	for _, row := range []struct {
		args string
		err  error
		code int
	}{
		{"--bus-socket " + socket + " confirm 4", nil, 0},
		{"--bus-socket " + socket + " activate 5", errors.New("verification failed"), 1},
	} {
		if code, stderr := realize(row.args, &fakeVerbs{err: row.err}); code != row.code || strings.Contains(stderr, "event bus") {
			t.Fatalf("%s: exit %d, %q", row.args, code, stderr)
		}
		select {
		case g := <-got:
			if want := "apply/" + strings.Fields(row.args)[3] + " apply.changed realize"; g != want {
				t.Fatalf("got %q, want %q", g, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the bus was not told", row.args)
		}
	}
	if code, stderr := realize("leases", &fakeVerbs{}); code != 0 || stderr != "" {
		t.Fatalf("leases: exit %d, %q", code, stderr)
	}
	if code, stderr := realize("--bus-socket "+filepath.Join(dir, "none.sock")+" confirm 4", &fakeVerbs{}); code != 0 || !strings.Contains(stderr, "event bus not told") {
		t.Fatalf("without a bus: exit %d, %q", code, stderr)
	}
	select {
	case g := <-got:
		t.Fatalf("unexpected %q", g)
	case <-time.After(100 * time.Millisecond):
	}
}

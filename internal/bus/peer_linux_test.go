package bus

import (
	"bufio"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// peerBus serves a PeerListener whose PeerUser maps this process's user ID to name.
func peerBus(t *testing.T, peerUser func(uint32) (string, error)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bus") // a Unix socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(Options{
		Path: filepath.Join(dir, "bus.db"),
		Grants: map[string]Grants{
			"svc":   {Publish: []string{"notification/delivery"}, Subscribe: []string{"delivery/#"}},
			"other": {Publish: []string{"#"}, Subscribe: []string{"#"}},
		},
		// A token is no way in on a PeerListener.
		Authenticate:  func(_, _ string) bool { return true },
		PeerListeners: map[string]net.Listener{"services": l},
		PeerUser:      peerUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return socket
}

// say sends frames in order and returns the reply to each.
func say(t *testing.T, socket string, lines ...string) []string {
	t.Helper()
	nc, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(nc)
	var replies []string
	for _, line := range lines {
		if _, err := nc.Write([]byte(line + "\n")); err != nil {
			break
		}
		reply, err := r.ReadString('\n')
		if err != nil {
			break
		}
		replies = append(replies, strings.TrimSpace(reply))
	}
	return replies
}

func mine(name string) func(uint32) (string, error) {
	return func(uid uint32) (string, error) {
		if int(uid) != os.Getuid() {
			return "", errors.New("unknown user")
		}
		return name, nil
	}
}

func TestPeerListenerKnowsClientsByUserID(t *testing.T) {
	refused := `{"op":"error","id":1,"message":"not authorized"}`
	for _, s := range []struct {
		name     string
		peerUser func(uint32) (string, error)
		hello    string
		want     string
	}{
		{"its own name", mine("svc"), `{"op":"hello","id":1,"user":"svc"}`, `{"op":"ok","id":1}`},
		{"another user's name, with a token", mine("svc"), `{"op":"hello","id":1,"user":"other","token":"secret"}`, refused},
		{"a user ID the bus cannot name", func(uint32) (string, error) { return "", errors.New("no such user") }, `{"op":"hello","id":1,"user":"svc"}`, refused},
		{"no PeerUser", nil, `{"op":"hello","id":1,"user":"svc"}`, refused},
	} {
		if got := say(t, peerBus(t, s.peerUser), s.hello); len(got) != 1 || got[0] != s.want {
			t.Errorf("%s: hello answered %q, want %q", s.name, got, s.want)
		}
	}
}

func TestPeerListenerKeepsGrantsPerUser(t *testing.T) {
	socket := peerBus(t, mine("svc"))
	got := say(t, socket,
		`{"op":"hello","id":1,"user":"svc"}`,
		`{"op":"subscribe","id":2,"filter":"delivery/#"}`,
		`{"op":"publish","id":3,"topic":"notification/delivery","payload":{}}`,
		`{"op":"subscribe","id":4,"filter":"config/#"}`,
		`{"op":"publish","id":5,"topic":"config/head","payload":{}}`,
	)
	if len(got) != 5 || !strings.Contains(got[1], `"op":"ok"`) || !strings.Contains(got[2], `"op":"ok"`) ||
		!strings.Contains(got[3], `"op":"error"`) || !strings.Contains(got[4], `"op":"error"`) {
		t.Fatalf("replies = %q", got)
	}
}

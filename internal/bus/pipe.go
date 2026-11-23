package bus

import (
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// pipeListener hands out connections to the engine's own clients, which never leave the process.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

// dial connects through a socket pair: unlike net.Pipe it buffers, and a packet a client writes in several pieces
// cannot interleave with another, which mochi would read as malformed.
func (l *pipeListener) dial() (net.Conn, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	client, err := fileConn(fds[0])
	if err != nil {
		unix.Close(fds[1])
		return nil, err
	}
	server, err := fileConn(fds[1])
	if err != nil {
		client.Close()
		return nil, err
	}
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		client.Close()
		server.Close()
		return nil, net.ErrClosed
	}
}

func fileConn(fd int) (net.Conn, error) {
	f := os.NewFile(uintptr(fd), "bus")
	defer f.Close()
	return net.FileConn(f)
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return engineListener }

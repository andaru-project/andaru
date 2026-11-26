package bus

import (
	"errors"
	"net"
	"syscall"
)

// PeerUID is the user ID of the process on the other end of a Unix socket.
func PeerUID(nc net.Conn) (uint32, error) {
	unix, ok := nc.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a Unix socket")
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	if cred == nil {
		return 0, errors.New("missing Unix peer credentials")
	}
	return cred.Uid, nil
}

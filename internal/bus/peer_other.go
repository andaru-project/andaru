//go:build !linux

package bus

import (
	"errors"
	"net"
)

func peerUID(net.Conn) (uint32, error) { return 0, errors.New("peer credentials need Linux") }

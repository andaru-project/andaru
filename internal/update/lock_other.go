//go:build !linux

package update

import (
	"errors"
	"sync"
)

// ErrLocked is returned when another update operation already holds the update lock.
var ErrLocked = errors.New("another software update operation is already running")

var lockMu sync.Mutex

func acquireLock(_ string) (func(), error) {
	if !lockMu.TryLock() {
		return nil, ErrLocked
	}
	return func() { lockMu.Unlock() }, nil
}

//go:build !linux

package addon

import "os"

func lockFile(_ *os.File) error { return nil }

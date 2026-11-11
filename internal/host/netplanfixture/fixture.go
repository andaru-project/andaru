// Package netplanfixture holds the files netplan generated on the lab VM, embedded so that a test
// binary copied to another host still has them.
package netplanfixture

import (
	"embed"
	"io/fs"
	"os"
	"testing"
)

//go:embed static dhcp
var files embed.FS

// Root copies the named fixture into a fresh directory and returns it, for use as a host root.
func Root(t testing.TB, name string) string {
	t.Helper()
	sub, err := fs.Sub(files, name)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.CopyFS(root, sub); err != nil {
		t.Fatal(err)
	}
	return root
}

//go:build !integration

// Not in the integration binaries: CI runs them away from the repository, where packaging/man is not.

package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/manpage"
)

var updateMan = flag.Bool("update-man", false, "rewrite packaging/man from the help text")

// The man page the image ships is the help text, rendered: run `go test ./cmd/andaru ./cmd/andaru-web -update-man` after a help change.
func TestManPageMatchesTheHelp(t *testing.T) {
	page := manpage.Page("andaru-web", "8", "serve the Andaru web interface", help)
	path := filepath.Join("..", "..", "packaging", "man", "andaru-web.8")
	if *updateMan {
		if err := os.WriteFile(path, page, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, page) {
		t.Errorf("%s does not match the help (%v): run go test ./cmd/andaru ./cmd/andaru-web -update-man", path, err)
	}
}

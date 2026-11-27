//go:build !integration

// Not in the integration binaries: CI runs them away from the repository, where packaging/man is not.

package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/manpage"
)

var updateMan = flag.Bool("update-man", false, "rewrite packaging/man from the help texts")

// The man pages the image ships are the help texts, rendered. After a help change, run
// go test ./cmd/andaru ./cmd/andaru-web -update-man.
func TestManPagesMatchTheHelp(t *testing.T) {
	var ctl bytes.Buffer
	ctlHelp(&ctl)
	var details []manpage.Section
	for _, c := range ctlCommands {
		details = append(details, manpage.Section{Title: "andaru ctl " + c.name, Help: c.help()})
	}
	checkManPages(t, map[string][]byte{
		"andaru.8":          manpage.Page("andaru", "8", "the Andaru firewall platform", topHelp),
		"andaru-engine.8":   manpage.Page("andaru-engine", "8", "run the Andaru engine", engineHelp),
		"andaru-ctl.8":      manpage.Page("andaru-ctl", "8", "read and change the configuration through the running engine", ctl.String(), details...),
		"andaru-realize.8":  manpage.Page("andaru-realize", "8", "privileged host steps of Andaru", realizeHelp),
		"andaru-outbound.8": manpage.Page("andaru-outbound", "8", "the service that talks to the outside world for Andaru", outboundHelp),
	})
}

func checkManPages(t *testing.T, pages map[string][]byte) {
	t.Helper()
	dir := filepath.Join("..", "..", "packaging", "man")
	// Every page there ships: one for a command gone is an error. andaru-web's own test checks its page.
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, ok := pages[f.Name()]; !ok && strings.HasSuffix(f.Name(), ".8") && f.Name() != "andaru-web.8" {
			t.Errorf("%s/%s is the page of no command: remove it", dir, f.Name())
		}
	}
	for name, page := range pages {
		path := filepath.Join(dir, name)
		if *updateMan {
			if err := os.WriteFile(path, page, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, page) {
			t.Errorf("%s does not match the help (%v): run go test ./cmd/andaru ./cmd/andaru-web -update-man", path, err)
		}
	}
}

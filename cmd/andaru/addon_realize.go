package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/generation"
)

var addonStagerForState = func(stateDir string) addon.Stager {
	return addon.Stager{
		StateDir: stateDir,
		ReferencedImages: func() (map[string]bool, error) {
			return generation.ReferencedAddonImages(stateDir)
		},
	}
}

func addonStageVerb(ctx context.Context, verb, stateDir, slug string, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "realize requires root")
		return 1
	}
	stager := addonStagerForState(stateDir)
	var err error
	switch verb {
	case "addon-stage":
		_, err = stager.Stage(ctx, slug)
	case "addon-unstage":
		err = stager.Unstage(ctx, slug)
	default:
		err = fmt.Errorf("unknown add-on staging verb %q", verb)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/generation"
)

func addonStageVerb(ctx context.Context, verb, stateDir, target string, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "realize requires root")
		return 1
	}
	stager := addon.Stager{
		StateDir: stateDir,
		ReferencedImages: func() (map[string]bool, error) {
			return generation.ReferencedAddonImages(stateDir)
		},
	}
	var err error
	switch verb {
	case "addon-stage":
		_, err = stager.Stage(ctx, target)
	case "addon-unstage":
		if addon.ValidDigestImage(target) {
			err = stager.UnstageDigest(ctx, target)
		} else {
			err = stager.Unstage(ctx, target)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

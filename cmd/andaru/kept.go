package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/kept"
)

func storeRecovery(ctx context.Context, stateDir string, args []string, stdout, stderr io.Writer, load func() ([]byte, map[string]string, error)) int {
	if len(args) == 1 && args[0] == "store-recovery" {
		args = nil
	}
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: andaru ctl kept store-recovery")
		return 2
	}
	if load == nil {
		load = func() ([]byte, map[string]string, error) {
			store, err := config.Open(ctx, stateDir)
			if err != nil {
				return nil, nil, err
			}
			defer store.Close()
			input, err := store.RealizationInput(ctx, nil)
			if err != nil {
				return nil, nil, err
			}
			return input.Value, input.Secrets, nil
		}
	}
	decl, secrets, err := load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	body, err := json.Marshal(struct {
		Declaration json.RawMessage   `json:"declaration"`
		Secrets     map[string]string `json:"secrets"`
	}{decl, secrets})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := kept.Open(stateDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = store.Put(ctx, "recovery", body, true); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "stored")
	return 0
}

package main

import (
	"context"
	"strconv"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
)

// tellApply publishes that generation id changed state on the engine's event bus, so the web UI hears of it
// without asking. It is not durable: the engine may be down, and a subscriber reads the state again whenever it
// reconnects.
func tellApply(ctx context.Context, socket string, id int) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client, err := bus.Dial(ctx, socket, "realize", "")
	if err != nil {
		return err
	}
	defer client.Close()
	payload, err := bus.NewEvent("apply.changed", "realize", map[string]int{"id": id})
	if err != nil {
		return err
	}
	return client.Publish(ctx, "apply/"+strconv.Itoa(id), payload, false)
}
